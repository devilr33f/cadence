package conversation_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func TestTurnFailureStoredAndServed(t *testing.T) {
	r := newRig(t)
	failure := &conversation.TurnFailure{Code: providers.CodeRateLimited, Stage: conversation.StageLLM, Step: 2,
		Provider: "gateway-main", Model: "vendor/model-x", HTTPStatus: 429, WaitedMs: 1234, LimitMs: 10000}
	if err := r.s.Append(context.Background(), r.dev, "failed-llm", conversation.TurnMeta{Trigger: "wake", Mode: "fast", Failure: failure},
		[]providers.Message{{Role: "user", Content: "hello"}}); err != nil {
		t.Fatal(err)
	}
	p := r.page("")
	if len(p.Turns) != 1 {
		t.Fatalf("page %+v", p)
	}
	got, ok := p.Turns[0]["failure"].(map[string]any)
	if !ok || got["code"] != providers.CodeRateLimited || got["stage"] != conversation.StageLLM ||
		got["step"] != float64(2) || got["provider"] != "gateway-main" || got["model"] != "vendor/model-x" ||
		got["httpStatus"] != float64(429) || got["waitedMs"] != float64(1234) || got["limitMs"] != float64(10000) {
		t.Fatalf("list failure %+v", p.Turns[0]["failure"])
	}
	code, body := r.call("x", "/devices/"+r.dev+"/turns/failed-llm")
	if code != 200 {
		t.Fatalf("detail %d %s", code, body)
	}
	var detail conversation.TurnDetail
	if err := json.Unmarshal([]byte(body), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Failure == nil || *detail.Failure != *failure || len(detail.Messages) != 1 {
		t.Fatalf("detail %+v", detail)
	}
}

func TestVoiceFailureCanCreateOrUpdateTurn(t *testing.T) {
	r := newRig(t)
	stt := conversation.TurnFailure{Code: providers.CodeSTTUnavailable, Stage: conversation.StageSTT, Provider: "stt-main"}
	if err := r.s.RecordFailure(context.Background(), r.dev, "voice-stt", conversation.TurnMeta{Trigger: "wake"}, stt); err != nil {
		t.Fatal(err)
	}
	code, body := r.call("x", "/devices/"+r.dev+"/turns/voice-stt")
	if code != 200 || !strings.Contains(body, `"messages":[]`) || !strings.Contains(body, `"stage":"stt"`) {
		t.Fatalf("stt detail %d %s", code, body)
	}

	if err := r.s.Append(context.Background(), r.dev, "voice-tts", conversation.TurnMeta{Trigger: "wake", Mode: "fast"},
		[]providers.Message{{Role: "user", Content: "hello"}, {Role: "assistant", Content: "answer"}}); err != nil {
		t.Fatal(err)
	}
	tts := conversation.TurnFailure{Code: providers.CodeTTSFailed, Stage: conversation.StageTTS, Provider: "tts-main"}
	if err := r.s.RecordFailure(context.Background(), r.dev, "voice-tts", conversation.TurnMeta{Trigger: "wake"}, tts); err != nil {
		t.Fatal(err)
	}
	_, body = r.call("x", "/devices/"+r.dev+"/turns/voice-tts")
	if !strings.Contains(body, `"stage":"tts"`) || !strings.Contains(body, `"content":"answer"`) || !strings.Contains(body, `"mode":"fast"`) {
		t.Fatalf("tts detail %s", body)
	}
}

func TestTurnFailureDropsProviderText(t *testing.T) {
	r := newRig(t)
	const leak = "provider body with sk-secret"
	failure := &conversation.TurnFailure{Code: leak, Stage: conversation.StageLLM, Provider: "gateway-main"}
	if err := r.s.Append(context.Background(), r.dev, "private", conversation.TurnMeta{Failure: failure},
		[]providers.Message{{Role: "user", Content: "hello"}}); err != nil {
		t.Fatal(err)
	}
	_, body := r.call("x", "/devices/"+r.dev+"/turns/private")
	if strings.Contains(body, leak) || strings.Contains(body, `"failure"`) {
		t.Fatalf("provider text stored or served: %s", body)
	}
}
