package agent_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func TestConversationResetToolNeedsNoWake(t *testing.T) {
	tool := agent.ConversationResetTool()
	for _, trigger := range []cadencev2.Trigger{
		cadencev2.Trigger_TRIGGER_FOLLOW_UP, cadencev2.Trigger_TRIGGER_EVENT, cadencev2.Trigger_TRIGGER_WAKE,
	} {
		out, err := agent.Run(context.Background(), tool, agent.Call{Trigger: trigger, Args: json.RawMessage(`{}`)})
		if err != nil || string(out) != `{"reset":"next_turn"}` {
			t.Fatalf("%s: %s %+v", trigger, out, err)
		}
	}
	if err := agent.Validate(tool, json.RawMessage(`{"memory":true}`)); err == nil || err.Code != "invalid_arguments" {
		t.Fatalf("extra arguments: %+v", err)
	}
}

func TestConversationResetToolAppliesAfterItsTurnCommits(t *testing.T) {
	for _, trigger := range []cadencev2.Trigger{cadencev2.Trigger_TRIGGER_FOLLOW_UP, cadencev2.Trigger_TRIGGER_EVENT} {
		t.Run(trigger.String(), func(t *testing.T) {
			h := newHarness(t,
				llmStep{calls: []providers.ToolCall{toolCall("r1", agent.ConversationReset, `{}`)}},
				llmStep{text: "Starting over."},
			)
			h.tools = []agent.Tool{agent.ConversationResetTool()}
			// even explicit empty allowlists leave this turn-scoped tool offered
			h.values.Tools = []string{}
			mode := fastMode
			mode.Tools = []string{}
			h.modes["fast"] = mode
			calls := 0
			h.reset = func(_ context.Context, id string) error {
				calls++
				if id != device {
					t.Errorf("reset device %q", id)
				}
				if len(h.convs.stored) != 1 {
					t.Errorf("reset ran before the turn committed")
				}
				return nil
			}
			res, outs := h.run(trigger, "давай с чистого листа")
			if res != (agent.Result{}) || calls != 1 {
				t.Fatalf("result %+v, resets %d", res, calls)
			}
			wantSpeech(t, outs, "Starting over.")
			reqs := h.requests(2)
			if !slices.Contains(toolNames(reqs[0].Tools), agent.ConversationReset) {
				t.Fatalf("tool not offered: %v", toolNames(reqs[0].Tools))
			}
			stored := h.convs.stored[0].msgs
			if len(stored) != 4 || stored[1].ToolCalls[0].Name != agent.ConversationReset || stored[2].Content != `{"ok":true,"data":{"reset":"next_turn"}}` {
				t.Fatalf("stored turn %+v", stored)
			}
		})
	}
}
