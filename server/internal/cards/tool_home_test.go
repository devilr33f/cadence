package cards_test

import (
	"context"
	"encoding/json"
	"testing"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
)

// regression: the bus carries the agent's envelope, {ok, data}; the entity
// is read from its data, so ha_control's card reaches the turn
func TestHAControlResultShowsTheEntity(t *testing.T) {
	r := newHARig(t)
	tc := &cards.ToolCards{HA: r.deps}
	envelope := json.RawMessage(`{"ok":true,"data":{"done":"turn_off","entity":{"id":"light.kitchen","name":"Kitchen light","state":"off"}}}`)
	subs := tc.Subjects(cards.ToolResult{Key: "t:c1", Tool: "ha_control", OK: true, Data: cards.ResultData(envelope)})
	if len(subs) != 1 || subs[0].ID() != cards.OpaqueID("ha", "light.kitchen") {
		t.Fatalf("subjects %v", subs)
	}
	c, err := r.svc.Show(context.Background(), "screen-1", subs[0])
	if err != nil || find[cardsv1.Toggle](c) == nil {
		t.Fatalf("card %v, %v", c, err)
	}
	// the envelope itself names no entity at its top
	if got := tc.Subjects(cards.ToolResult{Key: "t:c1", Tool: "ha_control", OK: true, Data: envelope}); len(got) != 0 {
		t.Fatalf("the envelope read as the tool's output: %v", got)
	}
}

func TestHASearchAndInspectShowTheirEntities(t *testing.T) {
	r := newHARig(t)
	tc := &cards.ToolCards{HA: r.deps}
	few := `{"matches":[{"id":"light.kitchen","name":"Kitchen light","domain":"light","state":"on"},{"id":"lock.front","name":"Front door","domain":"lock","state":"locked"}]}`
	subs := tc.Subjects(cards.ToolResult{Key: "t:c2", Tool: "ha_search", OK: true, Data: json.RawMessage(few)})
	if len(subs) != 2 || subs[0].ID() != cards.OpaqueID("ha", "light.kitchen") || subs[1].ID() != cards.OpaqueID("ha", "lock.front") {
		t.Fatalf("a few matches: %v", subs)
	}
	many := `{"matches":[{"id":"light.kitchen","name":"Kitchen light","state":"on"},{"id":"lock.front","name":"Front door","state":"locked"},
		{"id":"sensor.temp","name":"Temperature","state":"21.5"},{"id":"switch.plug","name":"Plug","state":"off"}]}`
	subs = tc.Subjects(cards.ToolResult{Key: "t:c3", Tool: "ha_search", OK: true, Data: json.RawMessage(many)})
	if len(subs) != 1 {
		t.Fatalf("many matches: %v", subs)
	}
	c, err := r.svc.Show(context.Background(), "screen-1", subs[0])
	if err != nil {
		t.Fatal(err)
	}
	kv := c.GetRoot().GetCard().GetChildren()[0].GetKeyvalue().GetItems()
	if len(kv) != 4 || kv[0].GetKey() != "Kitchen light" || kv[0].GetValue() != "Включено" || kv[2].GetValue() != "21.5" {
		t.Fatalf("list %v", kv)
	}
	subs = tc.Subjects(cards.ToolResult{Key: "t:c4", Tool: "ha_inspect", OK: true,
		Data: json.RawMessage(`{"entity":{"id":"lock.front","name":"Front door","domain":"lock","state":"locked"},"domain":"lock","services":[]}`)})
	if len(subs) != 1 || subs[0].ID() != cards.OpaqueID("ha", "lock.front") {
		t.Fatalf("inspect: %v", subs)
	}
	// without Home Assistant, none
	if got := (&cards.ToolCards{}).Subjects(cards.ToolResult{Tool: "ha_search", OK: true, Data: json.RawMessage(few)}); len(got) != 0 {
		t.Fatalf("no home: %v", got)
	}
}
