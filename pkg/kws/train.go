package kws

import (
	"context"
	"math"
	"math/rand/v2"
	"runtime"
	"slices"
	"sync"
	"time"
)

// TrainOptions tune TrainWord; zero values take the defaults.
type TrainOptions struct {
	Word string
	Seed uint64
	// goroutines that embed and train; 0 is up to 4, leaving a core free
	Workers int
	// passes over the data; 0 is 40
	Epochs int
	// stages, in order: embed, train, calibrate; fraction 0 to 1. Called from TrainWord's goroutines, one at a time
	Progress func(stage string, fraction float64)
	Now      func() time.Time
}

// TrainError is a training that could not run, by a code the owner can be shown.
type TrainError struct{ Code string }

func (e *TrainError) Error() string { return "kws: training " + e.Code }

const (
	// the first embedding of a stream covers the audio up to the end of its 10th step
	firstStep = (WindowFrames + streamPad + stepFrames - 1) / stepFrames
	// audio before the word in an augmented take: more than Context embeddings, so its first windows hold no word
	preSamples = 2400 * SampleRate / 1000
	// audio after it, which the positive windows reach into
	postSamples = 500 * SampleRate / 1000
	// a window that ends this soon after the word ends is a positive
	posSlack = 400 * SampleRate / 1000
	// silence kept around a trimmed take
	marginSamples = 100 * SampleRate / 1000
	// augmented copies of each take: at least, and about this many in all
	minCopies   = 12
	totalCopies = 360
	// copies of each held-out take
	heldOutCopies = 12
	batchSize     = 128
	learningRate  = 1e-3
	l2            = 1e-4
	// loss weight of a positive, and of a window of the owner's own room or speech
	posWeight  = 2
	userWeight = 3
	// thresholds calibration may choose
	minThreshold = 0.6
	maxThreshold = 0.97
	margin       = 0.05
)

// embEnd is the sample just past the audio embedding e of a fresh stream covers.
func embEnd(e int) int { return (e + firstStep) * StepSamples }

type sample struct {
	x []float32
	y float32
	w float32
}

