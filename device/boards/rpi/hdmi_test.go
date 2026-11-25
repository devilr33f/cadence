package rpi

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
	"git.shino.boo/devilreef/cadence/device/internal/bluetooth/btfake"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
)

// sysfs is a fake /sys/class/drm under root: one status file per connector
func sysfs(t *testing.T, root string, status map[string]string) {
	t.Helper()
	for conn, st := range status {
		dir := filepath.Join(root, "sys", "class", "drm", conn)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(st+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHDMIStatusReadsEveryHDMIConnector(t *testing.T) {
	root := t.TempDir()
	if c, known := hdmiStatus(root); c || known {
		t.Fatalf("no connectors = %v %v, want unknown", c, known)
	}
	// the owner's pi with nothing plugged in: two hdmi ports and the writeback connector, which is no sink
	sysfs(t, root, map[string]string{
		"card1-HDMI-A-1": "disconnected", "card1-HDMI-A-2": "disconnected", "card1-Writeback-1": "unknown",
	})
	if c, known := hdmiStatus(root); c || !known {
		t.Fatalf("unplugged = %v %v", c, known)
	}
	sysfs(t, root, map[string]string{"card1-HDMI-A-2": "connected"})
	if c, known := hdmiStatus(root); !c || !known {
		t.Fatalf("the second port plugged = %v %v", c, known)
	}
}

func TestHotplugDebouncesFlaps(t *testing.T) {
	connected, known := false, true
	h := hotplug{settle: 1500 * time.Millisecond, read: func() (bool, bool) { return connected, known }}
	t0 := time.Unix(1000, 0)
	at := func(d time.Duration) time.Time { return t0.Add(d) }
	expect := func(now time.Time, wantC, wantOK bool) {
		t.Helper()
		if c, ok := h.step(now); c != wantC || ok != wantOK {
			t.Fatalf("step at %v = %v %v, want %v %v", now.Sub(t0), c, ok, wantC, wantOK)
		}
	}
	// the first read counts at once: the boot's reconcile
	expect(at(0), false, true)
	expect(at(2*time.Second), false, false)
	// plugged: seen, then applied once it held 1.5 s
	connected = true
	expect(at(4*time.Second), false, false)
	expect(at(6*time.Second), true, true)
	expect(at(8*time.Second), false, false)
	// a flap (out for one poll) moves nothing
	connected = false
	expect(at(10*time.Second), false, false)
	connected = true
	expect(at(12*time.Second), false, false)
	expect(at(14*time.Second), false, false)
	// unplugged for good
	connected = false
	expect(at(16*time.Second), false, false)
	expect(at(18*time.Second), false, true)
	// no hdmi connector at all (no kms): the screen is left on, as before the probe
	known = false
	expect(at(20*time.Second), false, false)
	expect(at(22*time.Second), true, true)
}

// fakeControl records what the board asked of the screen's unit
type fakeControl struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeControl) Start(context.Context) error { f.add("start"); return nil }
func (f *fakeControl) Stop(context.Context) error  { f.add("stop"); return nil }

func (f *fakeControl) add(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeControl) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func TestScreenFollowsTheHDMISink(t *testing.T) {
	root := t.TempDir()
	sysfs(t, root, map[string]string{"card1-HDMI-A-1": "disconnected", "card1-HDMI-A-2": "disconnected"})
	ctl := &fakeControl{}
	b := New(env(nil), t.TempDir(), nil, withRoot(root), func(o *options) {
		o.screenControl, o.hdmiEvery, o.hdmiSettle = ctl, 10*time.Millisecond, 20*time.Millisecond
		o.audiod = "/nonexistent/audiod"
		o.qualify = func(context.Context) string { return "test" }
		o.qualifyWait = time.Millisecond
		be := btfake.New()
		o.dial = func(context.Context) (bluetooth.Backend, error) { return be, nil }
	})
	t.Cleanup(func() { _ = b.Close() })

	ids := map[string]bool{}
	for _, c := range b.Capabilities() {
		ids[c.Descriptor().ID] = true
	}
	// every capability stays reported: availability, not the report, follows the sink
	for _, id := range gated {
		if !ids[id] {
			t.Fatalf("%s not reported", id)
		}
		if b.Availability().Available(id) {
			t.Fatalf("%s available with nothing plugged in", id)
		}
	}
	for _, id := range []string{"audio.output", capschema.MediaScreencastID} {
		if !b.Availability().Available(id) {
			t.Fatalf("%s unavailable: the speaker needs no screen", id)
		}
	}
	if got := ctl.got(); !slices.Equal(got, []string{"stop"}) {
		t.Fatalf("at boot = %v, want the unit stopped", got)
	}

	sysfs(t, root, map[string]string{"card1-HDMI-A-1": "connected"})
	waitFor(t, func() bool { return b.Availability().Available(capschema.DisplayScreenID) })
	waitFor(t, func() bool { return slices.Equal(ctl.got(), []string{"stop", "start"}) })
	for _, id := range gated {
		if !b.Availability().Available(id) {
			t.Fatalf("%s unavailable once plugged in", id)
		}
	}

	sysfs(t, root, map[string]string{"card1-HDMI-A-1": "disconnected"})
	waitFor(t, func() bool { return !b.Availability().Available(capschema.DisplayCecID) })
	waitFor(t, func() bool { return slices.Equal(ctl.got(), []string{"stop", "start", "stop"}) })
}

func TestSystemdUnitResetsFailedBeforeStart(t *testing.T) {
	var calls [][]string
	u := &systemdUnit{name: "cadence-screen.service", run: func(_ context.Context, args ...string) error {
		calls = append(calls, args)
		return nil
	}}
	_ = u.Start(context.Background())
	_ = u.Stop(context.Background())
	want := [][]string{
		{"reset-failed", "cadence-screen.service"},
		{"start", "--no-block", "cadence-screen.service"},
		{"stop", "--no-block", "cadence-screen.service"},
	}
	if !slices.EqualFunc(calls, want, slices.Equal) {
		t.Fatalf("calls = %v", calls)
	}
}

func TestScreenUnitFromTheEnvironment(t *testing.T) {
	if b := New(env(nil), t.TempDir(), nil); b.screenUnit != screenUnit {
		t.Fatalf("default unit = %q", b.screenUnit)
	}
	if b := New(env(map[string]string{"CADENCE_RPI_SCREEN_UNIT": "none"}), t.TempDir(), nil); b.screenUnit != "" {
		t.Fatalf("none = %q", b.screenUnit)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
