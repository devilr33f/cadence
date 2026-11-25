package tools_test

import (
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
)

// owner 2026-11-16: a snooze needs no woken session, as it only puts off a ring set in one; the device finds the ring
// by kind, and the model hears the minutes, never an id or a wall time
func TestSnoozeAlarmAndTimer(t *testing.T) {
	f := &alarmsFake{state: oneAlarm, answer: map[string]string{
		"snooze": `{"snoozed":{"kind":"alarm","id":"a1","until":"2026-11-11T09:05:00Z"}}`,
	}}
	out, e := run(t, tools.Alarm(f.deps()), followUp, `{"action":"snooze","minutes":5}`)
	if e != nil || string(out) != `{"snoozed":{"kind":"alarm","minutes":5}}` {
		t.Fatalf("alarm snooze in a follow-up = %s, %+v", out, e)
	}
	f.wantCalls(t, `snooze {"sessionId":"s1","kind":"alarm","minutes":5}`)
	if _, e := run(t, tools.Alarm(f.deps()), wake, `{"action":"snooze"}`); e != nil {
		t.Fatalf("snooze by the default: %+v", e)
	}
	f.wantCalls(t, `snooze {"sessionId":"s1","kind":"alarm"}`)

	f.answer["snooze"] = `{"snoozed":{"kind":"timer","id":"t1","until":"2026-11-11T09:10:00Z"}}`
	out, e = run(t, tools.Timer(f.deps()), followUp, `{"action":"snooze","minutes":10}`)
	if e != nil || string(out) != `{"snoozed":{"kind":"timer","minutes":10}}` {
		t.Fatalf("timer snooze = %s, %+v", out, e)
	}
	f.wantCalls(t, `snooze {"sessionId":"s1","kind":"timer","minutes":10}`)

	f.err = &commands.DeviceError{Code: "not_found"}
	if _, e := run(t, tools.Alarm(f.deps()), wake, `{"action":"snooze","minutes":5}`); e == nil || e.Code != "not_found" ||
		!strings.Contains(e.Message, "10 minutes") {
		t.Fatalf("nothing to snooze: %+v", e)
	}
}

// P2: the tools tell the model that more time after a ring is a snooze, never a cancel or a new alarm
func TestSnoozeIsInTheDescriptions(t *testing.T) {
	for _, d := range []string{tools.Alarm(tools.AlarmDeps{}).Description, tools.Timer(tools.AlarmDeps{}).Description} {
		if !strings.Contains(d, "ещё 5 минут") || !strings.Contains(d, "action snooze") || !strings.Contains(d, "Never cancel") {
			t.Errorf("description: %s", d)
		}
	}
}
