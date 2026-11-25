package commands_test

import (
	"context"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
)

// TV plan item 14: a command sent for a woken session of another device carries a fresh grant bound to the target,
// the command and 30 s; one for the target's own session, a follow-up, an event turn or no vouch carries none
func TestACommandForAnotherDevicesWokenSessionCarriesAGrant(t *testing.T) {
	e := newEnv(t)
	now := time.UnixMilli(1_794_700_000_000)
	commands.SetClock(e.svc, func() time.Time { return now })
	e.answer(ok(`{}`))
	send := func(ctx context.Context) *cadencev2.CapabilityCommand {
		t.Helper()
		if _, err := e.svc.Send(ctx, devID, "alarms", "set_alarm", nil); err != nil {
			t.Fatal(err)
		}
		got := e.conn.commands()
		return got[len(got)-1]
	}
	woken := commands.WithVouch(context.Background(), commands.Vouch{DeviceID: "dev-a", SessionID: "sess-a", Trigger: cadencev2.Trigger_TRIGGER_WAKE})
	g := send(woken).GetGrant()
	if g.GetCallerDeviceId() != "dev-a" || g.GetCallerSessionId() != "sess-a" || g.GetCallerTrigger() != cadencev2.Trigger_TRIGGER_WAKE ||
		g.GetTargetDeviceId() != devID || g.GetCapability() != "alarms" || g.GetCommand() != "set_alarm" ||
		g.GetExpiresAtUnixMs() != now.Add(30*time.Second).UnixMilli() || len(g.GetId()) < 16 {
		t.Fatalf("grant = %v", g)
	}
	if again := send(woken).GetGrant(); again.GetId() == g.GetId() {
		t.Fatal("a grant's id was used twice")
	}
	pressed := commands.WithVouch(context.Background(), commands.Vouch{DeviceID: "dev-a", SessionID: "sess-a", Trigger: cadencev2.Trigger_TRIGGER_BUTTON})
	if send(pressed).GetGrant() == nil {
		t.Fatal("a pressed session got no grant")
	}
	for name, ctx := range map[string]context.Context{
		"no vouch":   context.Background(),
		"own device": commands.WithVouch(context.Background(), commands.Vouch{DeviceID: devID, SessionID: "s", Trigger: cadencev2.Trigger_TRIGGER_WAKE}),
		"follow-up":  commands.WithVouch(context.Background(), commands.Vouch{DeviceID: "dev-a", SessionID: "s", Trigger: cadencev2.Trigger_TRIGGER_FOLLOW_UP}),
		"event":      commands.WithVouch(context.Background(), commands.Vouch{DeviceID: "dev-a", SessionID: "s", Trigger: cadencev2.Trigger_TRIGGER_EVENT}),
		"no session": commands.WithVouch(context.Background(), commands.Vouch{DeviceID: "dev-a", Trigger: cadencev2.Trigger_TRIGGER_WAKE}),
	} {
		if g := send(ctx).GetGrant(); g != nil {
			t.Fatalf("%s: grant = %v", name, g)
		}
	}
}
