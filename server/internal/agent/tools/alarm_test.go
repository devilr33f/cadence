package tools_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
)

var alarmsNow = time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

// alarmsFake is the server's copy of the alarms state and a device that answers each command.
type alarmsFake struct {
	// the state the server holds; "" is none
	state string
	// each command as "name args", ids as <id>
	calls []string
	ids   []string
	// the device's answer: err, else answer[name]
	err    error
	answer map[string]string
	logs   bytes.Buffer
	// the server's clock; zero is alarmsNow
	now time.Time
}

var sentID = regexp.MustCompile(`"id":"([^"]*)"`)

func (f *alarmsFake) deps() tools.AlarmDeps {
	return tools.AlarmDeps{
		State: func(_ context.Context, deviceID, capability string) (json.RawMessage, bool) {
			if deviceID != "d1" || capability != "alarms" {
				panic("wrong device or capability")
			}
			return json.RawMessage(f.state), f.state != ""
		},
		Command: func(_ context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error) {
			if deviceID != "d1" || capability != "alarms" {
				panic("wrong device or capability")
			}
			if m := sentID.FindSubmatch(args); m != nil {
				f.ids = append(f.ids, string(m[1]))
			}
			f.calls = append(f.calls, name+" "+sentID.ReplaceAllString(string(args), `"id":"<id>"`))
			if f.err != nil {
				return nil, f.err
			}
			return json.RawMessage(f.answer[name]), nil
		},
		Now: func() time.Time {
			if f.now.IsZero() {
				return alarmsNow
			}
			return f.now
		},
		Log: slog.New(slog.NewTextHandler(&f.logs, &slog.HandlerOptions{ReplaceAttr: dropTime})),
	}
}

func dropTime(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return a
}

// wakeRefusal is the agent's own rule-7 refusal, word for word
func wakeRefusal(e *agent.ToolError) bool {
	w := agent.WakeRequired()
	return e != nil && e.Code == w.Code && e.Message == w.Message && e.Hint == w.Hint && !e.Retryable
}

func (f *alarmsFake) wantCalls(t *testing.T, want ...string) {
	t.Helper()
	if !slices.Equal(f.calls, want) && (len(f.calls) != 0 || len(want) != 0) {
		t.Fatalf("calls %q, want %q", f.calls, want)
	}
	f.calls = nil
}

const (
	noAlarms  = `{"alarms":[],"timers":[],"ringing":null}`
	oneAlarm  = `{"alarms":[{"id":"a1","time":"07:00","days":["mon","fri"],"level":40}],"timers":[],"ringing":null}`
	twoAlarms = `{"alarms":[{"id":"a1","time":"07:00","days":["mon","fri"],"level":40},` +
		`{"id":"a2","time":"08:30","date":"2026-11-12","level":50},{"id":"a3","time":"07:00","days":["sat"],"level":50}],` +
		`"timers":[],"ringing":null}`
	setAnswer = `{"alarm":{"id":"x","time":"07:00","days":["mon","tue","wed","thu","fri"],"level":50},"replaced":true}`
)

func setFake() *alarmsFake {
	return &alarmsFake{state: noAlarms, answer: map[string]string{"set_alarm": setAnswer}}
}

