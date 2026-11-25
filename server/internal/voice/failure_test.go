package voice

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

type recordedFailure struct {
	turn    string
	meta    conversation.TurnMeta
	failure conversation.TurnFailure
}

func failureRecorder() (chan recordedFailure, func(context.Context, string, string, conversation.TurnMeta, conversation.TurnFailure) error) {
	ch := make(chan recordedFailure, 1)
	return ch, func(_ context.Context, _ string, turn string, meta conversation.TurnMeta, failure conversation.TurnFailure) error {
		ch <- recordedFailure{turn: turn, meta: meta, failure: failure}
		return nil
	}
}

func TestVoiceTurnForwardsAndStoresAgentFailure(t *testing.T) {
	recorded, record := failureRecorder()
	want := &conversation.TurnFailure{Code: agent.CodeTimeout, Stage: conversation.StageLLM, Step: 1,
		Provider: "llm-instance-x", Model: "model-fast-x", WaitedMs: 10001, LimitMs: 10000}
	h := newHarness(t, func(s *setup) {
		s.recordFailure = record
		s.agent = func(context.Context, agent.Turn, chan<- agent.Out) agent.Result {
			return agent.Result{Code: agent.CodeTimeout, Failure: want}
		}
	})
	h.start("s1")
	h.mic("s1", 3, 1)
	closed := h.waitClose("s1")
	if closed.GetCode() != agent.CodeTimeout || closed.GetFailure() == nil ||
		closed.GetFailure().GetLimitMs() != 10000 || closed.GetFailure().GetProvider() != "llm-instance-x" {
		t.Fatalf("close %+v", closed)
	}
	h.waitTurn("s1")
	got := <-recorded
	if got.turn != "s1" || got.meta.Trigger != "button" || got.failure != *want {
		t.Fatalf("recorded %+v", got)
	}
}

func TestVoiceTurnStoresTTSFailureAfterAgent(t *testing.T) {
	recorded, record := failureRecorder()
	h := newHarness(t, func(s *setup) {
		s.recordFailure, s.tts = record, failTTS{}
		s.agent = func(_ context.Context, _ agent.Turn, out chan<- agent.Out) agent.Result {
			out <- agent.Out{Speech: "This cannot be spoken."}
			return agent.Result{}
		}
	})
	h.start("s1")
	h.mic("s1", 3, 1)
	if closed := h.waitClose("s1"); closed.GetCode() != providers.CodeTTSFailed ||
		closed.GetFailure().GetStage() != conversation.StageTTS {
		t.Fatalf("close %+v", closed)
	}
	h.waitTurn("s1")
	got := <-recorded
	if got.turn != "s1" || got.failure.Code != providers.CodeTTSFailed || got.failure.Stage != conversation.StageTTS ||
		got.failure.Provider != defaultValues.TTS {
		t.Fatalf("recorded %+v", got)
	}
}

func TestVoiceTurnStoresSTTFailureBeforeAgent(t *testing.T) {
	recorded, record := failureRecorder()
	h := newHarness(t, func(s *setup) {
		s.recordFailure = record
		s.stt = &recSTT{startErr: errors.New(providers.CodeSTTUnavailable)}
		s.agent = func(context.Context, agent.Turn, chan<- agent.Out) agent.Result {
			t.Fatal("the agent ran without stt")
			return agent.Result{}
		}
	})
	h.start("s1")
	if closed := h.waitClose("s1"); closed.GetCode() != providers.CodeSTTUnavailable ||
		closed.GetFailure().GetStage() != conversation.StageSTT {
		t.Fatalf("close %+v", closed)
	}
	h.waitTurn("s1")
	select {
	case got := <-recorded:
		if got.turn != "s1" || got.failure.Code != providers.CodeSTTUnavailable || got.failure.Provider != defaultValues.STT {
			t.Fatalf("recorded %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no failure recorded")
	}
}
