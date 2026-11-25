package tools_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/presence"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

type fakeBusy struct {
	puts    []string
	ttl     time.Duration
	devices []string
	deleted []string
	on      bool
	err     error
}

var dndNow = time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)

func (f *fakeBusy) Put(_ context.Context, source, reason string, devices []string, ttl time.Duration) (time.Time, error) {
	if f.err != nil {
		return time.Time{}, f.err
	}
	f.puts, f.ttl, f.devices, f.on = append(f.puts, source+" "+reason), ttl, devices, true
	return dndNow.Add(ttl), nil
}

func (f *fakeBusy) Delete(_ context.Context, source string) error {
	f.deleted = append(f.deleted, source)
	if !f.on {
		return presence.ErrNotFound
	}
	f.on = false
	return nil
}

func dndDeps(f *fakeBusy) tools.DNDDeps {
	return tools.DNDDeps{Busy: f, Settings: func(context.Context, string) (settings.Values, error) {
		return settings.Values{TimeZone: "Asia/Bishkek"}, nil
	}}
}

func TestDoNotDisturbOnAndOff(t *testing.T) {
	f := &fakeBusy{}
	tool := tools.DoNotDisturb(dndDeps(f))
	out, e := run(t, tool, wake, `{"minutes":90}`)
	if e != nil || string(out) != `{"minutes":90,"on":true,"until":"16:30"}` {
		t.Fatalf("on: %s %+v", out, e)
	}
	// every device, as the voice source, for exactly the minutes asked
	if len(f.puts) != 1 || f.puts[0] != "voice dnd" || f.devices != nil || f.ttl != 90*time.Minute {
		t.Fatalf("put %v %v %v", f.puts, f.devices, f.ttl)
	}
	if out, e := run(t, tool, button, `{"off":true}`); e != nil || string(out) != `{"on":false,"wasOn":true}` {
		t.Fatalf("off: %s %+v", out, e)
	}
	// off ends the dashboard's do-not-disturb too
	if len(f.deleted) != 2 || f.deleted[0] != "voice" || f.deleted[1] != "dashboard" {
		t.Fatalf("deleted %v", f.deleted)
	}
	if out, e := run(t, tool, wake, `{"off":true}`); e != nil || string(out) != `{"on":false,"wasOn":false}` {
		t.Fatalf("off again: %s %+v", out, e)
	}
	if out, e := run(t, tool, wake, `{"minutes":1440}`); e != nil || f.ttl != presence.MaxTTL {
		t.Fatalf("a day: %s %+v", out, e)
	}
}

func TestDoNotDisturbArguments(t *testing.T) {
	f := &fakeBusy{}
	tool := tools.DoNotDisturb(dndDeps(f))
	for _, args := range []string{`{}`, `{"minutes":0}`, `{"minutes":1441}`, `{"minutes":30,"off":true}`, `{"off":false}`, `{"for":5}`} {
		if _, e := run(t, tool, wake, args); e == nil || e.Code != "invalid_arguments" {
			t.Fatalf("%s: %+v", args, e)
		}
	}
	if len(f.puts) != 0 || len(f.deleted) != 0 {
		t.Fatalf("calls %v %v", f.puts, f.deleted)
	}
	f.err = errors.New("store down")
	if _, e := run(t, tool, wake, `{"minutes":5}`); e == nil || e.Code != "internal" {
		t.Fatalf("store: %+v", e)
	}
}

// a follow-up turns it on or off (the owner's call: only rule 7 needs a woken
// session, and quiet never raises the volume or sets an alarm); a turn the
// server started is never offered it
func TestDoNotDisturbInAFollowUp(t *testing.T) {
	f := &fakeBusy{}
	tool := tools.DoNotDisturb(dndDeps(f))
	if tool.Effects != agent.Write || tool.RequiresWokenSession || tool.EventTurns || tool.TaintExempt {
		t.Fatalf("policy %+v", tool)
	}
	if got := agent.NewRegistry(tool).Offer(nil, false, event); len(got) != 0 {
		t.Fatalf("an event turn is offered %d", len(got))
	}
	for _, args := range []string{`{"minutes":30}`, `{"off":true}`} {
		if _, e := run(t, tool, followUp, args); e != nil {
			t.Fatalf("%s: %+v", args, e)
		}
	}
	if len(f.puts) != 1 || len(f.deleted) == 0 {
		t.Fatalf("calls %v %v", f.puts, f.deleted)
	}
}