// v1 §6.1: what each action takes, and how it reaches the device
func TestAlarmArgs(t *testing.T) {
	for _, c := range []struct{ args, sent string }{
		{`{"action":"set","time":"7:00"}`, `{"sessionId":"s1","id":"<id>","time":"07:00"}`},
		{`{"action":"set","time":"07:05"}`, `{"sessionId":"s1","id":"<id>","time":"07:05"}`},
		{`{"action":"set","time":"0:00"}`, `{"sessionId":"s1","id":"<id>","time":"00:00"}`},
		{`{"action":"set","time":"23:59","days":["daily"]}`,
			`{"sessionId":"s1","id":"<id>","time":"23:59","days":["mon","tue","wed","thu","fri","sat","sun"]}`},
		{`{"action":"set","time":"7:00","days":["weekdays"]}`,
			`{"sessionId":"s1","id":"<id>","time":"07:00","days":["mon","tue","wed","thu","fri"]}`},
		{`{"action":"set","time":"7:00","days":["weekends"]}`, `{"sessionId":"s1","id":"<id>","time":"07:00","days":["sat","sun"]}`},
		{`{"action":"set","time":"7:00","days":["weekends","mon","sat"]}`,
			`{"sessionId":"s1","id":"<id>","time":"07:00","days":["mon","sat","sun"]}`},
		{`{"action":"set","time":"7:00","days":["sun","fri","mon","fri"]}`,
			`{"sessionId":"s1","id":"<id>","time":"07:00","days":["mon","fri","sun"]}`},
		{`{"action":"set","time":"7:00","date":"2026-11-12","days":[]}`,
			`{"sessionId":"s1","id":"<id>","time":"07:00","date":"2026-11-12"}`},
		{`{"action":"set","time":"7:00","level":1}`, `{"sessionId":"s1","id":"<id>","time":"07:00","level":1}`},
		{`{"action":"set","time":"7:00","level":100}`, `{"sessionId":"s1","id":"<id>","time":"07:00","level":100}`},
		// a gentler wake-up only when asked
		{`{"action":"set","time":"7:00","rampSeconds":300,"rampFrom":"quiet"}`,
			`{"sessionId":"s1","id":"<id>","time":"07:00","rampSeconds":300,"rampFrom":"quiet"}`},
		{`{"action":"set","time":"7:00","rampSeconds":0}`, `{"sessionId":"s1","id":"<id>","time":"07:00","rampSeconds":0}`},
		// the empty values of the plain types are absent, wherever they are (R7)
		{`{"action":"set","time":"7:00","all":false,"days":[]}`, `{"sessionId":"s1","id":"<id>","time":"07:00"}`},
		{`{"action":"cancel","time":"7:00","days":[],"all":false}`, `{"sessionId":"s1","time":"07:00"}`},
	} {
		f := setFake()
		f.answer["cancel_alarm"] = `{"cancelled":["a1"]}`
		if _, e := run(t, tools.Alarm(f.deps()), wake, c.args); e != nil {
			t.Fatalf("%s: %+v", c.args, e)
		}
		name := "set_alarm "
		if strings.Contains(c.args, "cancel") {
			name = "cancel_alarm "
		}
		f.wantCalls(t, name+c.sent)
	}
	for _, args := range []string{
		`{"action":"set","time":"24:00"}`, `{"action":"set","time":"7:5"}`, `{"action":"set","time":"007:00"}`,
		`{"action":"set","time":"7:00","level":0}`, `{"action":"set","time":"7:00","level":101}`,
		`{"action":"set"}`, `{"action":"set","days":["daily"]}`, `{"action":"set","time":""}`,
		`{"action":"set","time":"7:00","date":"2026-11-12","days":["mon"]}`,
		`{"action":"set","time":"7:00","date":"2026-02-30"}`, `{"action":"set","time":"7:00","date":"2026-1-01"}`,
		`{"action":"set","time":"7:00","days":["monday"]}`, `{"action":"set","time":"7:00","days":["often"]}`,
		`{"action":"set","time":"7:00","days":"daily"}`,
		`{"action":"set","time":"7:00","days":["mon","tue","wed","thu","fri","sat","sun","mon"]}`,
		// a field that does not go with the action is a mistake, not something to drop
		`{"action":"set","time":"7:00","all":true}`,
		`{"action":"cancel","time":"7:00","days":["mon"]}`, `{"action":"cancel","level":50}`,
		`{"action":"cancel","date":"2026-11-12"}`, `{"action":"cancel","all":true,"time":"7:00"}`,
		`{"action":"list","time":"7:00"}`, `{"action":"list","all":true}`,
		`{"action":"nap"}`, `{}`, `{"action":"list","label":"x"}`, `[]`,
		`{"action":"snooze","minutes":0}`, `{"action":"snooze","minutes":61}`, `{"action":"snooze","time":"7:00"}`,
		`{"action":"set","time":"7:00","rampSeconds":1801}`, `{"action":"set","time":"7:00","rampFrom":"loud"}`,
		`{"action":"set","time":"7:00","minutes":5}`, `{"action":"cancel","rampSeconds":60}`,
		// R7: no null and no "" stand for an absent field
		`{"action":"set","time":"7:00","level":null}`, `{"action":"set","time":"7:00","date":null}`,
		`{"action":"set","time":null}`, `{"action":"cancel","all":null}`, `{"action":"set","time":"7:00","days":null}`,
		`{"action":"set","time":"7:00","date":""}`, `{"action":"set","time":"7:00","days":""}`,
		`{"action":"cancel","all":true,"time":""}`,
	} {
		f := setFake()
		if _, e := run(t, tools.Alarm(f.deps()), wake, args); e == nil || e.Code != "invalid_arguments" {
			t.Fatalf("%s: %+v", args, e)
		}
		f.wantCalls(t)
	}
	f := setFake()
	if _, e := run(t, tools.Alarm(f.deps()), wake, `{"action":"list","days":[],"all":false}`); e != nil {
		t.Fatalf("list with empty values: %+v", e)
	}
	// the runner validates first; a call that skipped it is refused a null all the same
	for _, args := range []string{`{"action":"set","time":"7:00","level":null}`, `{"action":"list","time":""}`} {
		reg := agent.NewRegistry(tools.Alarm(f.deps()))
		c := agent.Call{DeviceID: "d1", SessionID: "s1", Trigger: wake, Args: json.RawMessage(args)}
		if _, e := agent.Run(context.Background(), reg.Offer(nil, false, wake)[0], c); e == nil || e.Code != "invalid_arguments" {
			t.Fatalf("unvalidated %s: %+v", args, e)
		}
	}
	f.wantCalls(t)
}

