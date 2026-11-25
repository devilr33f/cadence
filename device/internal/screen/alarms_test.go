package screen_test

import (
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/screen"
	"git.shino.boo/devilreef/cadence/device/internal/screen/screentest"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// the household's alarms reach the screen, and a screen that connects later gets the last summary, so a ring that
// began before it shows; once the link drops, none is kept
func TestAlarmsSummaryReachesLaterScreens(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	up := &uplink{}
	r.srv.LinkUp(up.send, &cadencev2.DeviceInfo{Name: "TV"})
	sum := &cadencev2.AlarmsSummary{Revision: 3, Ringing: []*cadencev2.AlarmRing{{DeviceId: "d1", Kind: "alarm", Id: "a1", Own: true}}}
	if !r.srv.LinkMessage(&cadencev2.Envelope{Body: &cadencev2.Envelope_AlarmsSummary{AlarmsSummary: sum}}) {
		t.Fatal("alarms summary not taken")
	}
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetAlarmsSummary().GetRevision() == 3 })

	later := r.connect(t, screentest.Options{})
	got := frame(t, later, func(e *screenv1.Envelope) bool { return e.GetAlarmsSummary() != nil }).GetAlarmsSummary()
	if len(got.GetRinging()) != 1 || !got.GetRinging()[0].GetOwn() {
		t.Fatalf("later screen got %v", got)
	}

	r.srv.LinkDown()
	third := r.connect(t, screentest.Options{})
	if _, err := third.Wait(300*time.Millisecond, func(e *screenv1.Envelope) bool { return e.GetAlarmsSummary() != nil }); err == nil {
		t.Fatal("a summary kept past the link")
	}
}
