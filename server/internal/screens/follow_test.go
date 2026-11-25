package screens_test

import (
	"context"
	"encoding/json"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/screens"
)

// a catalog play's card is the call's own card: it replaces the call's pending card in place, then follows the
// screen under the screen's lease, in the turn and past its end, as a live frame the screen puts in place
func TestAPlaysCardIsTheCallsCardAndFollowsUnderALease(t *testing.T) {
	var leases *cards.Leases
	r := newRig(t, func(d *screens.Deps) {
		d.Calls = &cards.ToolCards{}
		leases = cards.NewLeases(cards.LeaseDeps{Cards: d.Cards.(*cards.Service), Send: d.Send})
		d.Live = leases.Allow
	})
	t.Cleanup(leases.Run(context.Background()))
	r.svc.Inbound("spk", &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionStart{SessionStart: &cadencev2.SessionStart{
		SessionId: "s1", Trigger: cadencev2.Trigger_TRIGGER_WAKE,
	}}})
	r.await("tv", "listening", func(at *cadencev2.AssistantTurn) bool { return at.GetTurnId() == "s1" })
	id := cards.CallCardID("t1:c1")
	r.publish("tool_call", map[string]any{"step": 1, "id": "c1", "name": "catalog_play", "args": json.RawMessage(`{"ref":"r"}`)})
	cardOf := func(at *cadencev2.AssistantTurn) (title, state string, n int) {
		for _, c := range at.GetCards() {
			if c.GetId() == id {
				n++
				title = c.GetRoot().GetCard().GetTitle()
				if ch := c.GetRoot().GetCard().GetChildren(); len(ch) > 0 {
					state = ch[0].GetState().GetLabel()
				}
			}
		}
		return title, state, n
	}
	r.await("tv", "the pending card", func(at *cadencev2.AssistantTurn) bool {
		title, _, n := cardOf(at)
		return n == 1 && title == "Включаю на экране"
	})
	play := cards.NewCatalogPlay("s1:x", cards.CatalogPlaying{Title: "Тьма", Episode: "1", Screen: "Bedroom TV"})
	r.svc.Attach("spk", "s1", "t1:c1", play)
	at := r.await("tv", "the play's card in its place", func(at *cadencev2.AssistantTurn) bool {
		title, state, _ := cardOf(at)
		return title == "Тьма" && state == "Включаю"
	})
	if _, _, n := cardOf(at); n != 1 || len(at.GetCards()) != 1 || at.GetSteps()[0].GetCardId() != id {
		t.Fatalf("cards %d, steps %v", len(at.GetCards()), at.GetSteps())
	}
	r.publish("tool_result", map[string]any{"step": 1, "id": "c1", "name": "catalog_play", "ok": true,
		"result": json.RawMessage(`{"ok":true,"data":{"playing":true}}`)})
	r.await("tv", "the step done, the card still the play's", func(at *cadencev2.AssistantTurn) bool {
		title, _, n := cardOf(at)
		return len(at.GetSteps()) == 1 && at.GetSteps()[0].GetStatus() == cadencev2.StepStatus_STEP_STATUS_DONE && n == 1 && title == "Тьма"
	})

	live := func(state string) bool {
		for _, e := range r.of("tv") {
			cs := e.GetCards()
			if cs.GetContextId() == cards.LiveContext && len(cs.GetCards()) == 1 && cs.GetCards()[0].GetId() == id &&
				cs.GetCards()[0].GetRoot().GetCard().GetChildren()[0].GetState().GetLabel() == state {
				return true
			}
		}
		return false
	}
	// no lease, no follow
	play.Set(cards.PlayBuffering)
	if leases.Following(id) != 0 {
		t.Fatal("followed without a lease")
	}
	if n := leases.Lease("tv", "L", []string{id}); n != 1 {
		t.Fatalf("lease follows %d", n)
	}
	waitFor(t, "loading, live", func() bool { return live("Загружается") })
	r.svc.Outbound("spk", &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionClose{SessionClose: &cadencev2.SessionClose{SessionId: "s1"}}})
	r.await("tv", "resting", func(at *cadencev2.AssistantTurn) bool {
		return at.GetState() == cadencev2.AssistantState_ASSISTANT_STATE_RESTING
	})
	turns := len(r.turns("tv"))
	play.Set(cards.PlayPlaying)
	waitFor(t, "playing past the turn", func() bool { return live("Играет") })
	if n := len(r.turns("tv")); n != turns {
		t.Fatalf("the ended turn was sent again: %d after %d", n, turns)
	}
	if len(r.of("kitchen-tv")) != 0 {
		t.Fatalf("another room heard of it: %v", r.of("kitchen-tv"))
	}
}

// a play that asks which voiceover is no failed step: the question is the reply
func TestAVoiceoverQuestionIsNoFailedStep(t *testing.T) {
	r := newRig(t, func(d *screens.Deps) { d.Calls = &cards.ToolCards{} })
	r.svc.Inbound("spk", &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionStart{SessionStart: &cadencev2.SessionStart{SessionId: "s1"}}})
	r.await("tv", "the turn", func(at *cadencev2.AssistantTurn) bool { return at.GetTurnId() == "s1" })
	r.publish("tool_call", map[string]any{"step": 1, "id": "c1", "name": "catalog_play", "args": json.RawMessage(`{"ref":"r"}`)})
	r.publish("tool_result", map[string]any{"step": 1, "id": "c1", "name": "catalog_play", "ok": false, "code": "choose_voiceover"})
	r.publish("tool_call", map[string]any{"step": 1, "id": "c2", "name": "catalog_search", "args": json.RawMessage(`{}`)})
	r.publish("tool_result", map[string]any{"step": 1, "id": "c2", "name": "catalog_search", "ok": false, "code": "catalog_failed"})
	at := r.await("tv", "both steps ended", func(at *cadencev2.AssistantTurn) bool {
		return len(at.GetSteps()) == 2 && at.GetSteps()[1].GetStatus() != cadencev2.StepStatus_STEP_STATUS_RUNNING
	})
	if at.GetSteps()[0].GetStatus() != cadencev2.StepStatus_STEP_STATUS_DONE || at.GetSteps()[0].GetLabel() != "Включаю на экране" ||
		at.GetSteps()[1].GetStatus() != cadencev2.StepStatus_STEP_STATUS_FAILED || at.GetSteps()[1].GetLabel() != "Ищу в каталоге · каталог не ответил" {
		t.Fatalf("steps %v", at.GetSteps())
	}
	// the question's card stays where the pending one was, without a spinner, and asks
	for _, c := range at.GetCards() {
		if c.GetId() != cards.CallCardID("t1:c1") {
			continue
		}
		for _, n := range c.GetRoot().GetCard().GetChildren() {
			if n.GetProgress() != nil {
				t.Fatal("the asked play still spins")
			}
		}
		return
	}
	t.Fatalf("no card for the asked play: %v", at.GetCards())
}
