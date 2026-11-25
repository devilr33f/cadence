package midi

import (
	"bytes"
	"errors"
	"path/filepath"
	"syscall"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/leds"
)

// write is what one pwrite of the fake does: fail with err, or take at most n bytes (0: all of them, -1: none)
type write struct {
	n   int
	err error
}

// fakeFB is a ring node with the geometry it is given, holding what reaches it
type fakeFB struct {
	geo    fbGeometry
	geoErr error
	// the writes in turn; past the list a write takes everything
	writes []write
	data   []byte
	calls  int
	closed bool
}

func (f *fakeFB) geometry() (fbGeometry, error) { return f.geo, f.geoErr }

func (f *fakeFB) pwrite(p []byte, off int64) (int, error) {
	if f.closed {
		return 0, syscall.EBADF
	}
	w := write{}
	if f.calls < len(f.writes) {
		w = f.writes[f.calls]
	}
	f.calls++
	switch {
	case w.err != nil:
		return -1, w.err
	case w.n < 0:
		return 0, nil
	}
	n := len(p)
	if w.n > 0 {
		n = min(n, w.n)
	}
	if end := int(off) + n; len(f.data) < end {
		f.data = append(f.data, make([]byte, end-len(f.data))...)
	}
	copy(f.data[off:], p[:n])
	return n, nil
}

func (f *fakeFB) close() error {
	f.closed = true
	return nil
}

var midiGeometry = fbGeometry{xres: 89, yres: 1, bitsPerPixel: 32, lineLength: 356}

// opener hands out the framebuffers in turn, recording the paths it was asked for
type opener struct {
	fbs   []*fakeFB
	paths []string
	err   error
}

func (o *opener) open(path string) (framebuffer, error) {
	o.paths = append(o.paths, path)
	if o.err != nil {
		return nil, o.err
	}
	if len(o.paths) > len(o.fbs) {
		return nil, syscall.ENOENT
	}
	return o.fbs[len(o.paths)-1], nil
}

func newSink(root string, fbs ...*fakeFB) (*lightSink, *opener) {
	o := &opener{fbs: fbs}
	return &lightSink{root: root, open: o.open}, o
}

func pixels(n int) []leds.RGB {
	px := make([]leds.RGB, n)
	for i := range px {
		px[i] = leds.RGB{R: uint8(i), G: uint8(100 + i), B: uint8(200 - i)}
	}
	return px
}

// wantFrame is the frame the driver wants for px: pixel i at bytes 4i..4i+3 as r, g, b, 0, the rest dark
func wantFrame(px []leds.RGB) []byte {
	out := make([]byte, 356)
	for i, p := range px {
		out[4*i], out[4*i+1], out[4*i+2] = p.R, p.G, p.B
	}
	return out
}

func reasonOf(err error) string {
	var se *leds.SinkError
	if errors.As(err, &se) {
		return se.Reason
	}
	return ""
}

func TestRingFrameInPixelOrderAndRGBX(t *testing.T) {
	root := t.TempDir()
	fb := &fakeFB{geo: midiGeometry}
	s, o := newSink(root, fb)
	if len(o.paths) != 0 {
		t.Fatal("opened before the first frame")
	}
	if err := s.Ring(pixels(89)); err != nil {
		t.Fatal(err)
	}
	want := wantFrame(pixels(89))
	if !bytes.Equal(fb.data, want) {
		t.Fatalf("fb0 = % x\nwant % x", fb.data, want)
	}
	if want[0] != 0 || want[1] != 100 || want[2] != 200 || want[3] != 0 || want[352] != 88 || want[355] != 0 {
		t.Fatal("the expected frame itself is off")
	}
	// a whole frame each time, from the start of the node, on the node already open
	if err := s.Ring(pixels(3)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fb.data, wantFrame(pixels(3))) {
		t.Fatalf("fb0 after a short frame = % x", fb.data)
	}
	if err := s.Ring(make([]leds.RGB, 89)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fb.data, make([]byte, 356)) || len(o.paths) != 1 || o.paths[0] != filepath.Join(root, "dev", "fb0") {
		t.Fatalf("fb0 after a blank = % x, opened %q", fb.data, o.paths)
	}
	// more pixels than the ring has are dropped
	if err := s.Ring(pixels(100)); err != nil || !bytes.Equal(fb.data, wantFrame(pixels(89))) {
		t.Fatalf("a longer frame: %v, fb0 = % x", err, fb.data)
	}
	s.close()
	if !fb.closed {
		t.Fatal("the node stays open after close")
	}
}

