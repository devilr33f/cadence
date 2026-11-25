package midi_test

import (
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/leds"
)

// the ring alone: no button lights, no clock, no light sensor
func TestLightsAreTheRingAlone(t *testing.T) {
	tr := midiTree(t)
	b, _ := newBoard(t, tr)
	s, ok := b.Lights()
	if !ok || s == nil {
		t.Fatal("no lights")
	}
	if g := s.Geometry(); g != (leds.Geometry{Ring: 89}) {
		t.Fatalf("geometry = %+v", g)
	}
	if _, ok := s.(leds.LightSensor); ok {
		t.Fatal("a light sensor")
	}
	// nothing ever draws these, and they touch nothing
	if err := s.Button(make([]leds.RGB, 6)); err != nil {
		t.Fatal(err)
	}
	if err := s.Segments(map[leds.Segment]uint8{leds.Colon: 255}); err != nil {
		t.Fatal(err)
	}
	if got := tr.read("dev/fb0"); got != "" {
		t.Fatalf("fb0 = %q", got)
	}
}
