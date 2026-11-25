package agent_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
)

func names(tools []agent.Tool) []string {
	out := []string{}
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

func TestOfferTaintedReadOnly(t *testing.T) {
	reg := agent.NewRegistry(
		agent.Tool{Name: "weather", Effects: agent.Read, Output: agent.Trusted},
		agent.Tool{Name: "web_search", Effects: agent.Read, Output: agent.Untrusted},
		agent.Tool{Name: "device_volume", Effects: agent.Write, Output: agent.Trusted},
		agent.Tool{Name: "unmarked"},
	)
	cases := []struct {
		enabled []string
		tainted bool
		want    []string
	}{
		{nil, false, []string{"weather", "web_search", "device_volume", "unmarked"}},
		{nil, true, []string{"weather", "web_search"}},
		{[]string{"device_volume", "weather", "missing"}, false, []string{"weather", "device_volume"}},
		{[]string{"device_volume"}, true, []string{}},
		{[]string{}, false, []string{}},
	}
	for _, c := range cases {
		if got := names(reg.Offer(c.enabled, c.tainted, cadencev2.Trigger_TRIGGER_WAKE)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Offer(%v, %v) = %v, want %v", c.enabled, c.tainted, got, c.want)
		}
	}
	taints := map[string]bool{"weather": false, "web_search": true, "device_volume": false, "unmarked": true}
	for _, tool := range reg.Offer(nil, false, cadencev2.Trigger_TRIGGER_WAKE) {
		if tool.Taints() != taints[tool.Name] {
			t.Errorf("%s: Taints() = %v", tool.Name, tool.Taints())
		}
	}
}

// an event turn reads task output nobody vouched for: it gets Read tools and
// only the write tools marked for it, tainted or not (M5b-D4)
func TestOfferEventTurn(t *testing.T) {
	reg := agent.NewRegistry(
		agent.Tool{Name: "weather", Effects: agent.Read, Output: agent.Trusted},
		agent.Tool{Name: "device_volume", Effects: agent.Write, Output: agent.Trusted},
		agent.Tool{Name: "task_reply", Effects: agent.Write, Output: agent.Trusted, EventTurns: true},
	)
	event := cadencev2.Trigger_TRIGGER_EVENT
	for _, tainted := range []bool{false, true} {
		if got := names(reg.Offer(nil, tainted, event)); !reflect.DeepEqual(got, []string{"weather", "task_reply"}) {
			t.Errorf("event turn, tainted %v: %v", tainted, got)
		}
	}
	if got := names(reg.Offer([]string{"weather"}, true, event)); !reflect.DeepEqual(got, []string{"weather"}) {
		t.Errorf("the device's tool list still applies: %v", got)
	}
	// a microphone turn's taint still takes every write tool
	if got := names(reg.Offer(nil, true, cadencev2.Trigger_TRIGGER_WAKE)); !reflect.DeepEqual(got, []string{"weather"}) {
		t.Errorf("tainted wake turn: %v", got)
	}
	if got := names(reg.Offer(nil, false, cadencev2.Trigger_TRIGGER_FOLLOW_UP)); len(got) != 3 {
		t.Errorf("follow-up turn: %v", got)
	}
}

// task_reply and task_cancel stay in a tainted turn: their policy, not the
// taint, decides what they may do (Ruling M5b-R4); an event turn still gets
// only the tools marked for it
func TestOfferTaintExempt(t *testing.T) {
	reg := agent.NewRegistry(
		agent.Tool{Name: "weather", Effects: agent.Read, Output: agent.Trusted},
		agent.Tool{Name: "task_start", Effects: agent.Write, Output: agent.Trusted},
		agent.Tool{Name: "task_reply", Effects: agent.Write, Output: agent.Trusted, EventTurns: true, TaintExempt: true},
		agent.Tool{Name: "task_cancel", Effects: agent.Write, Output: agent.Trusted, TaintExempt: true},
	)
	if got := names(reg.Offer(nil, true, cadencev2.Trigger_TRIGGER_WAKE)); !reflect.DeepEqual(got, []string{"weather", "task_reply", "task_cancel"}) {
		t.Errorf("tainted wake turn: %v", got)
	}
	if got := names(reg.Offer(nil, true, cadencev2.Trigger_TRIGGER_EVENT)); !reflect.DeepEqual(got, []string{"weather", "task_reply"}) {
		t.Errorf("event turn: %v", got)
	}
}

// a tool's live description is read at each snapshot, a source's tools beside it
func TestDescribeReadAtEachSnapshot(t *testing.T) {
	desc := "first"
	reg := agent.NewRegistry(
		agent.Tool{Name: "agent_mode", Description: "at start", Describe: func() string { return desc }},
		agent.Tool{Name: "weather", Description: "fixed"},
	)
	descriptions := func() []string {
		var out []string
		for _, tool := range reg.Offer(nil, false, cadencev2.Trigger_TRIGGER_WAKE) {
			out = append(out, tool.Description)
		}
		return out
	}
	if got := descriptions(); !reflect.DeepEqual(got, []string{"first", "fixed"}) {
		t.Fatalf("got %v", got)
	}
	desc = "second"
	if got := descriptions(); !reflect.DeepEqual(got, []string{"second", "fixed"}) {
		t.Fatalf("after a change %v", got)
	}
	reg.AddSource("src", agent.SourceFunc(func() []agent.Tool {
		return []agent.Tool{{Name: "dyn", Description: "dynamic", Input: `{"type":"object"}`, Run: func(context.Context, agent.Call) (json.RawMessage, *agent.ToolError) { return nil, nil }}}
	}))
	desc = "third"
	if got := descriptions(); !reflect.DeepEqual(got, []string{"third", "fixed", "dynamic"}) {
		t.Fatalf("with a source %v", got)
	}
}
