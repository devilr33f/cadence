package tools_test

import (
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
)

const (
	twoTimers = `{"alarms":[],"timers":[{"id":"t1","seconds":600,"endsAt":"2026-11-11T09:04:00Z"},` +
		`{"id":"t2","seconds":60,"endsAt":"2026-11-11T08:59:59Z"}],"ringing":null}`
	timerAnswer = `{"timer":{"id":"x","seconds":600,"endsAt":"2026-11-11T09:10:00Z"}}`
)

func timerFake() *alarmsFake {
	return &alarmsFake{state: noAlarms, answer: map[string]string{"set_timer": timerAnswer, "cancel_timer": `{"cancelled":["t1"]}`}}
}

// v1 §6.2: set takes a length, cancel a length or all, list neither
func TestTimerArgs(t *testing.T) {
	for _, c := range []struct{ args, sent string }{
		{`{"action":"set","seconds":1}`, `set_timer {"sessionId":"s1","id":"<id>","seconds":1}`},
		{`{"action":"set","seconds":86400,"all":false}`, `set_timer {"sessionId":"s1","id":"<id>","seconds":86400}`},
		{`{"action":"cancel"}`, `cancel_timer {"sessionId":"s1"}`},
		{`{"action":"cancel","all":false}`, `cancel_timer {"sessionId":"s1"}`},
		{`{"action":"cancel","seconds":600}`, `cancel_timer {"sessionId":"s1","seconds":600}`},
		{`{"action":"cancel","all":true}`, `cancel_timer {"sessionId":"s1","all":true}`},
	} {
		f := timerFake()
		if _, e := run(t, tools.Timer(f.deps()), wake, c.args); e != nil {
			t.Fatalf("%s: %+v", c.args, e)
		}
		f.wantCalls(t, c.sent)
	}
	for _, args := range []string{
		`{"action":"set"}`, `{"action":"set","seconds":0}`, `{"action":"set","seconds":86401}`, `{"action":"set","seconds":1.5}`,
		`{"action":"set","seconds":60,"all":true}`, `{"action":"cancel","seconds":60,"all":true}`,
		`{"action":"list","seconds":60}`, `{"action":"list","all":true}`, `{"action":"pause"}`, `{"seconds":60}`,
		`{"action":"set","seconds":60,"time":"7:00"}`,
		// R7: plain types only, so null is no empty value
		`{"action":"cancel","seconds":null}`, `{"action":"cancel","all":null}`, `{"action":null}`,
	} {
		f := timerFake()
		if _, e := run(t, tools.Timer(f.deps()), wake, args); e == nil || e.Code != "invalid_arguments" {
			t.Fatalf("%s: %+v", args, e)
		}
		f.wantCalls(t)
	}
	f := timerFake()
	if _, e := run(t, tools.Timer(f.deps()), wake, `{"action":"list","all":false}`); e != nil {
		t.Fatalf("list with empty values: %+v", e)
	}
}

// a timer raises nothing: set, cancel and list work in a follow-up, and the device sees the session
func TestTimerInFollowUp(t *testing.T) {
	f := timerFake()
	f.state = twoTimers
	tool := tools.Timer(f.deps())
	for _, args := range []string{`{"action":"set","seconds":60}`, `{"action":"cancel"}`, `{"action":"list"}`} {
		if _, e := run(t, tool, followUp, args); e != nil {
			t.Fatalf("%s: %+v", args, e)
		}
	}
	f.wantCalls(t, `set_timer {"sessionId":"s1","id":"<id>","seconds":60}`, `cancel_timer {"sessionId":"s1"}`)
	f.err = &commands.DeviceError{Code: "wake_required"}
	if _, e := run(t, tool, followUp, `{"action":"set","seconds":60}`); !wakeRefusal(e) {
		t.Fatalf("device refusal: %+v", e)
	}
}

