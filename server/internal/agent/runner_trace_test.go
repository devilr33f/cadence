package agent_test

import (
	"context"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
	"git.shino.boo/devilreef/cadence/server/internal/tracing/tracingtest"
)

// a turn under the device's voice.turn: its steps, model calls and tools
// are one tree in that trace, with counts and names and none of the text
func TestTurnSpanTree(t *testing.T) {
	p := tracingtest.Setup(t)
	h := newHarness(t,
		llmStep{calls: []providers.ToolCall{toolCall("c1", "weather", `{}`)}},
		llmStep{text: "Sunny today."})
	weather, _ := fakeTool("weather", agent.Read, agent.Trusted, `{"summary":"sunny secret-result"}`, nil)
	h.tools = []agent.Tool{weather}
	res, _ := h.runIn(tracingtest.Remote(context.Background()), cadencev2.Trigger_TRIGGER_BUTTON, "weather secret-input")
	if res.Code != "" {
		t.Fatalf("result %+v", res)
	}
	spans := tracingtest.Wait(t, p, tracingtest.DeviceTrace, "agent.turn", "agent.prepare", "agent.step", "llm.request", "tool.call", "agent.commit")
	turn := tracingtest.Find(t, spans, "agent.turn")
	if turn.ParentID != tracingtest.DeviceSpan || !turn.RemoteParent || turn.Attrs["cadence.steps"] != int64(2) ||
		turn.Attrs[tracing.AttrDeviceID] != device || turn.Attrs["cadence.end_code"] != "ok" {
		t.Fatalf("turn %+v", turn)
	}
	byID := map[string]tracing.SpanView{}
	steps, llms := 0, 0
	for _, s := range spans {
		byID[s.SpanID] = s
		for _, v := range s.Attrs {
			if str, ok := v.(string); ok && (contains(str, "secret-input") || contains(str, "secret-result")) {
				t.Fatalf("span %s carries text: %v", s.Name, s.Attrs)
			}
		}
	}
	for _, s := range spans {
		switch s.Name {
		case "agent.prepare", "agent.step", "agent.commit":
			if s.ParentID != turn.SpanID {
				t.Fatalf("%s under %s", s.Name, byID[s.ParentID].Name)
			}
			if s.Name == "agent.step" {
				steps++
			}
		case "llm.request":
			llms++
			if byID[s.ParentID].Name != "agent.step" || s.Attrs[tracing.AttrProvider] != "llm-instance-x" ||
				s.Attrs["cadence.usage.in"] != int64(100) || s.Attrs["cadence.ttft_ms"] == nil {
				t.Fatalf("llm %+v", s)
			}
		case "tool.call":
			if byID[s.ParentID].Name != "agent.step" || s.Attrs["cadence.tool"] != "weather" ||
				s.Attrs["cadence.tool_class"] != "read" || s.Attrs["cadence.outcome"] != "ok" {
				t.Fatalf("tool %+v", s)
			}
		}
	}
	if steps != 2 || llms != 2 {
		t.Fatalf("steps %d llm calls %d", steps, llms)
	}
	finishes := map[any]bool{}
	for _, s := range spans {
		if s.Name == "llm.request" {
			finishes[s.Attrs["gen_ai.response.finish_reason"]] = true
		}
	}
	if !finishes["tool_calls"] || !finishes["stop"] {
		t.Fatalf("finish reasons %v", finishes)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
