package kws

import (
	_ "embed"
	"sync"
)

//go:embed assets/features.bin
var featuresBin []byte

var (
	weightsOnce sync.Once
	weights     map[string]tensor
	weightsErr  error
)

func loadWeights() (map[string]tensor, error) {
	weightsOnce.Do(func() { weights, weightsErr = readTensors(featuresBin) })
	return weights, weightsErr
}

// Features turns a stream of 16 kHz mono samples (int16 scale) into one embedding per 80 ms step, each of the last 76
// mel frames, from the stream's 10th step on. Not safe for concurrent use: one per stream.
type Features struct {
	mel     *melFront
	net     *embedNet
	samples []float32 // ctxSamples of history, then the step being filled
	frames  []float32
}

func NewFeatures() (*Features, error) {
	t, err := loadWeights()
	if err != nil {
		return nil, err
	}
	window, err := lookup(t, "mel.window", fftSize)
	if err != nil {
		return nil, err
	}
	filters, err := lookup(t, "mel.filters", fftBins, MelBins)
	if err != nil {
		return nil, err
	}
	net, err := newEmbedNet(t)
	if err != nil {
		return nil, err
	}
	f := &Features{mel: newMelFront(window, filters), net: net,
		samples: make([]float32, ctxSamples, callSamples), frames: make([]float32, stepFrames*MelBins)}
	return f, nil
}

// Reset forgets the stream: the next sample starts a new one.
func (f *Features) Reset() {
	f.samples = f.samples[:ctxSamples]
	clear(f.samples)
	f.net.reset()
}

// Push feeds samples and hands emit each embedding they complete; the slice is reused, so emit copies what it keeps.
func (f *Features) Push(pcm []int16, emit func(embedding []float32)) {
	for _, s := range pcm {
		f.samples = append(f.samples, float32(s))
		if len(f.samples) < callSamples {
			continue
		}
		f.mel.step(f.samples, f.frames)
		copy(f.samples, f.samples[StepSamples:])
		f.samples = f.samples[:ctxSamples]
		f.net.push(f.frames, stepFrames, emit)
	}
}
