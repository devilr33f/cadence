package vkcapsula_test

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	vkcapsula "git.shino.boo/devilreef/cadence/device/boards/vk-capsula-mini"
	"git.shino.boo/devilreef/cadence/device/internal/input"
)

func newBoard(t *testing.T, tr *tree) (*vkcapsula.Board, *logBuf) {
	t.Helper()
	log, buf := newLog()
	b := vkcapsula.New(tr.path("data/cadence"), log, vkcapsula.WithRoot(tr.root))
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

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestButtonsMapOnlyVerifiedKeys(t *testing.T) {
	l := vkcapsula.ButtonLayout("")
	if len(l.Devices) != 1 || l.Devices[0].Name != "csk05t" || l.MuteToggle != keyMicMute {
		t.Fatalf("layout = %+v", l)
	}
	if len(l.Keys) != 3 || l.Keys[keyPlayPause] != input.Action || l.Keys[keyVolumeUp] != input.VolumeUp ||
		l.Keys[keyVolumeDown] != input.VolumeDown {
		t.Fatalf("keys = %+v", l.Keys)
	}
	for _, code := range []uint16{keyCentre, keyPower, keyA} {
		if _, ok := l.Keys[code]; ok {
			t.Fatalf("key %d is mapped", code)
		}
	}

	tr := capsulaTree(t)
	tr.writeBytes("dev/input/event0", encode(
		ev{evKey, keyCentre, 1}, syn(), ev{evKey, keyCentre, 0}, syn(),
		ev{evKey, keyPower, 1}, syn(), ev{evKey, keyPower, 0}, syn(),
		ev{evKey, keyPlayPause, 1}, syn(), ev{evKey, keyPlayPause, 0}, syn(),
		ev{evKey, keyVolumeUp, 1}, syn(), ev{evKey, keyVolumeUp, 0}, syn()))
	b, _ := newBoard(t, tr)
	got := collect(t, vkcapsula.ButtonEvents(b), 5)
	// the start position first, then only the two verified keys
	want := []press{{input.MuteOff, false}, {input.Action, true}, {input.Action, false}, {input.VolumeUp, true},
		{input.VolumeUp, false}}
	if !slices.Equal(got, want) {
		t.Fatalf("events = %v", got)
	}
}

func TestMuteButtonTogglesAndPersists(t *testing.T) {
	tr := capsulaTree(t)
	tr.writeBytes("dev/input/event0", encode(
		ev{evKey, keyMicMute, 1}, syn(), ev{evKey, keyMicMute, 0}, syn()))
	b, _ := newBoard(t, tr)
	got := collect(t, vkcapsula.ButtonEvents(b), 2)
	if want := []press{{input.MuteOff, false}, {input.MuteOn, true}}; !slices.Equal(got, want) {
		t.Fatalf("events = %v", got)
	}
	state := tr.path("data/cadence/state/mute")
	waitFor(t, "mute state saved", func() bool {
		raw, err := os.ReadFile(state)
		return err == nil && string(raw) == "muted\n"
	})

	// a restart: the fake node has no events now, and the board starts muted
	tr.writeBytes("dev/input/event0", nil)
	b2, _ := newBoard(t, tr)
	in := b2.Input()
	if in == nil || !in.Muted() {
		t.Fatal("not muted after a restart")
	}
	select {
	case s := <-in.Capability().State():
		if !strings.Contains(string(s), `"muted":true`) {
			t.Fatalf("state = %s", s)
		}
	default:
		t.Fatal("no state")
	}
}

func TestMuteStateSaveFailureIsLogged(t *testing.T) {
	tr := capsulaTree(t)
	// a file where the state directory belongs
	tr.write("data/cadence/state", "")
	tr.writeBytes("dev/input/event0", encode(ev{evKey, keyMicMute, 1}, syn()))
	b, logs := newBoard(t, tr)
	_ = vkcapsula.ButtonEvents(b)
	waitFor(t, "failure logged", func() bool { return strings.Contains(logs.String(), "msg=mute_state_save_failed") })
}