// TrainWord learns en's word against neg: it trims the takes, mixes them into the room and the owner's speech at
// random speeds, levels and noise, trains a head, and calibrates its threshold to the lowest without false detections
// on held-out room, speech and negatives, plus a margin. Codes: too_few_takes (fewer than MinTakes with a clear word),
// no_ambient, no_negatives, cancelled.
func TrainWord(ctx context.Context, en *Enrollment, neg *Negatives, o TrainOptions) (*Model, error) {
	if o.Workers <= 0 {
		o.Workers = max(1, min(runtime.NumCPU()-1, 4))
	}
	if o.Epochs <= 0 {
		o.Epochs = 40
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	progress := o.Progress
	if progress == nil {
		progress = func(string, float64) {}
	}
	var notes []string
	if neg == nil || len(neg.Windows)+len(neg.Streams) == 0 {
		return nil, &TrainError{Code: "no_negatives"}
	}
	if len(en.Ambient) < SampleRate {
		return nil, &TrainError{Code: "no_ambient"}
	}
	var words [][]int16
	for _, t := range en.Takes {
		start, end, ok := findWord(t)
		if !ok {
			notes = append(notes, "take_skipped")
			continue
		}
		words = append(words, t[max(0, start-marginSamples):min(len(t), end+marginSamples)])
	}
	if len(words) < MinTakes {
		return nil, &TrainError{Code: "too_few_takes"}
	}
	backgrounds := [][]int16{en.Ambient}
	if len(en.Speech) >= SampleRate {
		backgrounds = append(backgrounds, en.Speech)
	}

	// every 4th take is held out for calibration
	var train, held [][]int16
	for i, w := range words {
		if len(words) >= 4 && i%4 == 3 {
			held = append(held, w)
		} else {
			train = append(train, w)
		}
	}
	if len(held) == 0 {
		held = train[len(train)-1:]
	}
	type clip struct {
		word []int16
		held bool
	}
	var clips []clip
	copies := max(minCopies, totalCopies/len(train))
	for _, w := range train {
		for range copies {
			clips = append(clips, clip{w, false})
		}
	}
	for _, w := range held {
		for range heldOutCopies {
			clips = append(clips, clip{w, true})
		}
	}
	// the streams: the clips, then the room and the speech whole
	streams := make([][]float32, len(clips)+2)
	ends := make([]int, len(clips))
	var done int
	var mu sync.Mutex
	err := parallel(ctx, o.Workers, len(streams), func(f *Features, i int) {
		r := rand.New(rand.NewPCG(o.Seed, uint64(i)+1)) //nolint:gosec // augmentation, no secret
		switch {
		case i < len(clips):
			pcm, end := augment(r, clips[i].word, backgrounds)
			ends[i] = end
			streams[i] = embed(f, pcm)
		case i == len(clips):
			streams[i] = embed(f, en.Ambient)
		default:
			streams[i] = embed(f, en.Speech)
		}
		mu.Lock()
		done++
		progress("embed", float64(done)/float64(len(streams)))
		mu.Unlock()
	})
	if err != nil {
		return nil, err
	}

	var pos, negs, user []sample
	var heldHits [][][]float32
	for i, c := range clips {
		s := streams[i]
		var hits [][]float32
		for e := Context - 1; e < len(s)/EmbeddingDim; e++ {
			end := embEnd(e)
			switch {
			case end >= ends[i] && end <= ends[i]+posSlack:
				hits = append(hits, window(s, e))
			case !c.held && end <= preSamples && e%2 == 0:
				// only the room or the speech before the word
				user = append(user, sample{window(s, e), 0, userWeight})
			}
		}
		if c.held {
			heldHits = append(heldHits, hits)
			continue
		}
		for _, h := range hits {
			pos = append(pos, sample{h, 1, posWeight})
		}
	}
	if len(pos) == 0 {
		return nil, &TrainError{Code: "too_few_takes"}
	}
	// the owner's room and speech: the first 80 % to train on, the rest to calibrate
	var eval [][]float32
	split := func(s []float32, frac float64, into *[]sample, weight float32) {
		n := len(s) / EmbeddingDim
		cut := int(float64(n) * frac)
		for e := Context - 1; e < cut; e++ {
			*into = append(*into, sample{window(s, e), 0, weight})
		}
		eval = append(eval, s[cut*EmbeddingDim:])
	}
	split(streams[len(clips)], 0.8, &user, userWeight)
	if len(en.Speech) > 0 {
		split(streams[len(clips)+1], 0.8, &user, userWeight)
	}
	for _, w := range neg.Windows {
		if len(w) == Context*EmbeddingDim {
			negs = append(negs, sample{w, 0, 1})
		}
	}
	for _, s := range neg.Streams {
		split(s, 0.75, &negs, 1)
	}

	r := rand.New(rand.NewPCG(o.Seed, 0x5eed)) //nolint:gosec // training's draws, no secret
	head, err := fit(ctx, r, pos, user, negs, o, func(f float64) { progress("train", f) })
	if err != nil {
		return nil, err
	}

	progress("calibrate", 0)
	m := &Model{Format: ModelFormat, Word: o.Word, Created: o.Now().UTC(), Takes: len(words), Head: head,
		Notes: compactNotes(notes)}
	scores := make([][]float32, len(eval))
	var embeddings int
	for i, s := range eval {
		for e := Context - 1; e < len(s)/EmbeddingDim; e++ {
			scores[i] = append(scores[i], head.Score(window(s, e)))
		}
		embeddings += len(s) / EmbeddingDim
	}
	m.EvalHours = float64(embeddings*StepSamples) / SampleRate / 3600
	triggers := func(t float32) int {
		n := 0
		for _, sc := range scores {
			var g gate
			for _, v := range sc {
				if g.step(v, t) {
					n++
				}
			}
		}
		return n
	}
	// a held-out take is found when two windows in a row pass: what the gate needs
	best := make([]float32, len(heldHits))
	for i, hits := range heldHits {
		for j := 1; j < len(hits); j++ {
			best[i] = max(best[i], min(head.Score(hits[j-1]), head.Score(hits[j])))
		}
	}
	threshold := float32(maxThreshold)
	for t := float32(0.3); t <= maxThreshold; t += 0.01 {
		if triggers(t) == 0 {
			threshold = t
			break
		}
	}
	m.Threshold = min(max(threshold+margin, minThreshold), maxThreshold)
	found := 0
	for _, b := range best {
		if b >= m.Threshold {
			found++
		}
	}
	m.Recall = float64(found) / float64(max(len(best), 1))
	m.FalsePerHour = float64(triggers(m.Threshold)) / math.Max(m.EvalHours, 1e-9)
	progress("calibrate", 1)
	return m, nil
}

func compactNotes(notes []string) []string {
	slices.Sort(notes)
	return slices.Compact(notes)
}

func window(s []float32, e int) []float32 {
	return s[(e-Context+1)*EmbeddingDim : (e+1)*EmbeddingDim : (e+1)*EmbeddingDim]
}

// embed is pcm's embedding stream from a fresh f, n x EmbeddingDim.
func embed(f *Features, pcm []int16) []float32 {
	f.Reset()
	out := make([]float32, 0, (len(pcm)/StepSamples)*EmbeddingDim)
	f.Push(pcm, func(e []float32) { out = append(out, e...) })
	return out
}

// parallel runs fn for each i below n on workers goroutines, each with Features of its own.
func parallel(ctx context.Context, workers, n int, fn func(f *Features, i int)) error {
	next := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		f, err := NewFeatures()
		if err != nil {
			return err
		}
		wg.Go(func() {
			for i := range next {
				fn(f, i)
			}
		})
	}
	var err error
	for i := range n {
		if ctx.Err() != nil {
			err = &TrainError{Code: "cancelled"}
			break
		}
		next <- i
	}
	close(next)
	wg.Wait()
	return err
}