// another firmware's fb0: refused before anything is written, the node closed; every frame checks afresh
func TestRingGeometryMismatchRefusedBeforeAnyWrite(t *testing.T) {
	for _, c := range []struct {
		name string
		geo  fbGeometry
		err  error
	}{
		{"a shorter ring", fbGeometry{xres: 88, yres: 1, bitsPerPixel: 32, lineLength: 356}, nil},
		{"two rows", fbGeometry{xres: 89, yres: 2, bitsPerPixel: 32, lineLength: 356}, nil},
		{"three bytes a pixel", fbGeometry{xres: 89, yres: 1, bitsPerPixel: 24, lineLength: 356}, nil},
		{"another line length", fbGeometry{xres: 89, yres: 1, bitsPerPixel: 32, lineLength: 352}, nil},
		{"no geometry", midiGeometry, syscall.ENOTTY},
	} {
		t.Run(c.name, func(t *testing.T) {
			first, second := &fakeFB{geo: c.geo, geoErr: c.err}, &fakeFB{geo: c.geo, geoErr: c.err}
			s, o := newSink(t.TempDir(), first, second)
			for _, fb := range []*fakeFB{first, second} {
				if r := reasonOf(s.Ring(pixels(89))); r != "framebuffer_geometry_invalid" {
					t.Fatalf("reason = %q", r)
				}
				if fb.calls != 0 || fb.data != nil || !fb.closed {
					t.Fatalf("writes %d, data % x, closed %v", fb.calls, fb.data, fb.closed)
				}
			}
			if len(o.paths) != 2 {
				t.Fatalf("opened %d times", len(o.paths))
			}
		})
	}
}

// a write the kernel interrupts is tried again, up to 8 times in a row; a short one goes on where it stopped
func TestRingWriteLoop(t *testing.T) {
	eintr := write{err: syscall.EINTR}
	fb := &fakeFB{geo: midiGeometry, writes: []write{
		{n: 19}, eintr, eintr, eintr, eintr, eintr, eintr, eintr, eintr, {n: 100}, eintr, {n: 1}, eintr, eintr, eintr, eintr,
		eintr, eintr, eintr, eintr,
	}}
	s, _ := newSink(t.TempDir(), fb)
	if err := s.Ring(pixels(89)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fb.data, wantFrame(pixels(89))) || fb.closed {
		t.Fatalf("fb0 = % x, closed %v", fb.data, fb.closed)
	}
	if fb.calls != 21 {
		t.Fatalf("writes = %d", fb.calls)
	}
}

func TestRingWriteFailures(t *testing.T) {
	eintr := write{err: syscall.EINTR}
	for _, c := range []struct {
		name   string
		writes []write
	}{
		{"nine interruptions in a row", []write{{n: 7}, eintr, eintr, eintr, eintr, eintr, eintr, eintr, eintr, eintr}},
		{"an error", []write{{n: 7}, {err: syscall.EIO}}},
		// never a spin on a node that takes nothing
		{"nothing written", []write{{n: 7}, {n: -1}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			failing, next := &fakeFB{geo: midiGeometry, writes: c.writes}, &fakeFB{geo: midiGeometry}
			s, _ := newSink(t.TempDir(), failing, next)
			if r := reasonOf(s.Ring(pixels(89))); r != "framebuffer_write_failed" {
				t.Fatalf("reason = %q", r)
			}
			if !failing.closed {
				t.Fatal("the node stays open")
			}
			// the next frame opens the node again
			if err := s.Ring(pixels(89)); err != nil || !bytes.Equal(next.data, wantFrame(pixels(89))) {
				t.Fatalf("after the failure: %v, fb0 = % x", err, next.data)
			}
		})
	}
}

func TestRingOpenFailure(t *testing.T) {
	s, o := newSink(t.TempDir())
	o.err = syscall.ENOENT
	if r := reasonOf(s.Ring(pixels(89))); r != "framebuffer_open_failed" {
		t.Fatalf("reason = %q", r)
	}
	// and again at the next frame
	fb := &fakeFB{geo: midiGeometry}
	o.err, o.fbs, o.paths = nil, []*fakeFB{fb}, nil
	if err := s.Ring(pixels(89)); err != nil || len(fb.data) != 356 {
		t.Fatalf("after the node came: %v", err)
	}
}
