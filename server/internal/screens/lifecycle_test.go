package screens_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/screens"
)

type oneSong struct{}

func (oneSong) MusicResult(dev, id string) (cards.MusicItem, bool) {
	if dev != "spk" || id != "r2" {
		return cards.MusicItem{}, false
	}
	return cards.MusicItem{Title: "Toxicity", Artist: "System of a Down", DurationS: 219}, true
}
func (oneSong) MusicNow(string) (cards.MusicItem, bool)         { return cards.MusicItem{}, false }
func (oneSong) MusicPlay(context.Context, string, string) error { return nil }

// living has two screens: tv and tv2
func twoScreens(d *screens.Deps) {
	d.Directory = dir{rooms: map[string]string{"spk": "living", "tv": "living", "tv2": "living", "soundbar": "living"}}
	d.Calls = &cards.ToolCards{Music: oneSong{}}
}

func (r *rig) addScreen(id string) {
	r.mu.Lock()
	r.states[id] = `{"power":"on","screen":true}`
	r.mu.Unlock()
}

func cardOf(at *cadencev2.AssistantTurn, id string) (title string, rev uint32, n int) {
	for _, c := range at.GetCards() {
		if c.GetId() == id {
			n++
			title, rev = c.GetRoot().GetCard().GetTitle(), c.GetRevision()
		}
	}
	return title, rev, n
}

// one card per call on every screen of the turn: created at the call (pending), replaced in place by its id once the
// call ends, the step's line its title; the dashboard's feed gets each state, the newest replacing the last
func TestCallCardsReplaceInPlaceOnEveryScreen(t *testing.T) {
	r := newRig(t, twoScreens)
	r.addScreen("tv2")
	var mu sync.Mutex
	var fed []screens.ToolCard
	feed := r.bus.Watch(func(e events.Event) bool { return e.Type == events.ToolCard })
	defer feed.Close()
	go func() {
		for e := range feed.C {
			mu.Lock()
			fed = append(fed, e.Data.(screens.ToolCard))
			mu.Unlock()
		}
	}()
	r.svc.Inbound("spk", &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionStart{SessionStart: &cadencev2.SessionStart{
		SessionId: "s1", Trigger: cadencev2.Trigger_TRIGGER_WAKE,
	}}})
	r.await("tv2", "listening", func(at *cadencev2.AssistantTurn) bool { return at.GetTurnId() == "s1" })
	id := cards.CallCardID("t1:c2")
	r.publish("tool_call", map[string]any{"step": 1, "id": "c2", "name": "music_play", "args": json.RawMessage(`{"id":"r2"}`)})
	for _, scr := range []string{"tv", "tv2"} {
		at := r.await(scr, "the pending card", func(at *cadencev2.AssistantTurn) bool { _, _, n := cardOf(at, id); return n == 1 })
		title, _, _ := cardOf(at, id)
		if title != "Загружаю «Toxicity · System of a Down»" || at.GetSteps()[0].GetLabel() != title || at.GetSteps()[0].GetCardId() != id {
			t.Fatalf("%s pending: card %q, step %v", scr, title, at.GetSteps()[0])
		}
	}
	r.publish("tool_result", map[string]any{"step": 1, "id": "c2", "name": "music_play", "ok": true, "code": "",
		"result": json.RawMessage(`{"ok":true,"data":{"playing":true}}`)})
	for _, scr := range []string{"tv", "tv2"} {
		at := r.await(scr, "the done card", func(at *cadencev2.AssistantTurn) bool {
			title, _, _ := cardOf(at, id)
			return strings.HasPrefix(title, "Играет")
		})
		title, rev, n := cardOf(at, id)
		if n != 1 || rev != 2 || len(at.GetCards()) != 1 || at.GetSteps()[0].GetLabel() != title ||
			at.GetSteps()[0].GetStatus() != cadencev2.StepStatus_STEP_STATUS_DONE {
			t.Fatalf("%s: %d cards, revision %d, steps %v", scr, len(at.GetCards()), rev, at.GetSteps())
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(fed)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(fed) != 2 || fed[0].Status != "running" || fed[1].Status != "done" || fed[1].CardID != id ||
		!strings.Contains(string(fed[1].Card), "Играет") || fed[1].Title != "Играет «Toxicity · System of a Down»" {
		t.Fatalf("feed %+v", fed)
	}
	if strings.Contains(r.logs.String(), "Toxicity") {
		t.Fatalf("a log line carries a title:\n%s", r.logs)
	}
}

// a screen that links mid-turn gets each call's card at its latest state, never the states before
func TestScreenJoiningMidTurnGetsTheLatestCards(t *testing.T) {
	r := newRig(t, twoScreens)
	r.svc.Inbound("spk", &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionStart{SessionStart: &cadencev2.SessionStart{
		SessionId: "s1", Trigger: cadencev2.Trigger_TRIGGER_WAKE,
	}}})
	r.await("tv", "listening", func(at *cadencev2.AssistantTurn) bool { return at.GetTurnId() == "s1" })
	r.publish("tool_call", map[string]any{"step": 1, "id": "c2", "name": "music_play", "args": json.RawMessage(`{"id":"r2"}`)})
	r.publish("tool_result", map[string]any{"step": 1, "id": "c2", "name": "music_play", "ok": false, "code": "unplayable",
		"result": json.RawMessage(`{"ok":false,"error":{"code":"unplayable"}}`)})
	r.await("tv", "the failure", func(at *cadencev2.AssistantTurn) bool {
		return len(at.GetSteps()) == 1 && at.GetSteps()[0].GetStatus() == cadencev2.StepStatus_STEP_STATUS_FAILED
	})
	if len(r.turns("tv2")) != 0 {
		t.Fatal("tv2 was sent the turn before it linked")
	}
	r.addScreen("tv2")
	r.svc.Linked("tv2")
	at := r.await("tv2", "the joined turn", func(at *cadencev2.AssistantTurn) bool { return at.GetTurnId() == "s1" })
	if got := len(r.turns("tv2")); got != 1 {
		t.Fatalf("tv2 got %d states, want the latest alone", got)
	}
	title, rev, n := cardOf(at, cards.CallCardID("t1:c2"))
	if n != 1 || len(at.GetCards()) != 1 || rev != 1 || title != "Не включилось «Toxicity · System of a Down» · не воспроизводится" ||
		at.GetSteps()[0].GetLabel() != title {
		t.Fatalf("joined: %q r%d, %d cards, steps %v", title, rev, len(at.GetCards()), at.GetSteps())
	}
	// a screen of another room never joins
	r.svc.Linked("kitchen-tv")
	r.publish("llm_text", map[string]any{"step": 2, "text": "Не вышло.", "spoken": true})
	r.await("tv2", "the reply", func(at *cadencev2.AssistantTurn) bool { return at.GetReply().GetText() == "Не вышло." })
	for _, at := range r.turns("kitchen-tv") {
		if at.GetTurnId() == "s1" {
			t.Fatal("kitchen-tv joined a living-room turn")
		}
	}
}

