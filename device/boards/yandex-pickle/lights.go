package pickle

import (
	"os"
	"sync"

	"git.shino.boo/devilreef/cadence/device/internal/leds"
)

const (
	ringLEDs   = 16
	buttonLEDs = 6
	digits     = 4
	// a..g and the decimal point
	segments = 8
	// a raw light reading at or above this is a lit room
	lightFull   = 4
	bytesPerLED = 3

	// the led that shows ring index 0: physical 0 is at the back and physical indices run clockwise seen from above,
	// while ring index 0 is the owner's left facing the clock and grows across the front, so a low volume fills the
	// part in view
	ringOffset = 12
)

// lightSink drives the ring (/dev/fb1) and the action button (/dev/fb0), framebuffers of 3 bytes per led in b, g, r
// order that show a whole-frame write at once, the clock's sysfs leds, and the light sensor. A framebuffer stays
// open; a failed write closes it, so the next frame opens it again.
type lightSink struct {
	root string

	mu     sync.Mutex
	ring   *os.File
	button *os.File
	// what each clock led holds; nil until a whole frame reached them
	clock  map[string]uint8
	sensor string
}

func (*lightSink) Geometry() leds.Geometry {
	return leds.Geometry{Ring: ringLEDs, Button: buttonLEDs, Digits: digits, Segments: segments, LightFull: lightFull}
}

// Ring maps logical index i to physical (12 - i) mod 16: counter-clockwise from the owner's left.
func (s *lightSink) Ring(px []leds.RGB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.framebuffer(&s.ring, "/dev/fb1", "ring", bgr(px, ringLEDs, func(i int) int {
		return ((ringOffset-i)%ringLEDs + ringLEDs) % ringLEDs
	}))
}

func (s *lightSink) Button(px []leds.RGB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.framebuffer(&s.button, "/dev/fb0", "button", bgr(px, buttonLEDs, func(i int) int { return i }))
}

func bgr(px []leds.RGB, n int, physical func(int) int) []byte {
	out := make([]byte, n*bytesPerLED)
	for i, p := range px[:min(len(px), n)] {
		at := physical(i) * bytesPerLED
		out[at], out[at+1], out[at+2] = p.B, p.G, p.R
	}
	return out
}

// callers hold mu
func (s *lightSink) framebuffer(f **os.File, node, part string, frame []byte) error {
	if *f == nil {
		opened, err := os.OpenFile(under(s.root, node), os.O_WRONLY, 0)
		if err != nil {
			return &leds.SinkError{Reason: part + "_open_failed"}
		}
		*f = opened
	}
	if n, err := (*f).WriteAt(frame, 0); err != nil || n != len(frame) {
		_ = (*f).Close()
		*f = nil
		return &leds.SinkError{Reason: part + "_write_failed"}
	}
	return nil
}

// close lets the framebuffers go; a frame after it opens them again
func (s *lightSink) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range []**os.File{&s.ring, &s.button} {
		if *f != nil {
			_ = (*f).Close()
			*f = nil
		}
	}
}