// R7: a provider may refuse a whole tool list over one union, and both tools are offered every turn, so their inputs
// use plain types alone: no anyOf, oneOf or allOf, and each type one name
func TestAlarmSchemasArePlain(t *testing.T) {
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch n := v.(type) {
		case map[string]any:
			for k, child := range n {
				switch k {
				case "anyOf", "oneOf", "allOf":
					t.Errorf("%s: %s", path, k)
				case "type":
					if _, ok := child.(string); !ok {
						t.Errorf("%s: type %v", path, child)
					}
				}
				walk(path+"/"+k, child)
			}
		case []any:
			for _, child := range n {
				walk(path, child)
			}
		}
	}
	f := setFake()
	for _, tool := range []agent.Tool{tools.Alarm(f.deps()), tools.Timer(f.deps())} {
		var schema any
		if err := json.Unmarshal([]byte(tool.Input), &schema); err != nil {
			t.Fatal(err)
		}
		walk(tool.Name, schema)
	}
}

// ids are new for each set and the device's own pattern
func TestAlarmIDs(t *testing.T) {
	f := setFake()
	f.state = twoAlarms
	tool := tools.Alarm(f.deps())
	for range 20 {
		if _, e := run(t, tool, wake, `{"action":"set","time":"6:00"}`); e != nil {
			t.Fatal(e)
		}
	}
	valid := regexp.MustCompile(`^[a-z0-9]{8}$`)
	for i, id := range f.ids {
		if !valid.MatchString(id) || slices.Contains(f.ids[:i], id) {
			t.Fatalf("id %q in %q", id, f.ids)
		}
	}
}

// rule 7: an alarm raises the volume when it rings, so setting one needs a session the user began; cancel and list
// do not, and the device judges the session again
func TestAlarmSetNeedsWake(t *testing.T) {
	f := setFake()
	f.state = oneAlarm
	f.answer["cancel_alarm"] = `{"cancelled":["a1"]}`
	tool := tools.Alarm(f.deps())
	for _, trig := range []cadencev2.Trigger{followUp, event, cadencev2.Trigger_TRIGGER_UNSPECIFIED} {
		_, e := run(t, tool, trig, `{"action":"set","time":"7:00"}`)
		if !wakeRefusal(e) {
			t.Fatalf("%v: %+v", trig, e)
		}
	}
	f.wantCalls(t)
	for _, trig := range []cadencev2.Trigger{wake, button} {
		if _, e := run(t, tool, trig, `{"action":"set","time":"7:00"}`); e != nil {
			t.Fatalf("%v: %+v", trig, e)
		}
	}
	f.wantCalls(t, `set_alarm {"sessionId":"s1","id":"<id>","time":"07:00"}`, `set_alarm {"sessionId":"s1","id":"<id>","time":"07:00"}`)
	if _, e := run(t, tool, followUp, `{"action":"cancel","time":"7:00"}`); e != nil {
		t.Fatalf("cancel in a follow-up: %+v", e)
	}
	if _, e := run(t, tool, followUp, `{"action":"list"}`); e != nil {
		t.Fatalf("list in a follow-up: %+v", e)
	}
	f.wantCalls(t, `cancel_alarm {"sessionId":"s1","time":"07:00"}`)
	// the device refuses a session it did not see begin with a wake or press
	for _, code := range []string{"follow_up", "wake_required"} {
		f.err = &commands.DeviceError{Code: code}
		_, e := run(t, tool, wake, `{"action":"set","time":"7:00"}`)
		if !wakeRefusal(e) {
			t.Fatalf("%s: %+v", code, e)
		}
	}
}