// a Home Assistant write's entity card may be followed on every screen of the turn; a read never is
func TestHAWriteCardsMayBeFollowed(t *testing.T) {
	var mu sync.Mutex
	var live []string
	r := newRig(t, twoScreens, func(d *screens.Deps) {
		d.Calls = stubCalls(func(c cards.Call) []cards.Subject {
			if c.Result == nil || !c.Result.OK {
				return nil
			}
			return []cards.Subject{cards.Posts("ha-"+c.Tool, []providers.Post{{Author: "дом", Text: "бойлер"}})}
		})
		d.Live = func(dev string, s cards.Subject) {
			mu.Lock()
			live = append(live, dev+"/"+s.ID())
			mu.Unlock()
		}
	})
	r.addScreen("tv2")
	r.svc.Inbound("spk", &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionStart{SessionStart: &cadencev2.SessionStart{
		SessionId: "s1", Trigger: cadencev2.Trigger_TRIGGER_WAKE,
	}}})
	r.await("tv2", "listening", func(at *cadencev2.AssistantTurn) bool { return at.GetTurnId() == "s1" })
	for i, tool := range []string{"ha_inspect", "ha_control"} {
		id := string(rune('a' + i))
		r.publish("tool_call", map[string]any{"step": 1, "id": id, "name": tool, "args": json.RawMessage(`{}`)})
		r.publish("tool_result", map[string]any{"step": 1, "id": id, "name": tool, "ok": true, "result": json.RawMessage(`{"ok":true,"data":{}}`)})
	}
	r.await("tv2", "both cards", func(at *cadencev2.AssistantTurn) bool { return len(at.GetCards()) == 2 })
	mu.Lock()
	defer mu.Unlock()
	want := cards.Posts("ha-ha_control", []providers.Post{{Author: "x", Text: "y"}}).ID()
	if strings.Join(live, ",") != "tv/"+want+",tv2/"+want {
		t.Fatalf("live %v", live)
	}
}
