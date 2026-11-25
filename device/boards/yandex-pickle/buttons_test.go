package pickle_test

import (
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pickle "git.shino.boo/devilreef/cadence/device/boards/yandex-pickle"
	"git.shino.boo/devilreef/cadence/device/internal/input"
)

// inputDevice names an evdev node in sysfs as the kernel does, and gives it the events it will read
func (tr *tree) inputDevice(node, name string, events ...ev) {
	tr.t.Helper()
	tr.write("sys/class/input/"+node+"/device/name", name+"\n")
	tr.writeBytes("dev/input/"+node, encode(events...))
}

func positions(on ...bool) func(*os.File) (bool, bool) {
	var calls atomic.Int32
	return func(*os.File) (bool, bool) {
		i := int(calls.Add(1)) - 1
		if i >= len(on) {
			return false, false
		}
		return on[i], true
	}
}

func newBoard(t *testing.T, tr *tree, opts ...pickle.Option) (*pickle.Board, *logBuf) {
	t.Helper()
	log, buf := newLog()
	b := pickle.New(tr.path("data/cadence"), log, append([]pickle.Option{pickle.WithRoot(tr.root), pickle.WithoutSoloistUpdates()}, opts...)...)
	t.Cleanup(func() { _ = b.Close() })
	return b, buf
}

type press struct {
	kind    input.Kind
	pressed bool
}

func collect(t *testing.T, events <-chan input.ButtonEvent, n int) []press {
	t.Helper()
	var got []press
	for len(got) < n {
		select {
		case e := <-events:
			got = append(got, press{e.Kind, e.Pressed})
		case <-time.After(5 * time.Second):
			t.Fatalf("after %v: no more events", got)
		}
	}
	select {
	case e := <-events:
		t.Fatalf("after %v: an extra %v", got, e)
	case <-time.After(50 * time.Millisecond):
	}
	return got
}

func waitLog(t *testing.T, logs *logBuf, line string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), line) {
		if time.Now().After(deadline) {
			t.Fatalf("no %q in %q", line, logs.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestKeysReachTheInputAsPressesAndReleases(t *testing.T) {
	tr := pickleTree(t)
	tr.write("sys/class/input/event0/device/name", "gpio-keys-other\n")
	tr.inputDevice("event2", "aw933xx",
		ev{evKey, keyVoiceCommand, 1}, syn(), ev{evKey, keyVoiceCommand, 0}, syn(),
		// a held key repeats (2): that is no new press
		ev{evKey, keyVolumeUp, 1}, syn(), ev{evKey, keyVolumeUp, 2}, syn(), ev{evKey, keyVolumeUp, 0}, syn(),
		ev{evKey, keyVolumeDown, 1}, syn(), ev{evKey, keyVolumeDown, 0}, syn(),
		ev{evKey, keyA, 1}, syn(), ev{evKey, keyA, 0}, syn())
	b, logs := newBoard(t, tr)
	got := collect(t, pickle.ButtonEvents(b), 6)
	want := []press{{input.Action, true}, {input.Action, false}, {input.VolumeUp, true}, {input.VolumeUp, false},
		{input.VolumeDown, true}, {input.VolumeDown, false}}
	if !slices.Equal(got, want) {
		t.Fatalf("events = %v", got)
	}
	// a fake node ends where a real one would fail
	waitLog(t, logs, "msg=input_lost device=keys")
	waitLog(t, logs, "msg=input_unavailable device=mute reason=not_found")
}

func TestMuteSwitchPositions(t *testing.T) {
	tr := pickleTree(t)
	tr.inputDevice("event1", "mute",
		// the mute button itself does nothing; the switch it latches does
		ev{evKey, keyMute, 1}, syn(), ev{evKey, keyMute, 0}, syn(),
		ev{evSw, swMuteDevice, 1}, syn(), ev{evSw, swMuteDevice, 0}, syn(),
		// the kernel dropped events: what follows up to the next report is partial, and the switch is read afresh
		ev{evSyn, synDropped, 0}, ev{evSw, swMuteDevice, 0}, ev{evKey, keyMute, 1}, syn())
	b, logs := newBoard(t, tr, pickle.WithSwitch(positions(false, true)))
	got := collect(t, pickle.ButtonEvents(b), 4)
	// the position it starts in first, then each change, then the position read after the drop
	want := []press{{input.MuteOff, false}, {input.MuteOn, true}, {input.MuteOff, false}, {input.MuteOn, true}}
	if !slices.Equal(got, want) {
		t.Fatalf("events = %v", got)
	}
	waitLog(t, logs, "msg=input_resync device=mute")
	waitLog(t, logs, "msg=input_unavailable device=keys reason=not_found")
}

func TestTheSwitchPositionAtBootReachesTheInput(t *testing.T) {
	tr := pickleTree(t)
	tr.inputDevice("event1", "mute")
	b, _ := newBoard(t, tr, pickle.WithSwitch(positions(true)))
	in := b.Input()
	if in == nil {
		t.Fatal("no input")
	}
	// muted before anything runs, so the first MuteState any audiod gets is the real position
	select {
	case s := <-in.Capability().State():
		if string(s) != `{"muted":true,"wake":false}` {
			t.Fatalf("state = %s", s)
		}
	default:
		t.Fatal("no state")
	}
}

func TestMissingButtonDevicesAreLoggedAndTheBoardRuns(t *testing.T) {
	tr := pickleTree(t)
	// named in sysfs, with no node to open
	tr.write("sys/class/input/event4/device/name", "mute\n")
	b, logs := newBoard(t, tr)
	if b.Input() == nil {
		t.Fatal("no input")
	}
	_ = b.Capabilities()
	if l := logs.String(); strings.Count(l, "msg=input_unavailable") != 2 ||
		!strings.Contains(l, "msg=input_unavailable device=keys reason=not_found") ||
		!strings.Contains(l, "msg=input_unavailable device=mute reason=open_failed") {
		t.Fatalf("logs = %q", l)
	}
	collect(t, pickle.ButtonEvents(b), 0)
}
