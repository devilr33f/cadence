// Package leds renders the device state on a board's lights. Everything here is board-neutral: geometry and byte
// order come from the board's Sink.
package leds

import "math"

type RGB struct{ R, G, B uint8 }

// Segment names one clock segment: Digit 0..3 with Index 0..6 = a..g, or Colon and Degree.
type Segment struct{ Digit, Index int }

var (
	// both colon halves together
	Colon = Segment{Digit: -1, Index: 0}
	// the dot that says an alarm is set
	Degree = Segment{Digit: -1, Index: 1}
)

// Geometry says which lights a board has; zero means the part is absent.
type Geometry struct {
	Ring   int
	Button int
	// 0 (no clock) or 4 (HHMM)
	Digits int
	// segments per digit, at least 7 (a..g) when Digits > 0
	Segments int
	// raw light sensor reading of a lit room, for the brightness curve; 0 when there is no sensor scale
	LightFull int
}

func (g Geometry) valid() bool {
	if g.Ring < 0 || g.Button < 0 || g.LightFull < 0 {
		return false
	}
	return g.Digits == 0 || g.Digits == 4 && g.Segments >= 7
}

func (g Geometry) empty() bool { return g.Ring == 0 && g.Button == 0 && g.Digits == 0 }

// Sink writes frames to a board's lights. Ring and Button take logical order, 8-bit RGB already gamma corrected for
// the linear leds: the sink maps order, orientation and byte order. Segments takes every segment of the clock at its
// 0..255 level (a segment the clock does not use is 0). A sink must not keep its argument and may be slow: the
// renderer never holds a lock across a call.
type Sink interface {
	Ring(px []RGB) error
	Button(px []RGB) error
	Segments(level map[Segment]uint8) error
	Geometry() Geometry
}

// LightSensor is optionally implemented by a sink with an ambient light sensor.
type LightSensor interface {
	// Light is the raw reading; false when it cannot be read
	Light() (int, bool)
}

// SinkError carries a code that never holds values.
type SinkError struct{ Reason string }

func (e *SinkError) Error() string { return "leds sink: " + e.Reason }

// Frame is one picture on every light. Ring index 0 is where the volume fill starts, and indices grow in the
// direction it fills.
type Frame struct {
	Ring   []RGB
	Button []RGB
	// Digits[d][s]: segment s of digit d
	Digits [][]uint8
	Colon  uint8
	Degree uint8
}

func newFrame(g Geometry) Frame {
	f := Frame{Ring: make([]RGB, g.Ring), Button: make([]RGB, g.Button), Digits: make([][]uint8, g.Digits)}
	for i := range f.Digits {
		f.Digits[i] = make([]uint8, g.Segments)
	}
	return f
}

func geometryOf(f Frame) Geometry {
	g := Geometry{Ring: len(f.Ring), Button: len(f.Button), Digits: len(f.Digits)}
	if len(f.Digits) > 0 {
		g.Segments = len(f.Digits[0])
	}
	return g
}

func towards(from, to uint8, amount float64) uint8 {
	return uint8(math.Round(float64(from) + (float64(to)-float64(from))*amount))
}

func towardsRGB(from, to RGB, a float64) RGB {
	return RGB{towards(from.R, to.R, a), towards(from.G, to.G, a), towards(from.B, to.B, a)}
}

// Blend moves every channel from `from` towards `to` by amount, 0..1. Both frames share one geometry.
func Blend(from, to Frame, amount float64) Frame {
	a := clamp01(amount)
	out := newFrame(geometryOf(from))
	for i := range out.Ring {
		out.Ring[i] = towardsRGB(from.Ring[i], to.Ring[i], a)
	}
	for i := range out.Button {
		out.Button[i] = towardsRGB(from.Button[i], to.Button[i], a)
	}
	for d := range out.Digits {
		for s := range out.Digits[d] {
			out.Digits[d][s] = towards(from.Digits[d][s], to.Digits[d][s], a)
		}
	}
	out.Colon = towards(from.Colon, to.Colon, a)
	out.Degree = towards(from.Degree, to.Degree, a)
	return out
}

func clamp01(v float64) float64 { return math.Min(math.Max(v, 0), 1) }
