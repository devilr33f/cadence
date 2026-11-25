package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
)

func call(trigger cadencev2.Trigger) agent.Call {
	return agent.Call{DeviceID: "d1", SessionID: "s1", Trigger: trigger, Args: json.RawMessage(`{}`)}
}

const weatherInput = `{
	"type": "object",
	"properties": {
		"days": {"type": "integer", "minimum": 1, "maximum": 3},
		"day": {"enum": ["today", "tomorrow"]}
	},
	"additionalProperties": false
}`

const volumeInput = `{
	"type": "object",
	"properties": {"level": {"type": "integer", "minimum": 0, "maximum": 100}},
	"required": ["level"]
}`

func TestValidateReportsPath(t *testing.T) {
	weather := agent.Tool{Name: "weather", Input: weatherInput}
	volume := agent.Tool{Name: "device_volume", Input: volumeInput}
	reg := agent.NewRegistry(weather, volume)
	if e := agent.Validate(reg.Offer([]string{"weather"}, false, cadencev2.Trigger_TRIGGER_WAKE)[0], json.RawMessage(`{"days":2,"day":"today"}`)); e != nil {
		t.Fatalf("valid arguments refused: %+v", e)
	}
	cases := []struct {
		tool agent.Tool
		args string
		want []string
	}{
		{volume, `{"level":150}`, []string{"invalid arguments at /level:", "maximum", "100"}},
		{volume, `{}`, []string{"invalid arguments: missing property", "level"}},
		{weather, `{"days":7}`, []string{"invalid arguments at /days:", "maximum", "3"}},
		{weather, `{"day":"someday"}`, []string{"invalid arguments at /day:", "today", "tomorrow"}},
		{weather, `{"days":1,"extra":1}`, []string{"invalid arguments:", "extra", "not allowed"}},
		// numbers as the model wrote them, never grouped
		{volume, `{"level":1000}`, []string{"invalid arguments at /level:", "got 1000, want 100"}},
		// providers pass arguments that do not parse (often cut off) on as a JSON string
		{weather, `"{\"days\":"`, []string{malformedArgs}},
		{weather, `"not an object"`, []string{malformedArgs}},
		{weather, `{"days":`, []string{malformedArgs}},
	}
	for _, c := range cases {
		registered := reg.Offer([]string{c.tool.Name}, false, cadencev2.Trigger_TRIGGER_WAKE)[0]
		for _, tl := range []agent.Tool{registered, c.tool} {
			e := agent.Validate(tl, json.RawMessage(c.args))
			if e == nil {
				t.Fatalf("%s accepted", c.args)
			}
			if e.Code != "invalid_arguments" || !e.Retryable || e.Hint == "" {
				t.Fatalf("%s: got %+v", c.args, e)
			}
			for _, w := range c.want {
				if !strings.Contains(e.Message, w) {
					t.Fatalf("%s: message %q lacks %q", c.args, e.Message, w)
				}
			}
			if strings.Contains(e.Message, "1,000") {
				t.Fatalf("%s: message %q groups digits", c.args, e.Message)
			}
		}
	}

	// the real volume tool: its own check says what is wrong, not the schema's oneOf
	deviceVolume := agent.NewRegistry(tools.DeviceVolume(tools.VolumeDeps{
		State: func(context.Context, string, string) (json.RawMessage, bool) {
			return json.RawMessage(`{"level":40}`), true
		},
		Command: func(context.Context, string, string, string, []byte) (json.RawMessage, error) {
			t.Error("volume set on invalid arguments")
			return nil, nil
		},
	})).Offer(nil, false, cadencev2.Trigger_TRIGGER_WAKE)[0]
	for _, args := range []string{`{"level":10,"change":-5}`, `{}`} {
		e := agent.Validate(deviceVolume, json.RawMessage(args))
		if e == nil {
			c := call(cadencev2.Trigger_TRIGGER_WAKE)
			c.Args = json.RawMessage(args)
			_, e = agent.Run(context.Background(), deviceVolume, c)
		}
		if e == nil || e.Code != "invalid_arguments" || e.Message != "Give exactly one of level and change." {
			t.Fatalf("%s: got %+v", args, e)
		}
	}
}

const malformedArgs = "The arguments were not a valid JSON object (maybe cut off); send one JSON object."

func TestNewRegistryRejectsBadTools(t *testing.T) {
	bad := map[string][]agent.Tool{
		"invalid schema": {{Name: "a", Input: `{"type":"objekt"}`}},
		"twice":          {{Name: "a"}, {Name: "a"}},
		"no name":        {{}},
	}
	for name, tools := range bad {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: registered", name)
				}
			}()
			agent.NewRegistry(tools...)
		}()
	}
}

