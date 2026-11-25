package leds

import "math"

// clockFull is the segment level of a lit segment at brightness 1
const clockFull = 200

const gamma = 2.2

// painter turns colours and 0..1 intensities into led values. the leds are linear while colours and intensities are
// perceptual, so ring and button values are gamma corrected; room brightness then scales the light linearly. clock
// segments are white and stay linear.
type painter struct{ brightness float64 }

func newPainter(brightness float64) painter { return painter{clamp01(brightness)} }

func (p painter) colour(base RGB, intensity float64) RGB {
	l := clamp01(intensity)
	return RGB{p.light(base.R, l), p.light(base.G, l), p.light(base.B, l)}
}

func (p painter) segment(intensity float64) uint8 {
	return uint8(math.Round(clockFull * clamp01(intensity) * p.brightness))
}

func (p painter) light(channel uint8, level float64) uint8 {
	return uint8(math.Round(255 * math.Pow(float64(channel)/255*level, gamma) * p.brightness))
}
