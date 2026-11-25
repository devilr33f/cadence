package cards

import (
	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
)

// at most this many entities as cards of their own; more are a list
const maxEntityCards = 3

type entityRef struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

// haControlCards: the entity the call controlled, live, with its controls and
// the per-call confirmation state while a slow command is pending.
func haControlCards(t *ToolCards, r ToolResult) []Subject {
	var out struct {
		Entity       entityRef `json:"entity"`
		Pending      bool      `json:"pending"`
		State        string    `json:"state"`
		Expected     string    `json:"expected"`
		Confirmation string    `json:"confirmation"`
	}
	if t.HA.Home == nil || !decode(r, &out) || out.Entity.ID == "" {
		return nil
	}
	if out.Confirmation == "" {
		return []Subject{t.HA.Entity(out.Entity.ID)}
	}
	initial := tools.HAConfirmationSnapshot{State: out.State, Expected: out.Expected}
	if out.Pending {
		initial.Phase = tools.HAConfirmationPending
	}
	return []Subject{&haEntity{d: t.HA, id: out.Entity.ID, confirmation: out.Confirmation, initial: initial}}
}

// haInspectCards: the entity the agent looked at, as ha_control's card
func haInspectCards(t *ToolCards, r ToolResult) []Subject {
	var out struct {
		Entity *entityRef `json:"entity"`
	}
	if t.HA.Home == nil || !decode(r, &out) || out.Entity == nil || out.Entity.ID == "" {
		return nil
	}
	return []Subject{t.HA.Entity(out.Entity.ID)}
}

// haSearchCards: a few matches as their own live cards, more as one list of
// names and states
func haSearchCards(t *ToolCards, r ToolResult) []Subject {
	var out struct {
		Matches []entityRef `json:"matches"`
	}
	if t.HA.Home == nil || !decode(r, &out) || len(out.Matches) == 0 {
		return nil
	}
	if len(out.Matches) <= maxEntityCards {
		subs := make([]Subject, 0, len(out.Matches))
		for _, m := range out.Matches {
			if m.ID != "" {
				subs = append(subs, t.HA.Entity(m.ID))
			}
		}
		return subs
	}
	return card("ha-found", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		items := make([][2]string, 0, len(out.Matches))
		for _, m := range out.Matches {
			items = append(items, [2]string{m.Name, word(rr, "ha.state.", m.State)})
		}
		b := &cardsv1.CardBlock{Title: rr.T("ha.found"), Icon: "home", Tone: cardsv1.Tone_TONE_NEUTRAL}
		if kv := rows(items); kv != nil {
			b.Children = pcards.Nodes(kv)
		}
		return b, rr.T("search.fallback", "count", itoa(len(out.Matches)))
	})
}

// word is a value as the catalog words it under prefix, the value itself
// when the catalog has no word for it
func word(rr *Render, prefix, v string) string {
	key := prefix + v
	if w := rr.T(key); w != key && w != "" {
		return w
	}
	return v
}
