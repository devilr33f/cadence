package kws

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

// a made-up word: two tone glides with a gap, unlike the noise and the hums the negatives hold
func fakeWord(r *rand.Rand, level float64) []int16 {
	pcm := make([]int16, SampleRate*3/2)
	start := SampleRate/2 + r.IntN(SampleRate/10)
	for i := range SampleRate * 6 / 10 {
		t := float64(i) / SampleRate
		var v float64
		switch {
		case t < 0.25:
			v = math.Sin(2 * math.Pi * (400*t + 1600*t*t))
		case t > 0.35:
			u := t - 0.35
			v = math.Sin(2*math.Pi*(1500*u-1200*u*u)) * 0.8
		}
		pcm[start+i] = clip16(v * level)
	}
	for i := range pcm {
		pcm[i] += int16(r.NormFloat64() * 30)
	}
	return pcm
}

func noise(r *rand.Rand, seconds float64, level float64) []int16 {
	pcm := make([]int16, int(seconds*SampleRate))
	for i := range pcm {
		pcm[i] = clip16(r.NormFloat64() * level)
	}
	return pcm
}

// negatives made here: embeddings of hums and noise, as the real set's speech and music would be
func fakeNegatives(t *testing.T, r *rand.Rand) *Negatives {
	t.Helper()
	f, err := NewFeatures()
	if err != nil {
		t.Fatal(err)
	}
	n := &Negatives{Source: "test"}
	for k := range 6 {
		pcm := noise(r, 12, 200+float64(k)*300)
		for i := range pcm {
			pcm[i] += clip16(2000 * math.Sin(2*math.Pi*float64(120+k*90)*float64(i)/SampleRate))
		}
		s := embed(f, pcm)
		for e := Context - 1; e < len(s)/EmbeddingDim; e += 3 {
			n.Windows = append(n.Windows, window(s, e))
		}
		n.Streams = append(n.Streams, s)
	}
	return n
}

