package bluetooth_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
	"git.shino.boo/devilreef/cadence/device/internal/bluetooth/btfake"
)

// withSpeaker is withOutput with the speaker's volume and buttons recorded: "hardware <on>", "speaker <level>",
// "control <action>"; a level the speaker set becomes the level, as the board's setter does
func withSpeaker() func(h *harness, o *bluetooth.Options) {
	return func(h *harness, o *bluetooth.Options) {
		withOutput("")(h, o)
		o.Output.Hardware = func(on bool) {
			if on && !slices.ContainsFunc(h.be.Calls(), func(c string) bool { return strings.HasPrefix(c, "SetVolume ") }) {
				// rule 7: audiod's gain goes to full only once the speaker holds the level
				h.t.Errorf("hardware before the speaker's volume was set: %v", h.be.Calls())
			}
			h.outEvent(fmt.Sprintf("hardware %t", on))
		}
		o.Output.SpeakerLevel = func(level int) {
			h.outEvent(fmt.Sprintf("speaker %d", level))
			h.setLevel(level)
		}
		o.Output.Control = func(_ context.Context, action string) { h.outEvent("control " + action) }
	}
}

func (h *harness) waitEvent(e string) {
	h.t.Helper()
	waitFor(h.t, e, func() bool { return slices.Contains(h.outEvents(), e) })
}

func (h *harness) count(prefix string) int {
	n := 0
	for _, c := range h.be.Calls() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// a speaker that takes absolute volume plays at the level on its own volume (bluealsa's soft volume off), the level
// follows its buttons, and a level set here goes back to it; its first report and one right after our write are
// answered, never applied (rule 7)
func TestSpeakerVolumeIsTheLevel(t *testing.T) {
	h := newHarness(t, withSpeaker())
	pcm := btfake.SourcePCMPath(speakerAddr)
	h.be.SetHardVolume(speakerAddr, true)
	// the speaker kept 127 from before: never applied
	h.be.AddSpeaker(speakerAddr, speakerName, true)
	h.waitEvent("hardware true")
	h.waitCall("SetSoftVolume false " + pcm)
	h.waitCall("SetVolume 38 " + pcm)
	if lvl, _ := h.currentLevel(); lvl != 30 {
		t.Fatalf("level %d after connect", lvl)
	}

	h.setLevel(50)
	h.waitCall("SetVolume 64 " + pcm)

	// the speaker answers our write with a value of its own at once: our level again
	h.be.SpeakerVolume(speakerAddr, 90)
	waitFor(t, "the level written again", func() bool { return h.count("SetVolume 64 ") == 2 })
	if lvl, _ := h.currentLevel(); lvl != 50 {
		t.Fatalf("level %d after the settle report", lvl)
	}

	// the owner presses the speaker's volume up later
	h.clock.add(2 * time.Second)
	h.be.SpeakerVolume(speakerAddr, 100)
	h.waitEvent("speaker 79")
	h.waitLog("bluetooth_speaker_volume_moved")
	time.Sleep(50 * time.Millisecond)
	if n := h.count("SetVolume 100 "); n != 0 {
		t.Fatalf("the speaker's own move written back %d times", n)
	}

	// gone: audiod's gain is the level before anything else plays
	h.be.Drop(speakerAddr)
	h.waitRoute("")
	evs := h.outEvents()
	// the gain back to the level just before the route away
	if n := len(evs); n < 2 || evs[n-2] != "hardware false" || evs[n-1] != "route " {
		t.Fatalf("events %v", evs)
	}
}

// a speaker without absolute volume keeps bluealsa's volume untouched: audiod's gain carries the level
func TestSpeakerWithoutAbsoluteVolume(t *testing.T) {
	h := newHarness(t, withSpeaker())
	h.be.AddSpeaker(speakerAddr, speakerName, true)
	h.waitRoute(speakerAddr)
	h.setLevel(50)
	time.Sleep(50 * time.Millisecond)
	if n := h.count("SetSoftVolume ") + h.count("SetVolume "); n != 0 {
		t.Fatalf("the pcm touched: %v", h.be.Calls())
	}
	if slices.Contains(h.outEvents(), "hardware true") {
		t.Fatal("hardware volume without absolute volume")
	}
}

// the speaker's buttons reach the board through our registered player, a play on connect aside, and the player
// shows the device's status
func TestSpeakerButtons(t *testing.T) {
	h := newHarness(t, withSpeaker())
	h.waitCall("RegisterPlayer")
	h.waitCall("SetPlayerStatus Stopped")
	h.be.AddSpeaker(speakerAddr, speakerName, true)
	h.waitRoute(speakerAddr)

	// many speakers send play as they connect
	h.be.Press("Play")
	h.waitLog("bluetooth_speaker_control_ignored")

	h.clock.add(4 * time.Second)
	controls := func() []string {
		var got []string
		for _, e := range h.outEvents() {
			if strings.HasPrefix(e, "control ") {
				got = append(got, e)
			}
		}
		return got
	}
	var want []string
	for _, p := range []struct{ method, action string }{{"Pause", "pause"}, {"PlayPause", "toggle"}, {"Next", "next"},
		{"Previous", "previous"}, {"Stop", "pause"}, {"Play", "resume"}} {
		h.be.Press(p.method)
		want = append(want, "control "+p.action)
		waitFor(t, p.method, func() bool { return slices.Equal(controls(), want) })
	}

	h.b.SetPlaying(bluetooth.PlayerPlaying)
	h.waitCall("SetPlayerStatus Playing")
	h.b.SetPlaying(bluetooth.PlayerPaused)
	h.waitCall("SetPlayerStatus Paused")

	// bluez restarted: the player registers again
	h.be.Update(func(s *bluetooth.Snapshot) { s.Adapter = nil })
	h.waitLog("bluetooth_adapter_gone")
	h.be.Update(func(s *bluetooth.Snapshot) {
		s.Adapter = &bluetooth.Adapter{Path: btfake.AdapterPath, Address: "00:11:22:33:44:55", Powered: true}
	})
	waitFor(t, "registered again", func() bool { return h.count("RegisterPlayer") == 2 })
	waitFor(t, "status again", func() bool { return h.count("SetPlayerStatus Paused") == 2 })
}