// the model gets lengths and the seconds left, never an id
func TestTimerResults(t *testing.T) {
	f := timerFake()
	tool := tools.Timer(f.deps())
	out, e := run(t, tool, wake, `{"action":"set","seconds":600}`)
	if e != nil || string(out) != `{"timer":{"seconds":600,"remainingS":600}}` {
		t.Fatalf("set: %s %+v", out, e)
	}
	// part of a second left counts as a second
	f.now = alarmsNow.Add(400 * time.Millisecond)
	if out, _ = run(t, tool, wake, `{"action":"set","seconds":600}`); string(out) != `{"timer":{"seconds":600,"remainingS":600}}` {
		t.Fatalf("set late: %s", out)
	}
	f.now = time.Time{}
	for _, bad := range []string{``, `{}`, `{"timer":{"seconds":600}}`, `{"timer":{"seconds":600,"endsAt":"soon"}}`} {
		f.answer["set_timer"] = bad
		if _, e := run(t, tool, wake, `{"action":"set","seconds":600}`); e == nil || e.Code != "timer_failed" || e.Retryable {
			t.Fatalf("answer %q: %+v", bad, e)
		}
	}
	if out, e = run(t, tool, wake, `{"action":"list"}`); e != nil || string(out) != `{"timers":[]}` {
		t.Fatalf("empty list: %s %+v", out, e)
	}
	// one about to ring still has a second to say
	f.state = twoTimers
	out, e = run(t, tool, wake, `{"action":"list"}`)
	if e != nil || string(out) != `{"timers":[{"seconds":600,"remainingS":240},{"seconds":60,"remainingS":1}]}` {
		t.Fatalf("list: %s %+v", out, e)
	}
	// what was cancelled, from the state read before the command
	for _, c := range []struct{ answer, args, want string }{
		{`{"cancelled":["t2"]}`, `{"action":"cancel"}`, `{"cancelled":[{"seconds":60}]}`},
		{`{"cancelled":["t1"]}`, `{"action":"cancel","seconds":600}`, `{"cancelled":[{"seconds":600}]}`},
		{`{"cancelled":["t1","t2"]}`, `{"action":"cancel","all":true}`, `{"cancelled":[{"seconds":600},{"seconds":60}]}`},
		{`{"cancelled":["zz"]}`, `{"action":"cancel","seconds":30}`, `{"cancelled":[{"seconds":30}]}`},
		{`{"cancelled":["t1","zz"]}`, `{"action":"cancel","all":true}`, `{"cancelled":[{"seconds":600},{}]}`},
	} {
		f.answer["cancel_timer"] = c.answer
		if out, e := run(t, tool, wake, c.args); e != nil || string(out) != c.want {
			t.Fatalf("%s: %s %+v", c.args, out, e)
		}
	}
	f.answer["cancel_timer"] = `{"cancelled":"t1"}`
	if _, e := run(t, tool, wake, `{"action":"cancel"}`); e == nil || e.Code != "timer_failed" {
		t.Fatalf("bad cancel answer: %+v", e)
	}
}

func TestTimerMapsDeviceCodes(t *testing.T) {
	for _, c := range []struct {
		err  error
		code string
	}{
		{&commands.DeviceError{Code: "full"}, "full"},
		{&commands.DeviceError{Code: "store"}, "store"},
		{&commands.DeviceError{Code: "not_found"}, "not_found"},
		{&commands.DeviceError{Code: "invalid"}, "invalid_arguments"},
		{commands.ErrOffline, "device_offline"},
		{commands.ErrTimeout, "timeout"},
		{devices.ErrUnknownCapability, "unavailable"},
		{commands.ErrBusy, "timer_failed"},
	} {
		for _, args := range []string{`{"action":"set","seconds":60}`, `{"action":"cancel","all":true}`} {
			f := timerFake()
			f.err = c.err
			_, e := run(t, tools.Timer(f.deps()), wake, args)
			if e == nil || e.Code != c.code || e.Message == "" || e.Retryable || strings.Contains(e.Message, deviceText) {
				t.Fatalf("%v %s: %+v", c.err, args, e)
			}
		}
	}
	for _, args := range []string{`{"action":"set","seconds":60}`, `{"action":"list"}`, `{"action":"cancel"}`} {
		f := timerFake()
		f.state = ""
		if _, e := run(t, tools.Timer(f.deps()), wake, args); e == nil || e.Code != "unavailable" {
			t.Fatalf("no capability %s: %+v", args, e)
		}
		f.wantCalls(t)
	}
}
