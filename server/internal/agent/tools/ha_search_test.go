package tools_test

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
)

func TestHASearchMatches(t *testing.T) {
	r := newHARig(t, baseHouse())
	search := tools.HASearch(r.deps())
	for args, want := range map[string]string{
		`{"query":"living room"}`: `{"matches":[{"id":"climate.living_room","name":"Living room","domain":"climate","state":"heat",` +
			`"attrs":{"current_temperature":20.5,"hvac_action":"heating","temperature":21}}]}`,
		// the area by name; attributes as home assistant reports them
		`{"query":" Kitchen  LIGHT "}`: `{"matches":[{"id":"light.kitchen","name":"Kitchen light","domain":"light","area":"Kitchen main",` +
			`"state":"on","attrs":{"brightness":128,"color_temp_kelvin":2700}}]}`,
		`{"query":"temp"}`: `{"matches":[{"id":"sensor.temp","name":"Temperature","domain":"sensor","state":"21.5",` +
			`"attrs":{"device_class":"temperature","unit_of_measurement":"°C"}}]}`,
		// sanitized names; free text never reaches the model
		`{"query":"odd"}`:  `{"matches":[{"id":"switch.odd","name":"Odd plug name","domain":"switch","state":"on"}]}`,
		`{"query":"feed"}`: `{"matches":[{"id":"sensor.feed","name":"News feed","domain":"sensor","state":"(text)"}]}`,
		`{"query":"note"}`: `{"matches":[{"id":"input_text.note","name":"Note","domain":"input_text","state":"(text)"}]}`,
		// an exact name first, then by id; aliases match too
		`{"query":"lamp"}`: `{"matches":[{"id":"switch.lamp_plug","name":"Lamp","domain":"switch","state":"off"},` +
			`{"id":"light.desk","name":"Desk","domain":"light","state":"off"},` +
			`{"id":"light.reading","name":"Reading lamp","domain":"light","state":"off"}]}`,
		`{"query":"hall","domain":"light"}`: `{"matches":[{"id":"light.hall","name":"Hall light","domain":"light","state":"off"}]}`,
	} {
		if got := r.raw(search, args); got != want {
			t.Fatalf("%s\n got %s\nwant %s", args, got, want)
		}
	}
	out := r.ok(search, `{"query":"light"}`)
	if n := len(out["matches"].([]any)); n != 5 {
		t.Fatalf("%d matches", n)
	}
	if len(r.fake.Calls()) != 0 || r.fake.Received("get_states") != 1 {
		t.Fatal("a search fetched or called something")
	}
}

func TestHASearchNotFound(t *testing.T) {
	r := newHARig(t, baseHouse())
	search := tools.HASearch(r.deps())
	for args, msg := range map[string]string{
		`{"query":"garage"}`:                  `nothing matched "garage"`,
		`{"query":"hall","domain":"cover"}`:   `nothing matched "hall" in domain cover`,
		`{"query":"garage","domain":"cover"}`: `nothing matched "garage" in domain cover`,
	} {
		if e := r.refused(search, args, "not_found"); e.Message != msg || !e.Retryable {
			t.Fatalf("%s: %+v", args, e)
		}
	}
	for _, args := range []string{`{"query":""}`, `{"query":"  "}`, `{"query":"x","domain":"Light"}`, `{"query":"` + strings.Repeat("x", 101) + `"}`} {
		r.refused(search, args, "invalid_arguments")
	}
}

func TestHASearchKeepsItsResultShort(t *testing.T) {
	b := baseHouse()
	b.states = nil
	b.areas = []any{obj{"area_id": "far", "name": strings.Repeat("я", 100)}}
	b.entities = nil
	for i := range 5 {
		id := fmt.Sprintf("switch.big_%s_%d", strings.Repeat("a", 100), i)
		b.states = append(b.states, haState(id, strings.Repeat("s", 70), obj{
			"friendly_name": strings.Repeat("ы", 100), "unit_of_measurement": strings.Repeat("u", 32),
			"device_class": strings.Repeat("d", 32), "hvac_action": strings.Repeat("h", 32), "operation_mode": strings.Repeat("o", 32),
		}))
		b.entities = append(b.entities, haReg(id, obj{"area_id": "far"}))
	}
	r := newHARig(t, b)
	raw, e := run(t, tools.HASearch(r.deps()), 0, `{"query":"big"}`)
	if e != nil {
		t.Fatalf("%+v", e)
	}
	n := strings.Count(string(raw), `"id":`)
	if utf8.RuneCount(raw) > 2000 || n < 1 || n >= 5 {
		t.Fatalf("%d matches in %d code points", n, utf8.RuneCount(raw))
	}
	if !strings.Contains(string(raw), `"name":"`+strings.Repeat("ы", 60)+`"`) || !strings.Contains(string(raw), `"state":"`+strings.Repeat("s", 64)+`"`) ||
		!strings.Contains(string(raw), `"area":"`+strings.Repeat("я", 60)+`"`) {
		t.Fatalf("names, areas and states are not cut: %s", raw)
	}
}
