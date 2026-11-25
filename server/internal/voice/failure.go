package voice

import (
	"context"
	"strings"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// failureFor keeps the agent's detail when its code is the session's, and
// classifies a failure voice handled before or after the agent otherwise.
func (s *session) failureFor(code string) *conversation.TurnFailure {
	if !failureCode(code) {
		return nil
	}
	if s.failure != nil && s.failure.Code == code {
		return conversation.SanitizeTurnFailure(s.failure)
	}
	f := &conversation.TurnFailure{Code: code, Stage: conversation.StageInternal,
		WaitedMs: s.c.d.Now().Sub(s.started).Milliseconds()}
	switch {
	case strings.HasPrefix(code, "stt_"):
		f.Stage, f.Provider = conversation.StageSTT, s.values.STT
	case strings.HasPrefix(code, "tts_"):
		f.Stage, f.Provider = conversation.StageTTS, s.values.TTS
	case code == codeNotConfigured:
		f.Stage = conversation.StageSetup
	case code == providers.CodeLLMFailed || code == providers.CodeProviderError || code == providers.CodeRateLimited || code == providers.CodeAuth:
		f.Stage, f.Provider, f.Model = conversation.StageLLM, s.values.LLM, s.values.Model
	case code == codeInternal && !s.asked:
		f.Stage = conversation.StageSetup
	case s.asked:
		f.Stage, f.Provider = conversation.StageLLM, s.values.LLM
	default:
		f.Stage = conversation.StageSTT
	}
	return conversation.SanitizeTurnFailure(f)
}

func failureCode(code string) bool {
	switch code {
	case "", "ok", codeStopped, codeFollowUpEcho, reasonDisconnected, reasonCancelled, reasonSuperseded:
		return false
	default:
		return true
	}
}

func failureProto(f *conversation.TurnFailure) *cadencev2.TurnFailure {
	f = conversation.SanitizeTurnFailure(f)
	if f == nil {
		return nil
	}
	step, status := uint32(f.Step), uint32(f.HTTPStatus) //nolint:gosec // SanitizeTurnFailure bounds both
	return &cadencev2.TurnFailure{Code: f.Code, Stage: f.Stage, Step: step, Provider: f.Provider, Model: f.Model,
		Tool: f.Tool, HttpStatus: status, WaitedMs: f.WaitedMs, LimitMs: f.LimitMs}
}

// recordFailure also runs after the session's context ended. The agent's rich
// failure is upserted unchanged; a tts failure replaces it, and a stt/setup one
// creates a row that has no synthesized transcript.
func (s *session) recordFailure(code string) {
	f := s.failureFor(code)
	if f == nil || s.c.d.RecordFailure == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 5*time.Second)
	defer cancel()
	meta := conversation.TurnMeta{Trigger: sessionTrigger(s.trigger), Mode: s.mode}
	if err := s.c.d.RecordFailure(ctx, s.dev, s.id, meta, *f); err != nil {
		s.c.d.Log.Error("turn_failure_record_failed", "device_id", s.dev, "session_id", s.id, "code", "store")
	}
}

func sessionTrigger(t cadencev2.Trigger) string {
	switch t {
	case cadencev2.Trigger_TRIGGER_WAKE:
		return "wake"
	case cadencev2.Trigger_TRIGGER_BUTTON:
		return "button"
	case cadencev2.Trigger_TRIGGER_FOLLOW_UP:
		return "follow_up"
	case cadencev2.Trigger_TRIGGER_EVENT:
		return conversation.TriggerEvent
	default:
		return ""
	}
}
