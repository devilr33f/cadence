package screens

import (
	"context"
	"encoding/json"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/events"
)

// ToolCard is the tool_card event's data: a call's line and card at its latest state, which replaces the call's
// earlier one by CallID on the dashboard. Text is shown, never logged (rule 8).
type ToolCard struct {
	CallID string `json:"callId"`
	Tool   string `json:"tool"`
	// Status is running, done or failed
	Status string `json:"status"`
	Title  string `json:"title"`
	// CardID is the call's own card, "" while it shows none; Card is that card (cadence.cards.v1, protojson) as the
	// turn's first screen holds it
	CardID string          `json:"cardId,omitempty"`
	Card   json.RawMessage `json:"card,omitempty"`
}

var phaseWords = map[cards.Phase]string{cards.Running: "running", cards.Done: "done", cards.Failed: "failed"}

func (s *Service) publishCall(ctx context.Context, t *turn, st *step) {
	if s.d.Publish == nil {
		return
	}
	turnID, _, _ := strings.Cut(st.call.Key, ":")
	tc := ToolCard{CallID: st.id, Tool: st.call.Tool, Status: phaseWords[st.call.Phase()], CardID: st.cardID,
		Title: s.stepLabel(s.lang(ctx, t.origin), st.call)}
	if st.cardID != "" && len(t.screens) > 0 {
		for _, c := range t.cards[t.screens[0]] {
			if c.GetId() == st.cardID {
				if raw, err := protojson.Marshal(c); err == nil {
					tc.Card = raw
				}
			}
		}
	}
	s.d.Publish(events.Event{Type: events.ToolCard, DeviceID: t.origin, SessionID: t.id, TurnID: turnID, At: s.d.Now(), Data: tc})
}
