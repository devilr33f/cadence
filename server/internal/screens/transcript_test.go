package screens_test

import (
	"encoding/json"
	"strings"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/screens"
)

// the room's speaker listens and the tv shows: each partial reaches the tv as
// it comes, before any final or reply, then the final replaces it
func TestPartialsReachTheScreenAsTheyCome(t *testing.T) {
	r := newRig(t)
	r.svc.Inbound("spk", &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionStart{SessionStart: &cadencev2.SessionStart{
		SessionId: "s1", Trigger: cadencev2.Trigger_TRIGGER_WAKE,
	}}})
	r.await("tv", "listening", func(at *cadencev2.AssistantTurn) bool {
		return at.GetState() == cadencev2.AssistantState_ASSISTANT_STATE_LISTENING
	})
	steps := []struct{ text, stable, unstable string }{
		{"выключи", "", "выключи"},
		{"выключи свет", "выключи", "свет"},
		// a repeat changes nothing, so nothing is sent
		{"выключи свет", "выключи", "свет"},
		{"выключи свет в", "выключи свет", "в"},
		// the recognizer changed its mind about a word: only the words before it stay stable
		{"выключи свет на кухне", "выключи свет", "на кухне"},
	}
	sent := 0
	for _, s := range steps {
		r.svc.Transcript("spk", "s1", s.text, false)
		at := r.await("tv", s.text, func(at *cadencev2.AssistantTurn) bool {
			return at.GetTranscript().GetStable() == s.stable && at.GetTranscript().GetUnstable() == s.unstable
		})
		if at.GetState() != cadencev2.AssistantState_ASSISTANT_STATE_LISTENING {
			t.Fatalf("a partial moved the state: %v", at)
		}
		sent = len(r.turns("tv"))
	}
	if want := 1 + 4; sent != want {
		t.Fatalf("%d states sent, want %d (a repeated partial sends nothing)", sent, want)
	}
	// another device's session, or a session no screen shows, draws nothing
	r.svc.Transcript("kitchen-tv", "s1", "чужое", false)
	r.svc.Transcript("spk", "s9", "чужое", false)

	r.svc.Transcript("spk", "s1", "выключи свет на кухне", true)
	r.await("tv", "the final", func(at *cadencev2.AssistantTurn) bool {
		return at.GetTranscript().GetUnstable() == "" && at.GetTranscript().GetStable() == "выключи свет на кухне"
	})
	n := len(r.turns("tv"))
	// a late partial and the agent's copy of the final change nothing
	r.svc.Transcript("spk", "s1", "выключи свет на", false)
	r.publish("transcript", map[string]any{"text": "выключи свет на кухне"})
	r.publish("llm_text", map[string]any{"step": 1, "text": "Готово.", "spoken": true})
	at := r.await("tv", "the reply", func(at *cadencev2.AssistantTurn) bool { return at.GetReply().GetText() == "Готово." })
	if at.GetTranscript().GetStable() != "выключи свет на кухне" || at.GetTranscript().GetUnstable() != "" {
		t.Fatalf("the final was replaced: %v", at.GetTranscript())
	}
	if got := len(r.turns("tv")); got != n+1 {
		t.Fatalf("%d states after the final, want only the reply's", got-n)
	}
	for _, e := range r.of("kitchen-tv") {
		if strings.Contains(e.String(), "чужое") {
			t.Fatal("another room drew the partial")
		}
	}
	if strings.Contains(r.logs.String(), "выключи") {
		t.Fatalf("a log line carries the transcript:\n%s", r.logs)
	}
}

// the agent's tool calls reach the tv as cards: a built-in tool's own card,
// and an MCP tool's generic one with its name and status alone
func TestToolCallsShowTheirCards(t *testing.T) {
	r := newRig(t, func(d *screens.Deps) { d.Calls = &cards.ToolCards{} })
	r.svc.Inbound("spk", &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionStart{SessionStart: &cadencev2.SessionStart{
		SessionId: "s1", Trigger: cadencev2.Trigger_TRIGGER_WAKE,
	}}})
	r.await("tv", "listening", func(at *cadencev2.AssistantTurn) bool { return at.GetTurnId() == "s1" })
	r.publish("tool_call", map[string]any{"step": 1, "id": "c1", "name": "weather", "args": json.RawMessage(`{}`)})
	r.publish("tool_result", map[string]any{"step": 1, "id": "c1", "name": "weather", "ok": true, "code": "",
		"result": json.RawMessage(`{"ok":true,"data":{"place":"Москва","days":[{"date":"2026-11-14","min":-2,"max":4,"code":3,"summary":"Облачно"}]}}`)})
	r.publish("tool_call", map[string]any{"step": 1, "id": "c2", "name": "notes_add", "args": json.RawMessage(`{"note":"hunter2"}`)})
	r.publish("tool_result", map[string]any{"step": 1, "id": "c2", "name": "notes_add", "ok": false, "code": "timeout",
		"result": json.RawMessage(`{"ok":false,"error":{"code":"timeout","message":"sk-live-123"}}`)})
	at := r.await("tv", "both cards", func(at *cadencev2.AssistantTurn) bool { return len(at.GetCards()) == 2 })
	if got := at.GetCards()[0].GetRoot().GetCard().GetTitle(); got != "Погода · Москва" {
		t.Fatalf("weather card %q", got)
	}
	generic := at.GetCards()[1]
	if generic.GetRoot().GetCard().GetTitle() != "notes_add" || strings.Contains(generic.String(), "hunter2") ||
		strings.Contains(generic.String(), "sk-live") {
		t.Fatalf("generic card %v", generic)
	}
}
