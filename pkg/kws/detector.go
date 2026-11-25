package kws

// The detection gate: Consecutive windows in a row at or above the threshold make a detection (a word stays above it
// for a few 80 ms steps, a one-off spike on other speech rarely does), then Refractory embeddings (2.88 s) are not
// scored, so the command that follows the word never fires it again.
const (
	Consecutive = 2
	Refractory  = 36
	// DefaultSensitivity keeps the model's own threshold
	DefaultSensitivity = 0.5
	// a sensitivity of 1 lowers the threshold by sensitivitySpan/2, 0 raises it as much
	sensitivitySpan = 0.4
)

// Threshold is the threshold a detector uses for m at sensitivity (0 to 1; higher wakes more easily).
func Threshold(m *Model, sensitivity float32) float32 {
	s := min(max(sensitivity, 0), 1)
	return min(max(m.Threshold+(DefaultSensitivity-s)*sensitivitySpan, 0.05), 0.99)
}

type gate struct{ run, quiet int }

func (g *gate) step(score, threshold float32) bool {
	switch {
	case g.quiet > 0:
		g.quiet--
		g.run = 0
		return false
	case score < threshold:
		g.run = 0
		return false
	}
	g.run++
	if g.run < Consecutive {
		return false
	}
	g.run, g.quiet = 0, Refractory
	return true
}

// Detector runs one model on one microphone stream. Not safe for concurrent use.
type Detector struct {
	feat      *Features
	head      *Head
	threshold float32
	window    []float32 // the last Context embeddings, oldest first
	have      int
	gate      gate
	last      float32
}

func NewDetector(m *Model, sensitivity float32) (*Detector, error) {
	if err := m.Head.Check(); err != nil {
		return nil, err
	}
	f, err := NewFeatures()
	if err != nil {
		return nil, err
	}
	return &Detector{feat: f, head: m.Head, threshold: Threshold(m, sensitivity),
		window: make([]float32, Context*EmbeddingDim)}, nil
}

// Push feeds 16 kHz samples; hit runs, within Push, for each detection with its score.
func (d *Detector) Push(pcm []int16, hit func(score float32)) {
	d.feat.Push(pcm, func(e []float32) {
		copy(d.window, d.window[EmbeddingDim:])
		copy(d.window[(Context-1)*EmbeddingDim:], e)
		if d.have < Context {
			d.have++
			if d.have < Context {
				return
			}
		}
		d.last = d.head.Score(d.window)
		if d.gate.step(d.last, d.threshold) && hit != nil {
			hit(d.last)
		}
	})
}

// Reset forgets the stream: after a gap (a mute, lost frames, a session) the next samples start a new one.
func (d *Detector) Reset() {
	d.feat.Reset()
	d.have = 0
	d.gate = gate{}
	d.last = 0
}

// Score is the last window's score, 0 before the first full one.
func (d *Detector) Score() float32 { return d.last }
