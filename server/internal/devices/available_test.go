package devices_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

func unavailable(capability, js string, down bool) *cadencev2.Envelope {
	return &cadencev2.Envelope{Body: &cadencev2.Envelope_StateDelta{
		StateDelta: &cadencev2.StateDelta{Capability: capability, Json: js, Unavailable: down},
	}}
}

// an unplugged screen: its capability stays reported and configured, its state is hidden, and the detail says so;
// plugged in again, all of it is back without a new report
func TestUnavailableCapabilityKeepsItsRowAndHidesItsState(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.sendReport(t, report(desc("audio")))
	var heard []string
	e.svc.OnState(func(_, capability string, state json.RawMessage) { heard = append(heard, capability+string(state)) })

	e.svc.Handle(ctx, e.id, unavailable("audio", `{"playing":true}`, false))
	if c := e.capability(t, "audio"); !c.Available {
		t.Fatal("a delta without the flag left it unavailable")
	}
	e.svc.Handle(ctx, e.id, unavailable("audio", `{"playing":true}`, true))
	if _, ok := e.svc.State(ctx, e.id, "audio"); ok {
		t.Fatal("an unavailable capability's state is served")
	}
	if c := e.capability(t, "audio"); c.Available || strings.ReplaceAll(string(c.State), " ", "") != `{"playing":true}` {
		t.Fatalf("detail = %+v", c)
	}
	// availability alone (no state to send): plugged in again
	e.svc.Handle(ctx, e.id, unavailable("audio", "", false))
	if got, ok := e.svc.State(ctx, e.id, "audio"); !ok || strings.ReplaceAll(string(got), " ", "") != `{"playing":true}` {
		t.Fatalf("state = %s %v", got, ok)
	}
	// the same availability again is no change
	e.svc.Handle(ctx, e.id, unavailable("audio", "", false))
	if len(heard) != 3 {
		t.Fatalf("heard %v, want the state, then each flip", heard)
	}
	if !strings.Contains(e.logs.String(), `capability_availability`) || strings.Contains(e.logs.String(), "state_invalid") {
		t.Fatalf("log: %s", e.logs)
	}
}
