package screens_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/screens"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

func stop(r *rig, dev, turnID string) calls.Reply {
	args, _ := json.Marshal(map[string]string{"turnId": turnID})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return r.svc.StopMethod()(ctx, calls.Call{DeviceID: dev, ID: "s1", Method: rpc.AssistantStop, Args: args})
}

func (r *rig) stopped() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.stops)
}

func (r *rig) sessionStart(dev, id string) {
	r.mu.Lock()
	r.live[dev] = id
	r.mu.Unlock()
	r.svc.Inbound(dev, &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionStart{SessionStart: &cadencev2.SessionStart{
		SessionId: id, Trigger: cadencev2.Trigger_TRIGGER_WAKE,
	}}})
}

// a screen's stop ends the voice session of the turn it shows, on the room's speaker; a stale id stops the newest
// turn it shows, and a screen of another room stops nothing
func TestScreenStopEndsTheRoomSession(t *testing.T) {
	r := newRig(t)
	r.sessionStart("spk", "s1")
	r.await("tv", "listening", func(at *cadencev2.AssistantTurn) bool { return at.GetTurnId() == "s1" })
	if rep := stop(r, "kitchen-tv", "s1"); rep.Status != rpc.StatusNotFound {
		t.Fatalf("another room's stop %+v", rep)
	}
	if rep := stop(r, "spk", "s1"); rep.Status != rpc.StatusInvalid {
		t.Fatalf("a speaker's stop %+v", rep)
	}
	rep := stop(r, "tv", "s1")
	if a, _ := rep.JSON.(screens.StopAnswer); rep.Status != rpc.StatusOK || a.TurnID != "s1" {
		t.Fatalf("stop %+v", rep)
	}
	if got := r.stopped(); !slices.Equal(got, []string{"spk/s1"}) {
		t.Fatalf("stops %v", got)
	}
	// the follow-up is a session of its own; the drawer still names the first
	r.sessionStart("spk", "s2")
	r.await("tv", "the follow-up", func(at *cadencev2.AssistantTurn) bool { return at.GetTurnId() == "s2" })
	if rep := stop(r, "tv", "s1"); rep.Status != rpc.StatusOK {
		t.Fatalf("stale stop %+v", rep)
	}
	if got := r.stopped(); !slices.Equal(got, []string{"spk/s1", "spk/s2"}) {
		t.Fatalf("stops %v", got)
	}
	if rep := stop(r, "tv", ""); rep.Status != rpc.StatusNotFound {
		t.Fatalf("nothing runs: %+v", rep)
	}
}

// a stop between the press and the speaker's session start stops the session as it starts
func TestScreenStopBeforeThePressedSessionStarts(t *testing.T) {
	r := newRig(t)
	ans, _ := invoke(r, "tv").JSON.(screens.InvokeAnswer)
	if rep := stop(r, "tv", ans.TurnID); rep.Status != rpc.StatusOK {
		t.Fatalf("stop %+v", rep)
	}
	r.sessionStart("spk", ans.TurnID)
	deadline := time.Now().Add(2 * time.Second)
	for len(r.stopped()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := r.stopped(); !slices.Equal(got, []string{"spk/" + ans.TurnID}) {
		t.Fatalf("stops %v", got)
	}
}
