package vkcapsula

import (
	"os"
	"strconv"
	"sync"

	"git.shino.boo/devilreef/cadence/device/internal/leds"
)

const (
	ringLEDs = 12
	digits   = 4
	segments = 7
	// 2300 lux is where the stock curve trunc(ln(0.75+0.05*lux)*19.9375) reaches 95 %
	lightFull = 2300
)

// lightSink drives the ring, the clock and the light sensor. The capsula has no action button light.
type lightSink struct {
	root string

	mu     sync.Mutex
	ring   ringWriter
	clock  map[int]uint8
	sensor string
}

func newLights(root string) *lightSink { return &lightSink{root: root} }

func (*lightSink) Geometry() leds.Geometry {
	return leds.Geometry{Ring: ringLEDs, Digits: digits, Segments: segments, LightFull: lightFull}
}

func (*lightSink) Button([]leds.RGB) error { return nil }

func (s *lightSink) Ring(px []leds.RGB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ring.write(s.root, ringChannels(px))
}

// writeLevel writes a sysfs led's brightness
func writeLevel(file string, v uint8) bool {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_TRUNC, 0) //nolint:gosec // sysfs, under the board's root
	if err != nil {
		return false
	}
	_, err = f.WriteString(strconv.Itoa(int(v)) + "\n")
	return f.Close() == nil && err == nil
}
