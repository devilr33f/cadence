package main

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// dbfsFloor stands in for a tone that is absent (log of zero)
const dbfsFloor = -120.0

// windowLevel is one tone's peak level in each channel over one window, in
// dBFS: a full-scale sine reads 0.
type windowLevel struct {
	startMs int
	dbfs    []float64
}

// toneLevels runs a Hann-windowed Goertzel filter at hz over each whole window
// of windowMs; a partial last window is left out.
func toneLevels(p pcm, hz float64, windowMs int) ([]windowLevel, error) {
	if p.rate <= 0 || p.channels <= 0 {
		return nil, errors.New("no audio format")
	}
	if hz <= 0 || hz >= float64(p.rate)/2 {
		return nil, errors.New("tone must be between 0 Hz and half the sample rate")
	}
	n := p.rate * windowMs / 1000
	if n < 2 {
		return nil, errors.New("window too short")
	}
	hann := make([]float64, n)
	var gain float64
	for i := range hann {
		hann[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n))
		gain += hann[i]
	}
	coeff := 2 * math.Cos(2*math.Pi*hz/float64(p.rate))
	frames := len(p.samples) / p.channels
	var out []windowLevel
	for start := 0; start+n <= frames; start += n {
		w := windowLevel{startMs: start * 1000 / p.rate, dbfs: make([]float64, p.channels)}
		for ch := range p.channels {
			var s1, s2 float64
			for i := range n {
				x := hann[i] * float64(p.samples[(start+i)*p.channels+ch]) / 32768
				s1, s2 = x+coeff*s1-s2, s1
			}
			// a sine of amplitude a gives |X| = a·gain/2
			amp := 2 * math.Sqrt(max(0, s1*s1+s2*s2-coeff*s1*s2)) / gain
			w.dbfs[ch] = dbfsFloor
			if amp > 0 {
				w.dbfs[ch] = max(dbfsFloor, 20*math.Log10(amp))
			}
		}
		out = append(out, w)
	}
	return out, nil
}

func formatLevel(w windowLevel) string {
	var b strings.Builder
	fmt.Fprintf(&b, "ms=%d", w.startMs)
	for ch, db := range w.dbfs {
		fmt.Fprintf(&b, " ch%d=%.1f", ch, db)
	}
	return b.String()
}
