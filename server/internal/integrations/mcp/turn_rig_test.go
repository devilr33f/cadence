package mcp_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

const (
	wake     = cadencev2.Trigger_TRIGGER_WAKE
	followUp = cadencev2.Trigger_TRIGGER_FOLLOW_UP
	event    = cadencev2.Trigger_TRIGGER_EVENT
)

// step answers one llm request of a turn; before runs when the request
// comes, after the turn took its tools.
type step struct {
	text   string
	calls  []providers.ToolCall
	before func()
}

func calling(calls ...string) step {
	var out []providers.ToolCall
	for i := 0; i < len(calls); i += 2 {
		out = append(out, providers.ToolCall{ID: "c-" + calls[i], Name: calls[i], Arguments: json.RawMessage(calls[i+1])})
	}
	return step{calls: out}
}

// script plays the model: each request gets the next step, the last repeats.
type script struct {
	steps []step
	mu    sync.Mutex
	reqs  []providers.LLMRequest
}

func (s *script) LLM(context.Context, string) (providers.LLM, error) { return s, nil }

func (s *script) Stream(_ context.Context, r providers.LLMRequest) (<-chan providers.LLMChunk, error) {
	s.mu.Lock()
	r.Messages = slices.Clone(r.Messages)
	s.reqs = append(s.reqs, r)
	st := s.steps[min(len(s.reqs), len(s.steps))-1]
	s.mu.Unlock()
	if st.before != nil {
		st.before()
	}
	ch := make(chan providers.LLMChunk, 4)
	if st.text != "" {
		ch <- providers.LLMChunk{Text: st.text}
	}
	if len(st.calls) > 0 {
		ch <- providers.LLMChunk{ToolCallStart: true}
		ch <- providers.LLMChunk{ToolCalls: slices.Clone(st.calls)}
	}
	ch <- providers.LLMChunk{Usage: &providers.Usage{PromptTokens: 10, CompletionTokens: 1}}
	close(ch)
	return ch, nil
}

type modes map[string]agent.Mode

func (m modes) Get(_ context.Context, name string) (agent.Mode, error) {
	if mode, ok := m[name]; ok {
		return mode, nil
	}
	return agent.Mode{}, agent.ErrModeNotFound
}

type noHistory struct{}

func (noHistory) History(context.Context, string, int64, int) ([]providers.Message, error) {
	return nil, nil
}
func (noHistory) Append(context.Context, string, string, conversation.TurnMeta, []providers.Message) error {
	return nil
}

// turns runs agent turns over the catalog's tools beside the real
// device_volume and a stand-in ha_control with the same policy (Write,
// Trusted, not taint-exempt).
type turns struct {
	t      *testing.T
	r      *toolRig
	reg    *agent.Registry
	values settings.Values
	mode   agent.Mode
	// volume is every volume command sent; home counts the ha_control runs
	mu     sync.Mutex
	volume []string
	home   atomic.Int32
}

func newTurns(t *testing.T, r *toolRig) *turns {
	h := &turns{t: t, r: r, values: settings.Defaults()}
	h.values.LLM, h.values.Model = "llm-x", "model-x"
	h.mode = agent.Mode{Name: "fast", MaxSteps: 8, MaxDuration: 20 * time.Second, Interim: agent.InterimDrop}
	volume := tools.DeviceVolume(tools.VolumeDeps{
		State: func(context.Context, string, string) (json.RawMessage, bool) { return nil, false },
		Command: func(_ context.Context, _, _, name string, args []byte) (json.RawMessage, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.volume = append(h.volume, name+" "+string(args))
			return json.RawMessage(`{"level":100}`), nil
		},
	})
	home := agent.Tool{
		Name: "ha_control", Description: "Controls the home.", Input: anyObject, Effects: agent.Write, Output: agent.Trusted,
		Run: func(context.Context, agent.Call) (json.RawMessage, *agent.ToolError) {
			h.home.Add(1)
			return json.RawMessage(`{"done":true}`), nil
		},
	}
	h.reg = agent.NewRegistry(volume, home)
	h.reg.AddSource("mcp", r.src)
	return h
}

// run answers one turn of trigger as steps script it and returns its requests.
func (h *turns) run(trigger cadencev2.Trigger, steps ...step) []providers.LLMRequest {
	h.t.Helper()
	llm := &script{steps: steps}
	v := h.values
	runner := agent.NewRunner(agent.RunnerDeps{
		Providers: llm,
		Settings:  func(context.Context, string) (settings.Values, error) { return v, nil },
		Modes:     modes{"fast": h.mode}, ModeState: agent.NewModeState(), Tools: h.reg, Conversations: noHistory{},
		Log: slog.New(slog.NewTextHandler(h.r.logs, nil)),
	})
	out := make(chan agent.Out, 64)
	res := runner.Run(bg(), agent.Turn{DeviceID: "dev-1", SessionID: "s-1", Trigger: trigger, Text: "hello", Language: "en"}, out)
	if res.Code != "" {
		h.t.Fatalf("turn ended %+v", res)
	}
	llm.mu.Lock()
	defer llm.mu.Unlock()
	return slices.Clone(llm.reqs)
}

func (h *turns) volumes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.volume)
}

func offered(req providers.LLMRequest) []string {
	out := []string{}
	if req.ToolChoice == "none" {
		return out
	}
	for _, s := range req.Tools {
		out = append(out, s.Name)
	}
	return out
}

// codeOf is the error code of the call's tool message in req, "" for a result.
func codeOf(t *testing.T, req providers.LLMRequest, callID string) string {
	t.Helper()
	for _, m := range req.Messages {
		if m.Role != "tool" || m.ToolCallID != callID {
			continue
		}
		var res struct {
			OK    bool `json:"ok"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(m.Content), &res); err != nil {
			t.Fatalf("tool message %s", m.Content)
		}
		return res.Error.Code
	}
	t.Fatalf("no tool message for %s", callID)
	return ""
}