// a turn with no session has nothing the device could check, so nothing is sent
func TestAlarmWithoutSessionSendsNone(t *testing.T) {
	f := setFake()
	f.state = oneAlarm
	reg := agent.NewRegistry(tools.Alarm(f.deps()), tools.Timer(f.deps()))
	for i, args := range []string{`{"action":"set","time":"7:00"}`, `{"action":"cancel","all":true}`, `{"action":"cancel"}`} {
		c := agent.Call{DeviceID: "d1", Trigger: wake, Args: json.RawMessage(args)}
		_, e := agent.Run(context.Background(), reg.Offer(nil, false, wake)[0], c)
		if !wakeRefusal(e) {
			t.Fatalf("%d: %+v", i, e)
		}
	}
	c := agent.Call{DeviceID: "d1", Trigger: wake, Args: json.RawMessage(`{"action":"set","seconds":60}`)}
	if _, e := agent.Run(context.Background(), reg.Offer(nil, false, wake)[1], c); e == nil || e.Code != "wake_required" {
		t.Fatalf("timer: %+v", e)
	}
	f.wantCalls(t)
}

// cancel with neither time nor all takes the only alarm, or asks which
func TestAlarmCancelPlans(t *testing.T) {
	f := &alarmsFake{state: noAlarms, answer: map[string]string{"cancel_alarm": `{"cancelled":["a1"]}`}}
	tool := tools.Alarm(f.deps())
	if _, e := run(t, tool, wake, `{"action":"cancel"}`); e == nil || e.Code != "not_found" || e.Message == "" {
		t.Fatalf("none: %+v", e)
	}
	f.wantCalls(t)
	f.state = oneAlarm
	out, e := run(t, tool, followUp, `{"action":"cancel","all":false}`)
	if e != nil || string(out) != `{"cancelled":[{"time":"07:00","days":["mon","fri"]}]}` {
		t.Fatalf("one: %s %+v", out, e)
	}
	f.wantCalls(t, `cancel_alarm {"sessionId":"s1","id":"<id>"}`)
	if f.ids[0] != "a1" {
		t.Fatalf("cancelled %q", f.ids)
	}
	f.state = twoAlarms
	_, e = run(t, tool, wake, `{"action":"cancel"}`)
	if e == nil || e.Code != "ambiguous" || e.Retryable || !slices.Equal(e.Candidates, []string{"07:00", "08:30"}) || e.Hint == "" {
		t.Fatalf("several: %+v", e)
	}
	f.wantCalls(t)
	// several at one time leave nothing to ask: every alarm at that time goes
	f.state = `{"alarms":[{"id":"a1","time":"07:00","days":["mon"],"level":50},{"id":"a3","time":"07:00","days":["sat"],"level":50}],` +
		`"timers":[],"ringing":null}`
	f.answer["cancel_alarm"] = `{"cancelled":["a1","a3"]}`
	out, e = run(t, tool, wake, `{"action":"cancel"}`)
	if e != nil || string(out) != `{"cancelled":[{"time":"07:00","days":["mon"]},{"time":"07:00","days":["sat"]}]}` {
		t.Fatalf("one time: %s %+v", out, e)
	}
	f.wantCalls(t, `cancel_alarm {"sessionId":"s1","time":"07:00"}`)
	f.state = twoAlarms
	// by time: every alarm at that time, whatever its days; the answer from the state read before the command
	f.answer["cancel_alarm"] = `{"cancelled":["a1","a3"]}`
	out, e = run(t, tool, wake, `{"action":"cancel","time":"7:00"}`)
	if e != nil || string(out) != `{"cancelled":[{"time":"07:00","days":["mon","fri"]},{"time":"07:00","days":["sat"]}]}` {
		t.Fatalf("by time: %s %+v", out, e)
	}
	f.wantCalls(t, `cancel_alarm {"sessionId":"s1","time":"07:00"}`)
	f.answer["cancel_alarm"] = `{"cancelled":["a1","a2","a3"]}`
	out, e = run(t, tool, wake, `{"action":"cancel","all":true}`)
	if e != nil || string(out) != `{"cancelled":[{"time":"07:00","days":["mon","fri"]},{"time":"08:30","date":"2026-11-12"},`+
		`{"time":"07:00","days":["sat"]}]}` {
		t.Fatalf("all: %s %+v", out, e)
	}
	f.wantCalls(t, `cancel_alarm {"sessionId":"s1","all":true}`)
	// one the server's copy did not hold yet is still counted, with what the request named
	f.state = oneAlarm
	f.answer["cancel_alarm"] = `{"cancelled":["a1","zz"]}`
	out, e = run(t, tool, wake, `{"action":"cancel","time":"07:00"}`)
	if e != nil || string(out) != `{"cancelled":[{"time":"07:00","days":["mon","fri"]},{"time":"07:00"}]}` {
		t.Fatalf("unseen by time: %s %+v", out, e)
	}
	out, e = run(t, tool, wake, `{"action":"cancel","all":true}`)
	if e != nil || string(out) != `{"cancelled":[{"time":"07:00","days":["mon","fri"]},{}]}` {
		t.Fatalf("unseen all: %s %+v", out, e)
	}
}

