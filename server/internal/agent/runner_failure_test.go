package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

type errorLLM struct{ err error }

func (l errorLLM) Stream(context.Context, providers.LLMRequest) (<-chan providers.LLMChunk, error) {
	ch := make(chan providers.LLMChunk, 1)
	ch <- providers.LLMChunk{Err: l.err}
	close(ch)
	return ch, nil
}

func TestTurnFailureOutcome(t *testing.T) {
	t.Run("first-chunk timeout", func(t *testing.T) {
		h := newHarness(t, llmStep{block: true})
		h.limits = []time.Duration{40 * time.Millisecond, 8 * time.Second, 8 * time.Second}
		res, _ := h.run(wake, "hello")
		f := res.Failure
		if res.Code != agent.CodeTimeout || f == nil || f.Stage != conversation.StageLLM || f.Step != 1 ||
			f.Provider != "llm-instance-x" || f.Model != "model-fast-x" || f.LimitMs != 40 {
			t.Fatalf("failure %+v, result %+v", f, res)
		}
		if got := h.convs.stored[0].meta.Failure; !reflect.DeepEqual(got, f) {
			t.Fatalf("stored %+v, result %+v", got, f)
		}
	})

	for _, tc := range []struct {
		name, code string
		status     int
	}{{"rate limited", providers.CodeRateLimited, 429}, {"http error", providers.CodeProviderError, 502}, {"auth", providers.CodeAuth, 401}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.llm.inner = errorLLM{err: &providers.CallError{Code: tc.code, Status: tc.status}}
			res, _ := h.run(wake, "hello")
			f := res.Failure
			if res.Code != tc.code || f == nil || f.Stage != conversation.StageLLM || f.Step != 1 ||
				f.Provider != "llm-instance-x" || f.Model != "model-fast-x" || f.HTTPStatus != tc.status {
				t.Fatalf("failure %+v, result %+v", f, res)
			}
			if got := h.convs.stored[0].meta.Failure; !reflect.DeepEqual(got, f) {
				t.Fatalf("stored %+v, result %+v", got, f)
			}
		})
	}
}

func TestProviderErrorTextNeverReachesOutcomeOrLog(t *testing.T) {
	const leak = "upstream body echoed prompt and key sk-test-secret"
	h := newHarness(t)
	h.llm.inner = errorLLM{err: errors.New(leak)}
	res, _ := h.run(wake, "hello")
	if res.Code != providers.CodeLLMFailed || res.Failure == nil || res.Failure.Code != providers.CodeLLMFailed {
		t.Fatalf("result %+v", res)
	}
	stored, err := json.Marshal(h.convs.stored[0].meta)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), leak) || strings.Contains(h.logs.String(), leak) {
		t.Fatalf("provider text leaked: stored %s log %s", stored, h.logs)
	}
}

// slowLLM sends its first chunk after delay
type slowLLM struct{ delay time.Duration }

func (l slowLLM) Stream(ctx context.Context, _ providers.LLMRequest) (<-chan providers.LLMChunk, error) {
	ch := make(chan providers.LLMChunk, 1)
	go func() {
		defer close(ch)
		select {
		case <-time.After(l.delay):
			ch <- providers.LLMChunk{Text: "Fine."}
		case <-ctx.Done():
		}
	}()
	return ch, nil
}

// a mode's first-token timeout replaces the built-in wait both ways, and the
// failure reports the mode's limit
func TestFirstTokenTimeoutFollowsMode(t *testing.T) {
	t.Run("shorter", func(t *testing.T) {
		h := newHarness(t)
		h.llm.inner = slowLLM{delay: 5 * time.Second}
		h.limits = []time.Duration{3 * time.Second, 8 * time.Second, 8 * time.Second}
		m := fastMode
		m.FirstTokenTimeout = 50 * time.Millisecond
		h.modes["fast"] = m
		start := time.Now()
		res, _ := h.run(wake, "hello")
		if res.Code != agent.CodeTimeout || res.Failure == nil || res.Failure.LimitMs != 50 {
			t.Fatalf("result %+v failure %+v", res, res.Failure)
		}
		if took := time.Since(start); took > 2*time.Second {
			t.Fatalf("took %v, the mode's 50ms not applied", took)
		}
	})
	t.Run("longer", func(t *testing.T) {
		h := newHarness(t)
		h.llm.inner = slowLLM{delay: 150 * time.Millisecond}
		h.limits = []time.Duration{40 * time.Millisecond, 8 * time.Second, 8 * time.Second}
		m := fastMode
		m.FirstTokenTimeout = 2 * time.Second
		h.modes["fast"] = m
		res, _ := h.run(wake, "hello")
		if res.Code != "" || res.Failure != nil {
			t.Fatalf("result %+v failure %+v", res, res.Failure)
		}
	})
}