// fit trains a head with Adam on the positives against the owner's negatives and the bundled ones. Each epoch takes
// every positive and every window of the owner's, a random draw of the bundled negatives, and, from the 4th epoch on,
// the bundled negatives the head currently scores highest, so its capacity goes to near misses rather than silence.
func fit(ctx context.Context, r *rand.Rand, pos, user, negs []sample, o TrainOptions, progress func(float64)) (*Head, error) {
	h := newHead(r)
	params := h.tensors()
	m1 := make([][]float32, len(params))
	m2 := make([][]float32, len(params))
	for i, p := range params {
		m1[i] = make([]float32, len(p))
		m2[i] = make([]float32, len(p))
	}
	grads := make([]*Head, o.Workers)
	gradTensors := make([][][]float32, o.Workers)
	for i := range grads {
		grads[i] = zeroHead()
		gradTensors[i] = grads[i].tensors()
	}
	draw := min(len(negs), max(4*len(pos), 6000))
	var hard []sample
	const beta1, beta2, eps = 0.9, 0.999, 1e-8
	step := 0
	for epoch := range o.Epochs {
		if ctx.Err() != nil {
			return nil, &TrainError{Code: "cancelled"}
		}
		if epoch >= 4 && epoch%4 == 0 {
			hard = hardest(h, negs, max(len(pos), 1000), o.Workers)
		}
		batch := slices.Concat(pos, user, hard)
		for range draw {
			batch = append(batch, negs[r.IntN(len(negs))])
		}
		r.Shuffle(len(batch), func(i, j int) { batch[i], batch[j] = batch[j], batch[i] })
		for start := 0; start < len(batch); start += batchSize {
			part := batch[start:min(start+batchSize, len(batch))]
			var wg sync.WaitGroup
			for w, g := range grads {
				for _, t := range gradTensors[w] {
					clear(t)
				}
				wg.Go(func() {
					var ps pass
					for i := w; i < len(part); i += len(grads) {
						s := part[i]
						p := h.forward(s.x, &ps)
						h.backward(s.x, &ps, p, s.y, s.w, g)
					}
				})
			}
			wg.Wait()
			step++
			lr := learningRate * math.Sqrt(1-math.Pow(beta2, float64(step))) / (1 - math.Pow(beta1, float64(step)))
			scale := 1 / float32(len(part))
			for pi, p := range params {
				for j := range p {
					var g float32
					for _, gt := range gradTensors {
						g += gt[pi][j]
					}
					g = g*scale + l2*p[j]
					m1[pi][j] = beta1*m1[pi][j] + (1-beta1)*g
					m2[pi][j] = beta2*m2[pi][j] + (1-beta2)*g*g
					p[j] -= float32(lr * float64(m1[pi][j]) / (math.Sqrt(float64(m2[pi][j])) + eps))
				}
			}
		}
		progress(float64(epoch+1) / float64(o.Epochs))
	}
	return h, nil
}