// the model gets data: what the device set or keeps, never an id
func TestAlarmResults(t *testing.T) {
	f := setFake()
	tool := tools.Alarm(f.deps())
	out, e := run(t, tool, wake, `{"action":"set","time":"7:00","days":["weekdays"]}`)
	if e != nil || string(out) != `{"alarm":{"time":"07:00","days":["mon","tue","wed","thu","fri"],"level":50},"replaced":true}` {
		t.Fatalf("set: %s %+v", out, e)
	}
	f.answer["set_alarm"] = `{"alarm":{"id":"x","time":"08:30","date":"2026-11-12","level":70},"replaced":false}`
	out, e = run(t, tool, wake, `{"action":"set","time":"8:30","date":"2026-11-12","level":70}`)
	if e != nil || string(out) != `{"alarm":{"time":"08:30","date":"2026-11-12","level":70},"replaced":false}` {
		t.Fatalf("set dated: %s %+v", out, e)
	}
	for _, bad := range []string{``, `null`, `{}`, `{"alarm":{"time":7}}`} {
		f.answer["set_alarm"] = bad
		if _, e := run(t, tool, wake, `{"action":"set","time":"7:00"}`); e == nil || e.Code != "alarm_failed" || e.Retryable {
			t.Fatalf("answer %q: %+v", bad, e)
		}
	}
	if out, e = run(t, tool, wake, `{"action":"list"}`); e != nil || string(out) != `{"alarms":[]}` {
		t.Fatalf("empty list: %s %+v", out, e)
	}
	f.state = twoAlarms
	out, e = run(t, tool, wake, `{"action":"list"}`)
	if e != nil || string(out) != `{"alarms":[{"time":"07:00","days":["mon","fri"],"level":40},`+
		`{"time":"08:30","date":"2026-11-12","level":50},{"time":"07:00","days":["sat"],"level":50}]}` {
		t.Fatalf("list: %s %+v", out, e)
	}
	f.wantCalls(t, `set_alarm {"sessionId":"s1","id":"<id>","time":"07:00","days":["mon","tue","wed","thu","fri"]}`,
		`set_alarm {"sessionId":"s1","id":"<id>","time":"08:30","date":"2026-11-12","level":70}`,
		`set_alarm {"sessionId":"s1","id":"<id>","time":"07:00"}`, `set_alarm {"sessionId":"s1","id":"<id>","time":"07:00"}`,
		`set_alarm {"sessionId":"s1","id":"<id>","time":"07:00"}`, `set_alarm {"sessionId":"s1","id":"<id>","time":"07:00"}`)
}

// R5: a device without the alarms capability, or one that reported no state, has none; nothing is sent
func TestAlarmWithoutCapability(t *testing.T) {
	for _, state := range []string{"", `{"alarms":"x"}`} {
		f := setFake()
		f.state = state
		for _, args := range []string{`{"action":"set","time":"7:00"}`, `{"action":"cancel"}`, `{"action":"list"}`, `{"action":"set"}`} {
			if _, e := run(t, tools.Alarm(f.deps()), wake, args); e == nil || e.Code != "unavailable" || e.Message == "" {
				t.Fatalf("%q %s: %+v", state, args, e)
			}
		}
		f.wantCalls(t)
	}
}

const deviceText = "upstream words"

