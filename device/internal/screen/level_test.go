package screen_test

import (
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/screen"
	"git.shino.boo/devilreef/cadence/device/internal/screen/screentest"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
)

func gateFrame(open bool) func(*screenv1.Envelope) bool {
	return func(e *screenv1.Envelope) bool {
		return e.GetPlaybackGate() != nil && e.GetPlaybackGate().GetOpen() == open
	}
}

// a board without audiod (sim-tv on a mac): the gate opens once the screen's player took the level in force, and
// never before (rule 7)
func TestDirectGateOpensOnceThePlayerHoldsTheLevel(t *testing.T) {
	r := newRig(t, screen.Options{DirectGate: true})
	s := r.connect(t, screentest.Options{})
	if s.Frames()[0].GetReady().GetGateOpen() {
		t.Fatal("ready says open before any level")
	}
	r.srv.DirectLevel(30)
	lv := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetSetLevel() != nil })
	if lv.GetSetLevel().GetLevel() != 30 {
		t.Fatalf("level %d, want the one in force", lv.GetSetLevel().GetLevel())
	}
	frame(t, s, gateFrame(true))

	// a later level is handed on, and the gate stays open
	r.srv.DirectLevel(12)
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetSetLevel().GetLevel() == 12 })

	// a screen that attaches later gets the level first, and the gate only after it held it
	s2 := r.connect(t, screentest.Options{})
	if s2.Frames()[0].GetReady().GetGateOpen() {
		t.Fatal("a new screen starts open")
	}
	frame(t, s2, func(e *screenv1.Envelope) bool { return e.GetSetLevel().GetLevel() == 12 })
	frame(t, s2, gateFrame(true))
}

func TestDirectGateStaysShutWhenThePlayerRefusesTheLevel(t *testing.T) {
	r := newRig(t, screen.Options{DirectGate: true})
	r.srv.DirectLevel(30)
	s := r.connect(t, screentest.Options{Code: "unsupported"})
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetSetLevel() != nil })
	if _, err := s.Wait(200*time.Millisecond, gateFrame(true)); err == nil {
		t.Fatal("gate opened on a refused level")
	}
}

// a board with audiod never hands the level on itself: audiod's VolumeApplied opens its gate
func TestNoDirectLevelWithAudiod(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	r.srv.DirectLevel(30)
	if _, err := s.Wait(200*time.Millisecond, func(e *screenv1.Envelope) bool { return e.GetSetLevel() != nil }); err == nil {
		t.Fatal("level sent without DirectGate")
	}
	if _, err := s.Wait(50*time.Millisecond, gateFrame(true)); err == nil {
		t.Fatal("gate opened without audiod's word")
	}
}