func TestTrainWordLearnsAWord(t *testing.T) {
	if testing.Short() {
		t.Skip("trains a model")
	}
	r := rand.New(rand.NewPCG(7, 7))
	en := &Enrollment{Ambient: noise(r, 20, 150), Speech: noise(r, 10, 600)}
	for range 8 {
		en.Takes = append(en.Takes, fakeWord(r, 6000+2000*r.Float64()))
	}
	var stages []string
	m, err := TrainWord(context.Background(), en, fakeNegatives(t, r), TrainOptions{
		Word: "test", Seed: 1, Epochs: 12, Now: func() time.Time { return time.Unix(0, 0) },
		Progress: func(stage string, _ float64) {
			if len(stages) == 0 || stages[len(stages)-1] != stage {
				stages = append(stages, stage)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(stages); got != 3 {
		t.Fatalf("stages %v", stages)
	}
	t.Logf("threshold %.2f recall %.2f false/h %.2f over %.3f h", m.Threshold, m.Recall, m.FalsePerHour, m.EvalHours)
	b, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	m, err = ParseModel(b)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDetector(m, DefaultSensitivity)
	if err != nil {
		t.Fatal(err)
	}
	hits := 0
	// a new take in the room: one detection
	stream := append(noise(r, 2, 150), fakeWord(r, 7000)...)
	stream = append(stream, noise(r, 2, 150)...)
	d.Push(stream, func(float32) { hits++ })
	if hits != 1 {
		t.Fatalf("%d detections on the word, want 1", hits)
	}
	hits = 0
	d.Reset()
	d.Push(noise(r, 20, 600), func(float32) { hits++ })
	if hits != 0 {
		t.Fatalf("%d detections on noise", hits)
	}
}

func TestTrainWordRefuses(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 1))
	neg := &Negatives{Windows: [][]float32{make([]float32, Context*EmbeddingDim)}}
	cases := map[string]struct {
		en  *Enrollment
		neg *Negatives
	}{
		"no_negatives":  {&Enrollment{Ambient: noise(r, 2, 100)}, nil},
		"no_ambient":    {&Enrollment{}, neg},
		"too_few_takes": {&Enrollment{Ambient: noise(r, 2, 100), Takes: [][]int16{noise(r, 1, 50)}}, neg},
	}
	for code, c := range cases {
		_, err := TrainWord(context.Background(), c.en, c.neg, TrainOptions{})
		var te *TrainError
		if !errors.As(err, &te) || te.Code != code {
			t.Errorf("%s: got %v", code, err)
		}
	}
}

func TestReadDataset(t *testing.T) {
	r := rand.New(rand.NewPCG(2, 2))
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	put := func(name string, b []byte) {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(b)
	}
	put("word/takes/1.wav", WAV16(fakeWord(r, 5000)))
	put("word/takes/2.WAV", WAV16(fakeWord(r, 5000)))
	put("word/ambient.wav", WAV16(noise(r, 2, 100)))
	put("word/speech/b.wav", WAV16(noise(r, 1, 100)))
	put("word/speech/a.wav", WAV16(noise(r, 1, 100)))
	put("word/readme.txt", []byte("ignored"))
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	en, err := ReadDataset(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(en.Takes) != 2 || len(en.Ambient) != 2*SampleRate || len(en.Speech) != 2*SampleRate {
		t.Fatalf("takes %d ambient %d speech %d", len(en.Takes), len(en.Ambient), len(en.Speech))
	}
	_, err = ReadDataset([]byte("not a zip"))
	var de *DatasetError
	if !errors.As(err, &de) || de.Code != "not_zip" {
		t.Fatalf("got %v", err)
	}
}

func TestReadWAVResamplesAndMixes(t *testing.T) {
	// 48 kHz stereo 16-bit, a 1 kHz tone in both channels
	const rate = 48000
	n := rate / 2
	b := WAV16(make([]int16, 2*n))
	put := func(off int, v uint32) {
		b[off], b[off+1], b[off+2], b[off+3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
	}
	b[22] = 2
	put(24, rate)
	put(28, rate*4)
	b[32] = 4
	for i := range n {
		v := clip16(8000 * math.Sin(2*math.Pi*1000*float64(i)/rate))
		for c := range 2 {
			o := 44 + 4*i + 2*c
			b[o], b[o+1] = byte(v), byte(uint16(v)>>8)
		}
	}
	pcm, err := ReadWAV(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(pcm) != SampleRate/2 {
		t.Fatalf("%d samples", len(pcm))
	}
	var peak int16
	for _, s := range pcm[100 : len(pcm)-100] {
		peak = max(peak, s)
	}
	if peak < 7500 || peak > 8100 {
		t.Fatalf("peak %d", peak)
	}
}

func TestNegativesRoundTrip(t *testing.T) {
	n := &Negatives{Source: "x", Windows: [][]float32{make([]float32, Context*EmbeddingDim)},
		Streams: [][]float32{make([]float32, 3*EmbeddingDim)}}
	n.Windows[0][5] = 1.5
	n.Streams[0][7] = -0.25
	var buf bytes.Buffer
	if err := WriteNegatives(&buf, n); err != nil {
		t.Fatal(err)
	}
	got, err := ReadNegatives(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "x" || got.Windows[0][5] != 1.5 || got.Streams[0][7] != -0.25 || len(got.Streams[0]) != 3*EmbeddingDim {
		t.Fatalf("round trip lost values")
	}
	if _, err := ReadNegatives(buf.Bytes()[:buf.Len()-1]); err == nil {
		t.Fatal("a truncated file passed")
	}
}

func TestHalf(t *testing.T) {
	for _, v := range []float32{0, 1, -2.5, 65504, 6.1035156e-05, 5.9604645e-08, 0.333251953125} {
		if got := FromHalf(ToHalf(v)); got != v {
			t.Errorf("%g -> %g", v, got)
		}
	}
	if FromHalf(ToHalf(1e6)) != float32(math.Inf(1)) {
		t.Error("no saturation")
	}
}