// the device's codes as the model's, never its text; nothing that may have landed is offered for a retry
func TestAlarmMapsDeviceCodes(t *testing.T) {
	for _, c := range []struct {
		err  error
		code string
	}{
		{&commands.DeviceError{Code: "full"}, "full"},
		{&commands.DeviceError{Code: "past"}, "past"},
		{&commands.DeviceError{Code: "store"}, "store"},
		{&commands.DeviceError{Code: "not_found"}, "not_found"},
		{&commands.DeviceError{Code: "unavailable"}, "unavailable"},
		{&commands.DeviceError{Code: "invalid"}, "invalid_arguments"},
		{&commands.InvalidArgumentsError{Path: "/date"}, "invalid_arguments"},
		{&commands.DeviceError{Code: "failed"}, "alarm_failed"},
		{commands.ErrOffline, "device_offline"},
		{commands.ErrTimeout, "timeout"},
		{commands.ErrBusy, "alarm_failed"},
		{devices.ErrUnknownCapability, "unavailable"},
		{devices.ErrUnknownCommand, "unavailable"},
		{devices.ErrNotFound, "unavailable"},
		{errors.New(deviceText), "alarm_failed"},
	} {
		for _, args := range []string{`{"action":"set","time":"7:00"}`, `{"action":"cancel","time":"7:00"}`} {
			f := setFake()
			f.err = c.err
			_, e := run(t, tools.Alarm(f.deps()), wake, args)
			if e == nil || e.Code != c.code || e.Message == "" || e.Retryable || strings.Contains(e.Message, deviceText) {
				t.Fatalf("%v %s: %+v", c.err, args, e)
			}
		}
	}
	// a set before the device knows its zone says why, so the user hears what to do
	f := setFake()
	f.err = &commands.DeviceError{Code: "unavailable"}
	if _, e := run(t, tools.Alarm(f.deps()), wake, `{"action":"set","time":"7:00"}`); !strings.Contains(e.Message, "time zone") {
		t.Fatalf("zone: %+v", e)
	}
}

func TestAlarmToolPolicy(t *testing.T) {
	f := setFake()
	for _, tool := range []agent.Tool{tools.Alarm(f.deps()), tools.Timer(f.deps())} {
		if tool.Effects != agent.Write || tool.Output != agent.Trusted || tool.SilentOnSuccess || tool.EventTurns ||
			tool.TaintExempt || tool.RequiresWokenSession || tool.Available != nil {
			t.Fatalf("%s policy %+v", tool.Name, tool)
		}
		// past the commands service's own 10 s, so its timeout is the one that answers
		if tool.Timeout != 12*time.Second {
			t.Fatalf("%s timeout %v", tool.Name, tool.Timeout)
		}
	}
	reg := agent.NewRegistry(tools.Alarm(f.deps()), tools.Timer(f.deps()))
	for _, c := range []struct {
		tainted bool
		trig    cadencev2.Trigger
		n       int
	}{{false, wake, 2}, {false, followUp, 2}, {true, wake, 0}, {false, event, 0}, {true, event, 0}} {
		if got := reg.Offer(nil, c.tainted, c.trig); len(got) != c.n {
			t.Fatalf("tainted %v %v: %d tools", c.tainted, c.trig, len(got))
		}
	}
}

// rule 8: one line per call with its action and code; never a time, a length or an id
func TestAlarmLogsCodesOnly(t *testing.T) {
	f := setFake()
	f.state = twoAlarms
	f.answer["cancel_alarm"] = `{"cancelled":["a1","a3"]}`
	tool := tools.Alarm(f.deps())
	run(t, tool, wake, `{"action":"set","time":"6:45","level":77}`)
	run(t, tool, wake, `{"action":"cancel","time":"7:00"}`)
	run(t, tool, followUp, `{"action":"set","time":"6:45"}`)
	run(t, tools.Timer(f.deps()), wake, `{"action":"list"}`)
	logs := f.logs.String()
	for _, want := range []string{"msg=alarm_tool device_id=d1 session_id=s1 action=set code=ok",
		"action=cancel code=ok", "action=set code=wake_required", "msg=timer_tool device_id=d1 session_id=s1 action=list code=ok"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs lack %q:\n%s", want, logs)
		}
	}
	for _, leak := range []string{"06:45", "6:45", "07:00", "77", "a1", "a3", "mon"} {
		if strings.Contains(logs, leak) {
			t.Fatalf("logs carry %q:\n%s", leak, logs)
		}
	}
}
