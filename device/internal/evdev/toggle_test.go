package evdev

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/input"
)

const toggleCode = 248

func rawEvent(typ, code uint16, value int32) []byte {
	b := make([]byte, eventSize)
	at := eventSize - 8
	binary.LittleEndian.PutUint16(b[at:], typ)
	binary.LittleEndian.PutUint16(b[at+2:], code)
	binary.LittleEndian.PutUint32(b[at+4:], uint32(value)) //nolint:gosec // test value
	return b
}

// toggleButtons opens a layout over a fake sysfs and a node holding the given key values
func toggleButtons(t *testing.T, start bool, saved *[]bool, values ...int32) (*Buttons, bool) {
	t.Helper()
	dir := t.TempDir()
	sys := filepath.Join(dir, "sys")
	nodes := filepath.Join(dir, "dev")
	if err := os.MkdirAll(filepath.Join(sys, "event0", "device"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nodes, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sys, "event0", "device", "name"), []byte("keys\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	for _, v := range values {
		raw.Write(rawEvent(evKey, toggleCode, v))
		raw.Write(rawEvent(evSyn, synReport, 0))
	}
	if err := os.WriteFile(filepath.Join(nodes, "event0"), raw.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	l := Layout{
		SysClass: sys, Nodes: nodes,
		Devices:         []Device{{Role: "keys", Name: "keys"}},
		MuteToggle:      toggleCode,
		MuteToggleStart: start,
		MuteToggled:     func(on bool) { *saved = append(*saved, on) },
	}
	return Open(l, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
}

func drain(t *testing.T, b *Buttons) []input.ButtonEvent {
	t.Helper()
	var wg sync.WaitGroup
	b.Start(context.Background(), &wg)
	wg.Wait()
	b.Close()
	var got []input.ButtonEvent
	for {
		select {
		case e := <-b.Events():
			got = append(got, e)
		case <-time.After(50 * time.Millisecond):
			return got
		}
	}
}

func kinds(es []input.ButtonEvent) []input.Kind {
	k := make([]input.Kind, len(es))
	for i, e := range es {
		k[i] = e.Kind
	}
	return k
}

func TestMuteToggleFlipsAndPersists(t *testing.T) {
	var saved []bool
	b, _ := toggleButtons(t, false, &saved, 1, 2, 0, 1)
	got := kinds(drain(t, b))
	want := []input.Kind{input.MuteOff, input.MuteOn, input.MuteOff}
	if !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if !slices.Equal(saved, []bool{true, false}) {
		t.Fatalf("saved = %v, want [true false]", saved)
	}
}

func TestMuteToggleStartReported(t *testing.T) {
	var saved []bool
	b, muted := toggleButtons(t, true, &saved)
	if !muted {
		t.Fatal("muted = false, want true")
	}
	es := drain(t, b)
	if len(es) != 1 || es[0].Kind != input.MuteOn || !es[0].Pressed {
		t.Fatalf("events = %v", es)
	}
	if len(saved) != 0 {
		t.Fatalf("saved = %v", saved)
	}
}

func TestLoadToggleCorruptIsUnmuted(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mute")
	if LoadToggle(p) {
		t.Fatal("missing file loads muted")
	}
	for _, c := range []string{"garbage", "", "muted", "MUTED\n"} {
		if err := os.WriteFile(p, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
		if LoadToggle(p) {
			t.Fatalf("%q loads muted", c)
		}
	}
	if err := SaveToggle(p, true); err != nil {
		t.Fatal(err)
	}
	if !LoadToggle(p) {
		t.Fatal("saved muted loads unmuted")
	}
	if err := SaveToggle(p, false); err != nil {
		t.Fatal(err)
	}
	if LoadToggle(p) {
		t.Fatal("saved unmuted loads muted")
	}
}