// hardest is the k negatives h scores highest.
func hardest(h *Head, negs []sample, k, workers int) []sample {
	scores := make([]float32, len(negs))
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			var ps pass
			for i := w; i < len(negs); i += workers {
				scores[i] = h.forward(negs[i].x, &ps)
			}
		})
	}
	wg.Wait()
	idx := make([]int, len(negs))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(a, b int) int {
		switch {
		case scores[a] > scores[b]:
			return -1
		case scores[a] < scores[b]:
			return 1
		}
		return a - b
	})
	out := make([]sample, 0, min(k, len(idx)))
	for _, i := range idx[:min(k, len(idx))] {
		out = append(out, negs[i])
	}
	return out
}

// findWord finds the take's word with an energy detector on 10 ms frames: the run of frames around the loudest one
// within 25 dB of it and well above the floor (gaps under 200 ms bridged), widened by the softer frames that start and
// end it. Its bounds are relative to the take's own peak, so a word recorded close or across the room trims the same.
func findWord(pcm []int16) (start, end int, ok bool) {
	const frame = SampleRate / 100
	n := len(pcm) / frame
	if n < 20 {
		return 0, 0, false
	}
	db := make([]float64, n)
	peak := 0
	for i := range n {
		var e float64
		for _, s := range pcm[i*frame : (i+1)*frame] {
			e += float64(s) * float64(s)
		}
		db[i] = 10 * math.Log10(e/frame+1)
		if db[i] > db[peak] {
			peak = i
		}
	}
	sorted := slices.Clone(db)
	slices.Sort(sorted)
	floor, top := sorted[n/6], db[peak]
	if top < 40 || top-floor < 12 {
		return 0, 0, false
	}
	loud := max(floor+0.4*(top-floor), top-25)
	soft := floor + 6
	reach := func(dir int) int {
		last := peak
		for i, gap := peak+dir, 0; i >= 0 && i < n && gap <= 20; i += dir {
			if db[i] >= loud {
				last, gap = i, 0
			} else {
				gap++
			}
		}
		for k := 0; k < 15; k++ {
			i := last + dir
			if i < 0 || i >= n || db[i] < soft {
				break
			}
			last = i
		}
		return last
	}
	start, end = reach(-1)*frame, (reach(1)+1)*frame
	if d := end - start; d < SampleRate/5 || d > 2*SampleRate {
		return 0, 0, false
	}
	return start, end, true
}

// augment places word, at a random speed (0.9 to 1.1) and level (-8 to +6 dB), after preSamples of a background and
// before postSamples more, the background mixed in at 5 to 30 dB below it. It answers the clip and where the word ends.
func augment(r *rand.Rand, word []int16, backgrounds [][]int16) ([]int16, int) {
	speed := 0.9 + 0.2*r.Float64()
	w := make([]float64, len(word))
	for i, s := range word {
		w[i] = float64(s)
	}
	w = stretch(w, speed)
	gain := math.Pow(10, (-8+14*r.Float64())/20)
	bg := backgrounds[r.IntN(len(backgrounds))]
	n := preSamples + len(w) + postSamples
	off := r.IntN(len(bg))
	snr := 5 + 25*r.Float64()
	var wordPower, bgPower float64
	for _, v := range w {
		wordPower += v * v
	}
	for i := range n {
		v := float64(bg[(off+i)%len(bg)])
		bgPower += v * v
	}
	wordRMS := math.Sqrt(wordPower/float64(max(len(w), 1))) * gain
	bgRMS := math.Sqrt(bgPower/float64(n)) + 1e-9
	bgScale := wordRMS / bgRMS / math.Pow(10, snr/20)
	out := make([]int16, n)
	for i := range out {
		v := float64(bg[(off+i)%len(bg)]) * bgScale
		if j := i - preSamples; j >= 0 && j < len(w) {
			v += w[j] * gain
		}
		out[i] = clip16(v)
	}
	return out, preSamples + len(w)
}

// stretch plays x faster by factor (pitch along), with linear interpolation.
func stretch(x []float64, factor float64) []float64 {
	n := int(float64(len(x)) / factor)
	out := make([]float64, n)
	for i := range out {
		p := float64(i) * factor
		j := int(p)
		if j+1 >= len(x) {
			out[i] = x[len(x)-1]
			continue
		}
		f := p - float64(j)
		out[i] = x[j]*(1-f) + x[j+1]*f
	}
	return out
}
