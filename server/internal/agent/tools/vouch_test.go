package tools_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
)

// sent is one command a vouch test's fake device saw: where it went, and the session the server vouched for
type sent struct {
	device, name string
	vouch        commands.Vouch
	vouched      bool
}

// household is speaker d1 (the turn's) and the TV tv, both with a volume and alarms
func household(context.Context) ([]agent.DeviceInfo, error) {
	caps := []string{agent.CapAudioOutput, "alarms"}
	return []agent.DeviceInfo{
		{ID: "d1", Name: "Кухня", Room: "Кухня", Kind: agent.KindSpeaker, Online: true, Caps: caps},
		{ID: "tv", Name: "Телевизор", Room: "Гостиная", Kind: agent.KindScreen, Online: true, Caps: append(caps, agent.CapScreen)},
	}, nil
}

func vouchRun(t *testing.T, tool agent.Tool, trig cadencev2.Trigger, args string) *agent.ToolError {
	t.Helper()
	c := agent.Call{DeviceID: "d1", SessionID: "s1", Trigger: trig, Args: json.RawMessage(args)}
	if e := agent.Validate(tool, c.Args); e != nil {
		t.Fatalf("%s: %+v", args, e)
	}
	_, e := agent.Run(context.Background(), tool, c)
	return e
}

// TV plan item 14: a write on another device goes with the turn's woken session vouched for (the commands service
// turns it into the target's grant); this device's own commands carry none, and a follow-up or an event turn sends
// nothing to another device at all
func TestACommandOnAnotherDeviceVouchesForTheWokenSession(t *testing.T) {
	var got []sent
	command := func(ctx context.Context, deviceID, _, name string, _ []byte) (json.RawMessage, error) {
		v, ok := commands.VouchFrom(ctx)
		got = append(got, sent{device: deviceID, name: name, vouch: v, vouched: ok})
		switch name {
		case "set_alarm":
			return json.RawMessage(`{"alarm":{"id":"x","time":"07:00","level":50},"replaced":false}`), nil
		case "set_timer":
			return json.RawMessage(`{"timer":{"id":"x","seconds":60,"endsAt":"2026-11-14T00:01:00Z"}}`), nil
		}
		return json.RawMessage(`{"level":60}`), nil
	}
	state := func(_ context.Context, _, capability string) (json.RawMessage, bool) {
		if capability == "alarms" {
			return json.RawMessage(`{"alarms":[],"timers":[]}`), true
		}
		return json.RawMessage(`{"level":30}`), true
	}
	resolver := &agent.Resolver{Household: household}
	alarms := tools.AlarmDeps{State: state, Command: command, Target: resolver.ResolveTarget,
		Now: func() time.Time { return time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC) }}
	volume := tools.DeviceVolume(tools.VolumeDeps{State: state, Command: command, Target: resolver.ResolveTarget})
	woken := commands.Vouch{DeviceID: "d1", SessionID: "s1", Trigger: cadencev2.Trigger_TRIGGER_WAKE}

	for _, c := range []struct {
		tool agent.Tool
		args string
		name string
	}{
		{volume, `{"level":60,"device":"Телевизор"}`, "set_level"},
		{volume, `{"change":-10,"device":"Телевизор"}`, "change_level"},
		{tools.Alarm(alarms), `{"action":"set","time":"7:00","device":"Телевизор"}`, "set_alarm"},
		{tools.Timer(alarms), `{"action":"set","seconds":60,"device":"Телевизор"}`, "set_timer"},
	} {
		got = nil
		if e := vouchRun(t, c.tool, cadencev2.Trigger_TRIGGER_WAKE, c.args); e != nil {
			t.Fatalf("%s: %+v", c.args, e)
		}
		if len(got) != 1 || got[0].device != "tv" || got[0].name != c.name || !got[0].vouched || got[0].vouch != woken {
			t.Fatalf("%s: sent %+v", c.args, got)
		}
	}

	got = nil
	if e := vouchRun(t, volume, cadencev2.Trigger_TRIGGER_WAKE, `{"level":60}`); e != nil {
		t.Fatal(e)
	}
	if len(got) != 1 || got[0].device != "d1" || got[0].vouched {
		t.Fatalf("this device's raise: sent %+v", got)
	}

	// rule 7 holds outside a woken session: no raise and no alarm, on this device or another; an event turn drives
	// no other device at all. a follow-up is the same conversation, so it may lower another device's level
	for _, trig := range []cadencev2.Trigger{cadencev2.Trigger_TRIGGER_FOLLOW_UP, cadencev2.Trigger_TRIGGER_EVENT} {
		got = nil
		if e := vouchRun(t, volume, trig, `{"level":60,"device":"Телевизор"}`); e == nil {
			t.Fatalf("%v raise went", trig)
		}
		if e := vouchRun(t, tools.Alarm(alarms), trig, `{"action":"set","time":"7:00","device":"Телевизор"}`); e == nil {
			t.Fatalf("%v alarm went", trig)
		}
		e := vouchRun(t, volume, trig, `{"change":-10,"device":"Телевизор"}`)
		if lowered := e == nil; lowered != (trig == cadencev2.Trigger_TRIGGER_FOLLOW_UP) {
			t.Fatalf("%v lower: %+v", trig, e)
		}
		if trig == cadencev2.Trigger_TRIGGER_EVENT && len(got) != 0 {
			t.Fatalf("%v: sent %+v", trig, got)
		}
		if trig == cadencev2.Trigger_TRIGGER_FOLLOW_UP && (len(got) != 1 || got[0].name != "change_level" || got[0].vouched) {
			t.Fatalf("%v: sent %+v", trig, got)
		}
	}
}
