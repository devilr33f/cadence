package screen_test

import (
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/screen"
	"git.shino.boo/devilreef/cadence/device/internal/screen/screentest"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
)

// every level in force reaches the screen to show; one that connects later gets the latest as its state, not shown
func TestVolumeReachesTheScreen(t *testing.T) {
	r := newRig(t, screen.Options{})
	r.srv.ShowVolume(40, false, false)
	s := r.connect(t, screentest.Options{})
	if v := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetVolumeState() != nil }).GetVolumeState(); v.GetLevel() != 40 || v.GetShow() {
		t.Fatalf("on attach: %v", v)
	}
	r.srv.ShowVolume(35, true, true)
	if v := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetVolumeState().GetShow() }).GetVolumeState(); v.GetLevel() != 35 || !v.GetMuted() {
		t.Fatalf("shown: %v", v)
	}
	r.srv.ShowVolume(400, false, true)
	if v := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetVolumeState().GetLevel() == 100 }).GetVolumeState(); v.GetMuted() {
		t.Fatalf("clamped: %v", v)
	}
}
