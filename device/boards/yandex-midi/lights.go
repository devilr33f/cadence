package midi

import (
	"errors"
	"sync"
	"syscall"

	"git.shino.boo/devilreef/cadence/device/internal/leds"
)

const (
	ringNode    = "/dev/fb0"
	ringLEDs    = 89
	bytesPerLED = 4
	// a whole frame, the driver's line
	frameBytes = ringLEDs * bytesPerLED
	// interruptions in a row a frame write survives
	maxInterrupted = 8
)

// the geometry the driver must report before anything is written: another firmware's fb0 could be laid out
// otherwise, and a frame for this one would draw garbage on it
var ringGeometry = fbGeometry{xres: ringLEDs, yres: 1, bitsPerPixel: 8 * bytesPerLED, lineLength: frameBytes}

// fbGeometry is what the framebuffer driver says of its node
type fbGeometry struct{ xres, yres, bitsPerPixel, lineLength uint32 }

// framebuffer is an open framebuffer node: its geometry, and positional writes that may be short or interrupted
type framebuffer interface {
	geometry() (fbGeometry, error)
	pwrite(p []byte, off int64) (int, error)
	close() error
}

// lightSink drives the ring, /dev/fb0: one row of 4 bytes per led in r, g, b, 0 order, which shows a whole-frame
// write at once. Ring index i is pixel i (the physical orientation is unverified). The node opens at the first frame
// and stays open; a failure closes it, so the next frame opens it again and checks its geometry afresh. There is no
// button light, no clock and no light sensor.
type lightSink struct {
	root string
	open func(path string) (framebuffer, error)

	mu sync.Mutex
	fb framebuffer
}

func (*lightSink) Geometry() leds.Geometry { return leds.Geometry{Ring: ringLEDs} }

func (s *lightSink) Ring(px []leds.RGB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fb == nil {
		fb, err := s.open(under(s.root, ringNode))
		if err != nil {
			return &leds.SinkError{Reason: "framebuffer_open_failed"}
		}
		if g, err := fb.geometry(); err != nil || g != ringGeometry {
			_ = fb.close()
			return &leds.SinkError{Reason: "framebuffer_geometry_invalid"}
		}
		s.fb = fb
	}
	if !writeFrame(s.fb, rgbx(px)) {
		_ = s.fb.close()
		s.fb = nil
		return &leds.SinkError{Reason: "framebuffer_write_failed"}
	}
	return nil
}

func rgbx(px []leds.RGB) []byte {
	out := make([]byte, frameBytes)
	for i, p := range px[:min(len(px), ringLEDs)] {
		at := i * bytesPerLED
		out[at], out[at+1], out[at+2] = p.R, p.G, p.B
	}
	return out
}

// writeFrame writes all of frame from the node's start: a short write goes on where it stopped, an interrupted one
// is tried again unless it is the ninth in a row, and one that writes nothing fails rather than spins
func writeFrame(fb framebuffer, frame []byte) bool {
	interrupted := 0
	for off := 0; off < len(frame); {
		n, err := fb.pwrite(frame[off:], int64(off))
		if errors.Is(err, syscall.EINTR) && interrupted < maxInterrupted {
			interrupted++
			continue
		}
		if err != nil || n <= 0 {
			return false
		}
		interrupted = 0
		off += n
	}
	return true
}

// Button never runs: the geometry has no button lights.
func (*lightSink) Button([]leds.RGB) error { return nil }

// Segments never runs: the geometry has no clock.
func (*lightSink) Segments(map[leds.Segment]uint8) error { return nil }

// close lets the framebuffer go; a frame after it opens it again
func (s *lightSink) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fb != nil {
		_ = s.fb.close()
		s.fb = nil
	}
}