func TestRunTimeout(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	ignores := agent.Tool{Name: "stuck", Timeout: 20 * time.Millisecond,
		Run: func(context.Context, agent.Call) (json.RawMessage, *agent.ToolError) {
			<-release
			return json.RawMessage(`{}`), nil
		}}
	start := time.Now()
	out, e := agent.Run(context.Background(), ignores, call(cadencev2.Trigger_TRIGGER_WAKE))
	if out != nil || e == nil || e.Code != "timeout" || !e.Retryable {
		t.Fatalf("got %s %+v", out, e)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("a tool that ignores its context held Run for %v", d)
	}

	var deadline atomic.Bool
	respects := agent.Tool{Name: "slow", Timeout: 20 * time.Millisecond,
		Run: func(ctx context.Context, _ agent.Call) (json.RawMessage, *agent.ToolError) {
			_, ok := ctx.Deadline()
			deadline.Store(ok)
			<-ctx.Done()
			return nil, &agent.ToolError{Code: "upstream_failed"}
		}}
	if _, e := agent.Run(context.Background(), respects, call(cadencev2.Trigger_TRIGGER_WAKE)); e == nil || e.Code != "timeout" {
		t.Fatalf("an error after the deadline: got %+v", e)
	}
	if !deadline.Load() {
		t.Fatal("the tool's context has no deadline")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := agent.Run(ctx, respects, call(cadencev2.Trigger_TRIGGER_WAKE)); e == nil || e.Code != "canceled" || e.Retryable {
		t.Fatalf("a cancelled turn: got %+v", e)
	}
}

func TestRunRecoversPanic(t *testing.T) {
	for _, tool := range []agent.Tool{
		{Name: "boom", Run: func(context.Context, agent.Call) (json.RawMessage, *agent.ToolError) {
			panic("secret-panic-value")
		}},
		{Name: "no run"},
	} {
		out, e := agent.Run(context.Background(), tool, call(cadencev2.Trigger_TRIGGER_WAKE))
		if out != nil || e == nil || e.Code != "internal" {
			t.Fatalf("%s: got %s %+v", tool.Name, out, e)
		}
		if strings.Contains(e.Message+e.Hint, "secret-panic-value") {
			t.Fatalf("%s: error carries the panic value", tool.Name)
		}
	}
}

func TestWakeRequired(t *testing.T) {
	var runs atomic.Int32
	run := func(context.Context, agent.Call) (json.RawMessage, *agent.ToolError) {
		runs.Add(1)
		return json.RawMessage(`{"level":50}`), nil
	}
	guarded := agent.Tool{Name: "raise", Effects: agent.Write, RequiresWokenSession: true, Run: run}
	for _, tr := range []cadencev2.Trigger{cadencev2.Trigger_TRIGGER_FOLLOW_UP, cadencev2.Trigger_TRIGGER_UNSPECIFIED} {
		out, e := agent.Run(context.Background(), guarded, call(tr))
		if out != nil || e == nil || e.Code != "wake_required" || e.Retryable {
			t.Fatalf("%v: got %s %+v", tr, out, e)
		}
	}
	if n := runs.Load(); n != 0 {
		t.Fatalf("the tool ran %d times outside a woken session", n)
	}
	for _, tr := range []cadencev2.Trigger{cadencev2.Trigger_TRIGGER_WAKE, cadencev2.Trigger_TRIGGER_BUTTON} {
		out, e := agent.Run(context.Background(), guarded, call(tr))
		if e != nil || string(out) != `{"level":50}` {
			t.Fatalf("%v: got %s %+v", tr, out, e)
		}
	}
	open := agent.Tool{Name: "lower", Effects: agent.Write, Run: run}
	if _, e := agent.Run(context.Background(), open, call(cadencev2.Trigger_TRIGGER_FOLLOW_UP)); e != nil {
		t.Fatalf("an unguarded tool refused in a follow-up: %+v", e)
	}
}

func TestEventTurnIsNeverWoken(t *testing.T) {
	if agent.Woken(cadencev2.Trigger_TRIGGER_EVENT) {
		t.Fatal("an event turn counts as woken")
	}
	guarded := agent.Tool{Name: "raise", Effects: agent.Write, RequiresWokenSession: true,
		Run: func(context.Context, agent.Call) (json.RawMessage, *agent.ToolError) {
			t.Fatal("the tool ran in an event turn")
			return nil, nil
		}}
	if _, e := agent.Run(context.Background(), guarded, call(cadencev2.Trigger_TRIGGER_EVENT)); e == nil || e.Code != "wake_required" {
		t.Fatalf("got %+v", e)
	}
}
