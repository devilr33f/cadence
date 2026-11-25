package pickle_test

import (
	"bytes"
	"errors"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/leds"
)

func sink(t *testing.T, tr *tree) leds.Sink {
	t.Helper()
	b, _ := newBoard(t, tr)
	s, ok := b.Lights()
	if !ok || s == nil {
		t.Fatal("no lights")
	}
	return s
}

func TestGeometry(t *testing.T) {
	if g := sink(t, pickleTree(t)).Geometry(); g != (leds.Geometry{Ring: 16, Button: 6, Digits: 4, Segments: 8, LightFull: 4}) {
		t.Fatalf("geometry = %+v", g)
	}
}

func pixels(n int) []leds.RGB {
	px := make([]leds.RGB, n)
	for i := range px {
		px[i] = leds.RGB{R: uint8(10 + i), G: uint8(100 + i), B: uint8(200 + i)}
	}
	return px
}

func TestRingFrameInPhysicalOrderAndBGR(t *testing.T) {
	tr := pickleTree(t)
	s := sink(t, tr)
	if err := s.Ring(pixels(16)); err != nil {
		t.Fatal(err)
	}
	// logical 0 is the owner's left facing the clock, at physical 12, and the fill runs counter-clockwise from there
	want := make([]byte, 48)
	for i := range 16 {
		at := ((12-i)%16 + 16) % 16 * 3
		want[at], want[at+1], want[at+2] = uint8(200+i), uint8(100+i), uint8(10+i)
	}
	if got := tr.read("dev/fb1"); !bytes.Equal([]byte(got), want) {
		t.Fatalf("fb1 = % x\nwant % x", got, want)
	}
	if want[36] != 200 || want[33] != 201 || want[39] != 215 {
		t.Fatal("the expected mapping itself is off")
	}
	// a whole frame each time, from the start of the node
	if err := s.Ring(make([]leds.RGB, 16)); err != nil {
		t.Fatal(err)
	}
	if got := tr.read("dev/fb1"); !bytes.Equal([]byte(got), make([]byte, 48)) {
		t.Fatalf("fb1 after a blank = % x", got)
	}
}

func TestButtonFrameInOrderAndBGR(t *testing.T) {
	tr := pickleTree(t)
	if err := sink(t, tr).Button(pixels(6)); err != nil {
		t.Fatal(err)
	}
	want := []byte{200, 100, 10, 201, 101, 11, 202, 102, 12, 203, 103, 13, 204, 104, 14, 205, 105, 15}
	if got := tr.read("dev/fb0"); !bytes.Equal([]byte(got), want) {
		t.Fatalf("fb0 = % x", got)
	}
}

func reason(err error) string {
	var se *leds.SinkError
	if errors.As(err, &se) {
		return se.Reason
	}
	return ""
}

func TestFramebufferFailures(t *testing.T) {
	tr := pickleTree(t)
	s := sink(t, tr)
	tr.remove("dev/fb1")
	if r := reason(s.Ring(pixels(16))); r != "ring_open_failed" {
		t.Fatalf("ring without its node: %q", r)
	}
	tr.remove("dev/fb0")
	if r := reason(s.Button(pixels(6))); r != "button_open_failed" {
		t.Fatalf("button without its node: %q", r)
	}
	// the next frame opens the node again
	tr.write("dev/fb1", "")
	if err := s.Ring(pixels(16)); err != nil || len(tr.read("dev/fb1")) != 48 {
		t.Fatalf("ring after its node came back: %v", err)
	}
}
