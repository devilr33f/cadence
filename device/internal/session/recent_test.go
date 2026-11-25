package session

import (
	"fmt"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

func TestRecentKeepsTheLast16(t *testing.T) {
	var r Recent
	for i := range 20 {
		trig := cadencev2.Trigger_TRIGGER_WAKE
		if i%2 == 1 {
			trig = cadencev2.Trigger_TRIGGER_FOLLOW_UP
		}
		r.Record(fmt.Sprintf("s%d", i), trig)
	}
	for i := range 4 {
		if _, ok := r.Trigger(fmt.Sprintf("s%d", i)); ok {
			t.Fatalf("s%d kept past 16", i)
		}
	}
	for i := 4; i < 20; i++ {
		want := cadencev2.Trigger_TRIGGER_WAKE
		if i%2 == 1 {
			want = cadencev2.Trigger_TRIGGER_FOLLOW_UP
		}
		if got, ok := r.Trigger(fmt.Sprintf("s%d", i)); !ok || got != want {
			t.Fatalf("s%d = %v, %v", i, got, ok)
		}
	}
	if _, ok := r.Trigger(""); ok {
		t.Fatal("the empty id is known")
	}
	if _, ok := r.Trigger("other"); ok {
		t.Fatal("an unknown id is known")
	}
}

func TestBeginRecordsTheSessionBeforeTheServerKnowsIt(t *testing.T) {
	recent := &Recent{}
	r := newRig(t, 0, WithRecent(recent))
	recorded := make(chan bool, 1)
	r.onSend = func(env *cadencev2.Envelope) {
		if start := env.GetSessionStart(); start != nil {
			got, ok := recent.Trigger(start.GetSessionId())
			recorded <- ok && got == cadencev2.Trigger_TRIGGER_BUTTON
		}
	}
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON}
	if start := r.next(t).GetSessionStart(); start.GetSessionId() != "s1" {
		t.Fatalf("start = %v", start)
	}
	if !<-recorded {
		t.Fatal("s1 was not recorded as a press when its start went out")
	}
}
