package kws

import (
	"math"
)

// The front end, as openWakeWord's melspectrogram model computes it: 16 kHz samples on the int16 scale, a 512-point
// STFT every 160 samples with the model's 400-point periodic Hann window centred in the frame and no padding, the
// power spectrum through a 257x32 mel filterbank, 10*log10 floored at 1e-10, every value of one call raised to at least
// the call's maximum less 80 dB, then x/10 + 2 (openWakeWord's own scaling). openWakeWord calls it once per 80 ms step
// on the step's 1280 samples and the 480 before them, which gives the step's 8 frames.
const (
	SampleRate = 16000
	// StepSamples is one streaming step, 80 ms: 8 mel frames, and at most one embedding
	StepSamples = 1280
	MelBins     = 32

	stepFrames  = 8
	fftSize     = 512
	fftBins     = fftSize/2 + 1
	hopSamples  = 160
	ctxSamples  = 480 // the samples before a step its first frames reach back to
	callSamples = ctxSamples + StepSamples
	topDB       = 80
)

type melFront struct {
	window  []float32 // fftSize
	filters []float32 // fftBins x MelBins
	// the fft's tables and scratch
	rev    [fftSize]uint16
	cos    [fftSize / 2]float64
	sin    [fftSize / 2]float64
	re, im [fftSize]float64
	power  [fftBins]float32
}

func newMelFront(window, filters []float32) *melFront {
	m := &melFront{window: window, filters: filters}
	bits := 0
	for 1<<bits < fftSize {
		bits++
	}
	for i := range m.rev {
		r := 0
		for b := range bits {
			r |= (i >> b & 1) << (bits - 1 - b)
		}
		m.rev[i] = uint16(r)
	}
	for i := range m.cos {
		a := -2 * math.Pi * float64(i) / fftSize
		m.cos[i], m.sin[i] = math.Cos(a), math.Sin(a)
	}
	return m
}

// step writes the stepFrames log-mel frames of one call (callSamples samples) to out (stepFrames x MelBins).
func (m *melFront) step(samples []float32, out []float32) {
	top := float32(math.Inf(-1))
	for f := range stepFrames {
		frame := samples[f*hopSamples : f*hopSamples+fftSize]
		m.spectrum(frame)
		row := out[f*MelBins : (f+1)*MelBins]
		for b := range row {
			row[b] = 0
		}
		for k, p := range m.power {
			if p == 0 {
				continue
			}
			w := m.filters[k*MelBins : (k+1)*MelBins]
			for b := range row {
				row[b] += p * w[b]
			}
		}
		for b, v := range row {
			db := float32(10 * math.Log10(float64(max(v, 1e-10))))
			row[b] = db
			top = max(top, db)
		}
	}
	floor := top - topDB
	for i, v := range out[:stepFrames*MelBins] {
		out[i] = max(v, floor)/10 + 2
	}
}

// spectrum is the power spectrum of one windowed frame: an in-place radix-2 fft.
func (m *melFront) spectrum(frame []float32) {
	for i := range fftSize {
		j := m.rev[i]
		m.re[j] = float64(frame[i] * m.window[i])
		m.im[j] = 0
	}
	for size := 2; size <= fftSize; size <<= 1 {
		half, stride := size/2, fftSize/size
		for start := 0; start < fftSize; start += size {
			for k := range half {
				c, s := m.cos[k*stride], m.sin[k*stride]
				a, b := start+k, start+k+half
				tr := m.re[b]*c - m.im[b]*s
				ti := m.re[b]*s + m.im[b]*c
				m.re[b], m.im[b] = m.re[a]-tr, m.im[a]-ti
				m.re[a] += tr
				m.im[a] += ti
			}
		}
	}
	for k := range m.power {
		m.power[k] = float32(m.re[k]*m.re[k] + m.im[k]*m.im[k])
	}
}
