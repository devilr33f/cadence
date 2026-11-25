package tools_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
)

func TestHAInspectListsWhatADeviceCanDo(t *testing.T) {
	r := newHARig(t, baseHouse())
	inspect := tools.HAInspect(r.deps())
	for args, want := range map[string]string{
		`{"target":"vacuum.cleaner"}`: `{"entity":{"id":"vacuum.cleaner","name":"Cleaner","domain":"vacuum","state":"docked"},"domain":"vacuum","services":[` +
			`{"name":"clean_spot","description":"Spot clean now.","fields":[]},` +
			`{"name":"set_fan_speed","fields":[{"name":"fan_speed","required":true,"kind":"text"}]},` +
			`{"name":"start","fields":[]}]}`,
		// a domain name
		`{"target":"light"}`: `{"domain":"light","services":[{"name":"toggle","fields":[]},{"name":"turn_off","fields":[]},` +
			`{"name":"turn_on","description":"Turn on one or more lights.","fields":[{"name":"brightness_pct","kind":"number 0..100 %"},` +
			`{"name":"color_temp_kelvin","kind":"color temperature"}]}]}`,
		// a domain with entities but no services
		`{"target":"sensor"}`: `{"domain":"sensor","services":[]}`,
		`{"target":"odd"}`: `{"entity":{"id":"switch.odd","name":"Odd plug name","domain":"switch","state":"on"},"domain":"switch","services":[` +
			`{"name":"toggle","fields":[]},{"name":"turn_off","fields":[]},{"name":"turn_on","fields":[]}]}`,
		`{"target":"news feed"}`: `{"entity":{"id":"sensor.feed","name":"News feed","domain":"sensor","state":"(text)"},"domain":"sensor","services":[]}`,
	} {
		if got := r.raw(inspect, args); got != want {
			t.Fatalf("%s\n got %s\nwant %s", args, got, want)
		}
	}
	if len(r.fake.Calls()) != 0 || r.fake.Received("get_services") != 1 {
		t.Fatal("an inspection fetched or called something")
	}
}

func TestHAInspectAsksOrFindsNothing(t *testing.T) {
	r := newHARig(t, baseHouse())
	inspect := tools.HAInspect(r.deps())
	e := r.refused(inspect, `{"target":"hall"}`, "ambiguous")
	if e.Message != "Several devices match: Hall light, Hall sensor, Hallway." || !e.Retryable ||
		!slices.Equal(e.Candidates, []string{"Hall light", "Hall sensor", "Hallway"}) {
		t.Fatalf("%+v", e)
	}
	// the device's area settles it
	r.setArea("bedroom")
	if out := r.ok(inspect, `{"target":"hall"}`); out["entity"].(map[string]any)["id"] != "sensor.hall" {
		t.Fatalf("%v", out)
	}
	// a domain lists the domain, never the one entity of another domain the
	// area would leave (the bedroom's "Wall switch" light)
	if out := r.raw(inspect, `{"target":"switch"}`); !strings.HasPrefix(out, `{"domain":"switch","services":[`) {
		t.Fatalf("%s", out)
	}
	// names that collide carry their area, else their id
	r.setArea("")
	e = r.refused(inspect, `{"target":"Light Bulb"}`, "ambiguous")
	if e.Message != "Several devices match: Light Bulb (light.bulb), Light Bulb (update.bulb)." ||
		!slices.Equal(e.Candidates, []string{"Light Bulb (light.bulb)", "Light Bulb (update.bulb)"}) {
		t.Fatalf("%+v", e)
	}
	if e := r.refused(inspect, `{"target":"garage"}`, "not_found"); e.Message != `no device or domain matched "garage"` || !e.Retryable {
		t.Fatalf("%+v", e)
	}
}

func TestHAInspectKeepsItsResultShort(t *testing.T) {
	b := baseHouse()
	b.states = []any{haState("big.thing", "on", named("Thing")), haState("many.thing", "on", named("Many"))}
	big, many := obj{}, obj{}
	for i := range 20 {
		fields := obj{}
		for j := range 15 {
			fields[fmt.Sprintf("f%02d", j)] = obj{"selector": obj{"number": obj{"min": 0, "max": 100, "unit_of_measurement": "%"}}}
		}
		big[fmt.Sprintf("s%02d", i)] = obj{"description": strings.Repeat("d", 200), "fields": fields}
		many[fmt.Sprintf("s%02d", i)] = obj{"fields": obj{}}
	}
	b.services = obj{"big": big, "many": many}
	b.entities, b.entries = nil, nil
	r := newHARig(t, b)
	inspect := tools.HAInspect(r.deps())
	out := r.ok(inspect, `{"target":"big.thing"}`)
	services := out["services"].([]any)
	raw := jsonOf(out)
	if out["truncated"] != true || len(services) < 1 || len(services) >= 15 || utf8.RuneCountInString(raw) > 4000 {
		t.Fatalf("%d services in %d code points: %.200s", len(services), utf8.RuneCountInString(raw), raw)
	}
	first := services[0].(map[string]any)
	if len(first["fields"].([]any)) != 12 || first["description"] != strings.Repeat("d", 120) {
		t.Fatalf("first %v", first)
	}
	out = r.ok(inspect, `{"target":"many"}`)
	if n := len(out["services"].([]any)); n != 15 || out["truncated"] != true {
		t.Fatalf("%d services, truncated %v", n, out["truncated"])
	}
}
