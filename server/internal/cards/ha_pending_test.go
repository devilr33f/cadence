package cards_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
)

// haCards wires a tool and its card to one tracker and a real lease, as the
// app's screen service does. pushed holds only revisions sent under the lease.
type haCards struct {
	r       *haRig
	confirm *tools.HAConfirmations
	tools   *cards.ToolCards
	late    chan tools.HAConfirmationRecord
	allow   func(string, cards.Subject)
	lease   func(string, string, []string) int

	mu     sync.Mutex
	pushed []*cardsv1.Card
}

func newHACards(t *testing.T, options tools.HAConfirmationOptions) *haCards {
	t.Helper()
	h := &haCards{r: newHARig(t), late: make(chan tools.HAConfirmationRecord, 2)}
	if options.Record == nil {
		options.Record = func(_ context.Context, rec tools.HAConfirmationRecord) { h.late <- rec }
	}
	h.confirm = tools.NewHAConfirmations(h.r.home, options)
	t.Cleanup(h.confirm.Close)
	h.r.deps.Confirmations = h.confirm
	h.tools = &cards.ToolCards{HA: h.r.deps}
	leases := cards.NewLeases(cards.LeaseDeps{Cards: h.r.svc, Gap: time.Millisecond, Send: func(_ string, env *cadencev2.Envelope) error {
		h.mu.Lock()
		h.pushed = append(h.pushed, env.GetCards().GetCards()...)
		h.mu.Unlock()
		return nil
	}})
	t.Cleanup(leases.Run(context.Background()))
	h.allow = leases.Allow
	h.lease = leases.Lease
	return h
}

func (h *haCards) follow(subj cards.Subject) {
	h.allow("screen-1", subj)
	if n := h.lease("screen-1", "lease-1", []string{subj.ID()}); n != 1 {
		h.r.t.Fatalf("following %d cards", n)
	}
}

func (h *haCards) run(turnID, callID, args string) json.RawMessage {
	h.r.t.Helper()
	control := tools.HAControl(tools.HADeps{Home: h.r.home, Confirmations: h.confirm})
	raw, e := agent.Run(context.Background(), control, agent.Call{
		DeviceID: "d1", SessionID: "s1", TurnID: turnID, CallID: callID,
		Trigger: cadencev2.Trigger_TRIGGER_WAKE, Args: json.RawMessage(args),
	})
	if e != nil {
		h.r.t.Fatalf("ha_control: %+v", e)
	}
	return raw
}

func (h *haCards) show(raw json.RawMessage) *cardsv1.Card {
	h.r.t.Helper()
	subs := h.tools.Subjects(cards.ToolResult{Key: "turn:card", Tool: "ha_control", OK: true, Data: raw})
	if len(subs) != 1 {
		h.r.t.Fatalf("%d subjects", len(subs))
	}
	if _, ok := subs[0].(cards.Live); !ok {
		h.r.t.Fatal("a pending ha_control card is not live")
	}
	c, err := h.r.svc.Show(context.Background(), "screen-1", subs[0])
	if err != nil {
		h.r.t.Fatal(err)
	}
	if text := cardText(c); !strings.Contains(text, "Выполняется") || !strings.Contains(text, "Цель: Выключено") {
		h.r.t.Fatalf("pending card %s", text)
	}
	h.follow(subs[0])
	return c
}

func cardText(c *cardsv1.Card) string {
	var text []string
	shownText(c.ProtoReflect(), &text)
	return strings.Join(text, " | ")
}

func (h *haCards) await(want string) *cardsv1.Card {
	h.r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		h.mu.Lock()
		var last *cardsv1.Card
		if len(h.pushed) > 0 {
			last = h.pushed[len(h.pushed)-1]
		}
		h.mu.Unlock()
		if last != nil && strings.Contains(cardText(last), want) {
			return last
		}
		if time.Now().After(deadline) {
			if last == nil {
				h.r.t.Fatalf("no leased card showed %q", want)
			}
			h.r.t.Fatalf("leased card never showed %q: %s", want, cardText(last))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHAControlPendingCardConfirmsUnderItsLease(t *testing.T) {
	h := newHACards(t, tools.HAConfirmationOptions{})
	confirmed := time.AfterFunc(6*time.Second, func() {
		h.r.fake.Emit("state_changed", obj{"entity_id": "climate.living", "old_state": nil,
			"new_state": haState("climate.living", "off", obj{
				"friendly_name": "Living room", "temperature": 21, "current_temperature": 20.5, "min_temp": 15, "max_temp": 28,
			})})
	})
	defer confirmed.Stop()
	raw := h.run("turn-card-slow", "call-card-slow", `{"action":"turn_off","target":"climate.living"}`)
	initial := h.show(raw)
	done := h.await("Готово")
	if done.GetRevision() <= initial.GetRevision() || done.GetId() != initial.GetId() || strings.Contains(cardText(done), "Выполняется") ||
		!strings.Contains(cardText(done), "Выключено") {
		t.Fatalf("terminal card revision %d: %s", done.GetRevision(), cardText(done))
	}
	select {
	case rec := <-h.late:
		if rec.OperationID != "turn-card-slow:call-card-slow" || !strings.Contains(string(rec.Result), `"late":true`) {
			t.Fatalf("late record %+v", rec)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no late success record")
	}
}

func TestHAControlPendingCardFailsAfterTheWholeBudget(t *testing.T) {
	h := newHACards(t, tools.HAConfirmationOptions{Short: 20 * time.Millisecond, Cap: 140 * time.Millisecond})
	raw := h.run("turn-card-failed", "call-card-failed", `{"action":"turn_off","target":"climate.living"}`)
	h.show(raw)
	failed := h.await("Не сработало")
	if strings.Contains(cardText(failed), "Выполняется") || failed.GetRoot().GetCard().GetTone() != cardsv1.Tone_TONE_DANGER {
		t.Fatalf("failed card %s", cardText(failed))
	}
	select {
	case rec := <-h.late:
		var result struct {
			OK    bool `json:"ok"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(rec.Result, &result) != nil || result.OK || result.Error.Code != "no_effect" {
			t.Fatalf("late failure %s", rec.Result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no late failure record")
	}
}
