package ha_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
)

func TestHomeLoadsTheView(t *testing.T) {
	h := newHomeRig(t, baseHouse())
	if _, err := h.home.View(); !errors.Is(err, ha.ErrUnavailable) {
		t.Fatalf("view before any load: %v", err)
	}
	v := h.loaded()
	want := []ha.Entity{
		{ID: "climate.ac", Domain: "climate", Platform: "demo", Name: "AC", State: "cool",
			Attrs: ha.Attrs{Temperature: ptr(26.0), CurrentTemperature: ptr(24.5), HVACAction: ptr("cooling")}},
		{ID: "cover.garage", Domain: "cover", Platform: "demo", Name: "Garage door", State: "closed",
			Attrs: ha.Attrs{DeviceClass: ptr("garage")}},
		// the entity's own area wins over its device's; a null brightness is absent
		{ID: "light.bedroom", Domain: "light", Platform: "demo", Name: "Bedroom lamp", Area: "bedroom", State: "off"},
		{ID: "light.hall", Domain: "light", Platform: "demo", Name: "Hall light", State: "off"},
		// the registry name wins; the area comes from the device; null and over-long aliases dropped
		{ID: "light.kitchen", Domain: "light", Platform: "hue", Name: "Kitchen", Area: "kitchen",
			Aliases: []string{"big light", "верхний свет"}, State: "on", Attrs: ha.Attrs{Brightness: ptr(128.0)}},
		// no registry entry, no platform
		{ID: "light.porch", Domain: "light", Name: "light.porch", State: "on"},
		// hidden entities stay, disabled ones (sensor.old) go
		{ID: "switch.fan", Domain: "switch", Platform: "demo", Name: "Fan plug", Area: "kitchen", State: "on"},
	}
	if !reflect.DeepEqual(v.Entities, want) {
		t.Fatalf("entities\n got %+v\nwant %+v", v.Entities, want)
	}
	if e, ok := v.ByID("light.kitchen"); !ok || e.Name != "Kitchen" {
		t.Fatalf("by id %+v %v", e, ok)
	}
	if _, ok := v.ByID("sensor.old"); ok {
		t.Fatal("a disabled entity is in the view")
	}
	wantAreas := []ha.Area{{ID: "bedroom", Name: "Bedroom"}, {ID: "kitchen", Name: "Kitchen", Aliases: []string{"кухня"}}}
	if !reflect.DeepEqual(v.Areas, wantAreas) {
		t.Fatalf("areas %+v", v.Areas)
	}
	wantServices := map[string]map[string]ha.Service{
		"light": {
			"turn_on": {Description: "Turn a light on", Fields: []ha.Field{
				{Name: "transition", Kind: "number 0..300 seconds", Selector: "number"},
				{Name: "brightness_pct", Description: "Percent", Kind: "number 0..100 %", Selector: "number"},
				// a section's fields are flat, as the call takes them
				{Name: "flash", Kind: "one of short, long", Selector: "select"},
			}},
			"turn_off": {},
		},
		"homeassistant": {"turn_on": {}, "turn_off": {}, "toggle": {}},
		"climate": {"set_temperature": {Fields: []ha.Field{
			{Name: "temperature", Required: true, Description: "Target", Kind: "number 7..35", Selector: "number"},
		}}},
	}
	if !reflect.DeepEqual(v.Services, wantServices) {
		t.Fatalf("services\n got %+v\nwant %+v", v.Services, wantServices)
	}
	if !regexp.MustCompile(`msg=ha_loaded entities=7 areas=2 services=6 ms=\d+\n`).MatchString(h.logs.String()) {
		t.Fatalf("logs %s", h.logs)
	}
}

func TestHomeFollowsStateChanges(t *testing.T) {
	h := newHomeRig(t, baseHouse())
	h.loaded()
	h.fake.Emit("state_changed", stateChanged("light.kitchen",
		state("light.kitchen", "off", obj{"friendly_name": "Kitchen ceiling", "brightness": nil, "color_temp_kelvin": nil})))
	h.fake.Emit("state_changed", stateChanged("light.porch", nil))
	h.fake.Emit("state_changed", stateChanged("fan.new", state("fan.new", "on", obj{"friendly_name": "New fan", "percentage": 40})))
	v := h.until("the changes", func(v ha.View) bool { _, ok := v.ByID("fan.new"); return ok })
	if e, _ := v.ByID("light.kitchen"); e.State != "off" || e.Attrs.Brightness != nil || e.Name != "Kitchen" || e.Area != "kitchen" {
		t.Fatalf("updated %+v", e)
	}
	if _, ok := v.ByID("light.porch"); ok {
		t.Fatal("a removed entity is still there")
	}
	if e, _ := v.ByID("fan.new"); e.Name != "New fan" || e.Attrs.Percentage == nil || *e.Attrs.Percentage != 40 {
		t.Fatalf("added %+v", e)
	}
	// a new state the parser refuses removes the entity, as a reload would
	h.fake.Emit("state_changed", stateChanged("light.hall", state("light.hall", strings.Repeat("x", 256), nil)))
	h.until("the malformed state", func(v ha.View) bool { _, ok := v.ByID("light.hall"); return !ok })
}

func TestHomeKeepsStateChangesThatArriveDuringALoad(t *testing.T) {
	h := newHomeRig(t, baseHouse())
	// get_states is answered, then the load waits on get_services
	h.fake.Hold("get_services")
	h.run()
	eventually(t, "the load waiting", func() bool { return h.fake.Held() == 1 })
	h.fake.Emit("state_changed", stateChanged("light.kitchen", state("light.kitchen", "off", nil)))
	h.fake.Emit("state_changed", stateChanged("light.porch", nil))
	h.fake.Emit("state_changed", stateChanged("light.porch", state("light.porch", "unavailable", nil)))
	h.fake.Emit("state_changed", stateChanged("light.hall", nil))
	h.fake.Release()
	v := h.until("loaded", func(v ha.View) bool { return v.Fresh })
	if e, _ := v.ByID("light.kitchen"); e.State != "off" {
		t.Fatalf("lost a change made during the load: %+v", e)
	}
	if e, _ := v.ByID("light.porch"); e.State != "unavailable" {
		t.Fatalf("the last change per entity wins: %+v", e)
	}
	if _, ok := v.ByID("light.hall"); ok {
		t.Fatal("lost a removal made during the load")
	}
}

func TestHomeRelistsAChangedRegistry(t *testing.T) {
	b := baseHouse()
	h := newHomeRig(t, b)
	h.loaded()
	b.areas[0] = obj{"area_id": "kitchen", "name": "Galley", "aliases": []string{"камбуз"}}
	b.devices[0] = obj{"id": "devk", "area_id": "bedroom"}
	b.entities[0] = regEntry("light.kitchen", obj{"name": "Main light", "device_id": "devk"})
	b.entries["light.kitchen"] = obj{"aliases": []any{"top light"}}
	b.script(h.fake)
	h.fake.Emit("area_registry_updated", obj{"action": "update", "area_id": "kitchen"})
	v := h.until("the area", func(v ha.View) bool { return v.Areas[1].Name == "Galley" })
	if !slices.Equal(v.Areas[1].Aliases, []string{"камбуз"}) {
		t.Fatalf("areas %+v", v.Areas)
	}
	h.fake.Emit("device_registry_updated", obj{"action": "update", "device_id": "devk"})
	h.until("the device's area", func(v ha.View) bool { e, _ := v.ByID("light.kitchen"); return e.Area == "bedroom" })
	h.fake.Emit("entity_registry_updated", obj{"action": "update", "entity_id": "light.kitchen", "changes": obj{"name": "Kitchen"}})
	v = h.until("the entity", func(v ha.View) bool { e, _ := v.ByID("light.kitchen"); return e.Name == "Main light" })
	if e, _ := v.ByID("light.kitchen"); !slices.Equal(e.Aliases, []string{"top light"}) {
		t.Fatalf("aliases %+v", e)
	}
	if n := h.fake.Received("get_states"); n != 1 {
		t.Fatalf("a registry change fetched the states again (%d)", n)
	}
}

func TestHomeRefreshesServices(t *testing.T) {
	h := newHomeRig(t, baseHouse())
	h.loaded()
	services := obj{
		"light":  obj{"turn_on": obj{"fields": obj{}}},
		"vacuum": obj{"clean_spot": obj{"description": "Spot", "fields": obj{}}},
	}
	h.fake.SetResult("get_services", services)
	h.fake.Emit("service_registered", obj{"domain": "vacuum", "service": "clean_spot"})
	v := h.until("the new service", func(v ha.View) bool { _, ok := v.Services["vacuum"]["clean_spot"]; return ok })
	if v.Services["vacuum"]["clean_spot"].Description != "Spot" || len(v.Services) != 2 {
		t.Fatalf("services %+v", v.Services)
	}
	delete(services, "light")
	h.fake.SetResult("get_services", services)
	h.fake.Emit("service_removed", obj{"domain": "light", "service": "turn_on"})
	h.until("the service gone", func(v ha.View) bool { _, ok := v.Services["light"]; return !ok })
}

func TestHomeDropsMalformedEntriesAndKeepsTheRest(t *testing.T) {
	b := house{
		states: []any{
			state("light.good", "on", obj{"friendly_name": "Good", "hvac_action": nil}),
			state("Light.Upper", "on", nil),
			state("light", "on", nil),
			state("light."+strings.Repeat("a", 123), "on", nil),
			state("light.long_state", strings.Repeat("s", 256), nil),
			state("light.long_name", "on", obj{"friendly_name": strings.Repeat("n", 101)}),
			state("light.bright", "on", obj{"brightness": 256}),
			state("light.bright_text", "on", obj{"brightness": "high"}),
			state("fan.pct", "on", obj{"percentage": 101}),
			state("climate.mode", "heat", obj{"hvac_action": strings.Repeat("h", 33)}),
			state("light.attrs", "on", nil),
			obj{"entity_id": "light.no_state"},
			"junk",
			state(strings.Repeat("d", 65)+".long_domain", "on", nil),
			state("sensor.max_state", strings.Repeat("ы", 255), obj{"friendly_name": strings.Repeat("ы", 100)}),
			state("switch.long_entry", "on", obj{"friendly_name": "Long"}),
		},
		services: obj{
			"light": obj{
				"turn_on":    obj{"description": "ok", "fields": obj{"flash": obj{"selector": obj{"select": obj{"options": []string{"a"}}}}}},
				"long":       obj{"description": strings.Repeat("d", 501)},
				"bad_field":  obj{"fields": obj{"x": obj{"required": "yes"}}},
				"Bad-Name":   obj{},
				"not_object": "junk",
			},
			"Bad-Domain": obj{"x": obj{}},
			"scalar":     7,
			"switch":     obj{"turn_on": obj{"description": strings.Repeat("d", 500)}},
		},
		areas: []any{
			obj{"area_id": "kitchen", "name": "Kitchen", "aliases": []string{}},
			obj{"area_id": "Bad Area", "name": "X"},
			obj{"area_id": "long_name", "name": strings.Repeat("n", 101)},
			obj{"area_id": "no_name"},
			"junk",
		},
		devices: []any{obj{"id": "dev1", "area_id": "kitchen"}, obj{"id": "Bad Device", "area_id": "kitchen"}, 3},
		entities: []any{
			regEntry("light.good", obj{"device_id": "dev1"}), regEntry("light.attrs", obj{"name": 5}), "junk",
			regEntry("switch.long_entry", obj{"name": strings.Repeat("n", 101), "platform": "cadence"}),
		},
		entries: map[string]any{},
	}
	// attributes that are not an object make the entry malformed
	b.states[10].(obj)["attributes"] = []int{1}
	h := newHomeRig(t, b)
	v := h.loaded()
	if got := ids(v); !slices.Equal(got, []string{"light.good", "sensor.max_state", "switch.long_entry"}) {
		t.Fatalf("entities %v", got)
	}
	if e, _ := v.ByID("light.good"); e.Area != "kitchen" || e.Name != "Good" || e.PlatformUnknown {
		t.Fatalf("good %+v", e)
	}
	// an entry it cannot read may be any integration's, Cadence's own included
	if e, _ := v.ByID("switch.long_entry"); !e.PlatformUnknown || !e.EntryUnreadable || e.Platform != "" || e.Name != "Long" {
		t.Fatalf("an unreadable entry %+v", e)
	}
	if len(v.Areas) != 1 || v.Areas[0].ID != "kitchen" {
		t.Fatalf("areas %+v", v.Areas)
	}
	var got []string
	for d, ss := range v.Services {
		for s := range ss {
			got = append(got, d+"."+s)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"light.turn_on", "switch.turn_on"}) {
		t.Fatalf("services %v", got)
	}
}

func TestHomeCapsWhatItKeeps(t *testing.T) {
	b := baseHouse()
	b.states = nil
	for i := range 4100 {
		b.states = append(b.states, state(fmt.Sprintf("sensor.s%04d", i), "1", nil))
	}
	domains := obj{}
	for i := range 260 {
		domains[fmt.Sprintf("d%03d", i)] = obj{}
	}
	many := obj{}
	for i := range 70 {
		many[fmt.Sprintf("s%02d", i)] = obj{}
	}
	fields := obj{}
	for i := range 30 {
		fields[fmt.Sprintf("f%02d", i)] = obj{}
	}
	section := obj{}
	for i := range 10 {
		section[fmt.Sprintf("g%02d", i)] = obj{}
	}
	fields["zz_section"] = obj{"fields": section}
	domains["d000"] = obj{"many": obj{"fields": fields}}
	domains["d001"] = many
	b.services = domains
	h := newHomeRig(t, b)
	v := h.loaded()
	if len(v.Entities) != 4096 || v.Entities[4095].ID != "sensor.s4095" {
		t.Fatalf("%d entities, last %s", len(v.Entities), v.Entities[len(v.Entities)-1].ID)
	}
	if len(v.Services) != 256 || len(v.Services["d001"]) != 64 {
		t.Fatalf("%d domains, %d services in d001", len(v.Services), len(v.Services["d001"]))
	}
	if _, ok := v.Services["d001"]["s63"]; !ok {
		t.Fatal("the services kept are not the first 64 home assistant listed")
	}
	f := v.Services["d000"]["many"].Fields
	if len(f) != 32 || f[29].Name != "f29" || f[30].Name != "g00" || f[31].Name != "g01" {
		t.Fatalf("%d fields: %+v", len(f), f)
	}
	// a new entity past the cap is not taken
	h.fake.Emit("state_changed", stateChanged("sensor.zzz", state("sensor.zzz", "1", nil)))
	h.fake.Emit("state_changed", stateChanged("sensor.s0000", state("sensor.s0000", "2", nil)))
	v = h.until("the update", func(v ha.View) bool { e, _ := v.ByID("sensor.s0000"); return e.State == "2" })
	if _, ok := v.ByID("sensor.zzz"); ok || len(v.Entities) != 4096 {
		t.Fatalf("%d entities after an add past the cap", len(v.Entities))
	}
}

func TestHomeReconnectReplacesTheSnapshot(t *testing.T) {
	b := baseHouse()
	h := newHomeRig(t, b)
	h.loaded()
	b.states = []any{state("light.cellar", "on", obj{"friendly_name": "Cellar"})}
	b.script(h.fake)
	h.fake.Drop()
	v := h.until("the new snapshot", func(v ha.View) bool { return v.Fresh && len(v.Entities) == 1 })
	if v.Entities[0].ID != "light.cellar" {
		t.Fatalf("entities %v", ids(v))
	}
	if n := h.logs.count("ha_loaded"); n != 2 {
		t.Fatalf("%d loads: %s", n, h.logs)
	}
}

func TestHomeServesAStaleViewForTenMinutes(t *testing.T) {
	h := newHomeRig(t, baseHouse())
	h.loaded()
	h.fake.Restart(time.Hour)
	h.until("not fresh", func(v ha.View) bool { return !v.Fresh })
	h.clock.Advance(9 * time.Minute)
	if v := h.view(); v.Fresh || len(v.Entities) != 7 {
		t.Fatalf("after 9 min: fresh %v, %d entities", v.Fresh, len(v.Entities))
	}
	h.clock.Advance(2 * time.Minute)
	if _, err := h.home.View(); !errors.Is(err, ha.ErrUnavailable) || ha.Code(err) != ha.CodeUnavailable {
		t.Fatalf("after 11 min: %v", err)
	}
	if _, ok := h.home.Wait(bg(), "light.kitchen", func(ha.Entity) bool { return true }, 10*time.Millisecond); ok {
		t.Fatal("wait saw an entity past the stale window")
	}
	h.fake.Restart(0)
	h.until("fresh again", func(v ha.View) bool { return v.Fresh })
}

func TestHomeWaitResolvesOnTheEvent(t *testing.T) {
	h := newHomeRig(t, baseHouse())
	h.loaded()
	off := func(e ha.Entity) bool { return e.State == "off" }
	type result struct {
		e  ha.Entity
		ok bool
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		e, ok := h.home.Wait(bg(), "light.kitchen", off, 5*time.Second)
		done <- result{e, ok}
	}()
	time.Sleep(20 * time.Millisecond)
	h.fake.Emit("state_changed", stateChanged("light.kitchen", state("light.kitchen", "off", nil)))
	select {
	case r := <-done:
		if !r.ok || r.e.State != "off" || time.Since(start) > 2*time.Second {
			t.Fatalf("wait %+v after %v", r, time.Since(start))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not resolve on the event")
	}
	// already true: at once
	if e, ok := h.home.Wait(bg(), "light.kitchen", off, time.Hour); !ok || e.ID != "light.kitchen" {
		t.Fatalf("already off: %+v %v", e, ok)
	}
	start = time.Now()
	if _, ok := h.home.Wait(bg(), "light.bedroom", func(e ha.Entity) bool { return e.State == "on" }, 50*time.Millisecond); ok ||
		time.Since(start) < 50*time.Millisecond {
		t.Fatalf("a condition never met resolved after %v", time.Since(start))
	}
	ctx, cancel := context.WithCancel(bg())
	cancel()
	if _, ok := h.home.Wait(ctx, "light.unknown", func(ha.Entity) bool { return true }, time.Hour); ok {
		t.Fatal("an absent entity satisfied the wait")
	}
}

func TestSlowRelistNeverBlocksTheReadLoop(t *testing.T) {
	b := baseHouse()
	h := newHomeRig(t, b)
	h.loaded()
	h.fake.Hold("config/area_registry/list")
	h.fake.Emit("area_registry_updated", obj{"action": "update", "area_id": "kitchen"})
	eventually(t, "the re-list held", func() bool { return h.fake.Held() == 1 })
	h.fake.Emit("state_changed", stateChanged("light.porch", state("light.porch", "off", nil)))
	h.until("a state change during the re-list", func(v ha.View) bool { e, _ := v.ByID("light.porch"); return e.State == "off" })
	h.fake.SetResult("get_config", obj{"version": "x"})
	if _, err := h.c.Command(bg(), "get_config", nil); err != nil {
		t.Fatalf("a command during the re-list: %v", err)
	}
	for range 5 {
		h.fake.Emit("area_registry_updated", obj{"action": "update", "area_id": "kitchen"})
	}
	b.areas = b.areas[:1]
	h.fake.SetResult("config/area_registry/list", b.areas)
	h.fake.Release()
	h.until("the areas", func(v ha.View) bool { return len(v.Areas) == 1 })
	// the load, the held re-list and one more for the five events behind it
	eventually(t, "three area lists", func() bool { return h.fake.Received("config/area_registry/list") == 3 })
	time.Sleep(50 * time.Millisecond)
	if n := h.fake.Received("config/area_registry/list"); n != 3 {
		t.Fatalf("%d area lists: events behind a running re-list coalesce into one", n)
	}
}

func TestHomeLoadFailureRetries(t *testing.T) {
	h := newHomeRig(t, baseHouse())
	h.fake.Fail("get_services", "home_assistant_error")
	h.run()
	eventually(t, "a failed load", func() bool {
		return strings.Contains(h.logs.String(), "msg=ha_reload_failed what=services code=ha_error:home_assistant_error")
	})
	if _, err := h.home.View(); !errors.Is(err, ha.ErrUnavailable) {
		t.Fatalf("view after a failed load: %v", err)
	}
	h.fake.Fail("get_services", "")
	h.until("loaded on a retry", func(v ha.View) bool { return v.Fresh && len(v.Entities) == 7 })
}

func TestHomeRelistFailureKeepsWhatItHad(t *testing.T) {
	h := newHomeRig(t, baseHouse())
	h.loaded()
	h.fake.Fail("config/entity_registry/list", "unknown_error")
	h.fake.Emit("entity_registry_updated", obj{"action": "update", "entity_id": "light.kitchen"})
	eventually(t, "the failed re-list", func() bool {
		return strings.Contains(h.logs.String(), "msg=ha_reload_failed what=entities code=ha_error:unknown_error")
	})
	if e, _ := h.view().ByID("light.kitchen"); e.Name != "Kitchen" || len(e.Aliases) != 2 {
		t.Fatalf("lost the registry: %+v", e)
	}
	if !h.c.Connected() {
		t.Fatal("a failed re-list closed the connection")
	}
}

// unknown are the ids of the entities whose platform the view does not know.
func unknown(v ha.View) []string {
	var out []string
	for _, e := range v.Entities {
		if e.PlatformUnknown {
			out = append(out, e.ID)
		}
	}
	return out
}

// m8-d6: home assistant lists a new entity in its registry before its first
// state, so while an entity re-list is pending, an entity with a state and no
// entry may be a Cadence device's own: its platform is unknown until the
// registry is listed again. A load is such a list too.
func TestHomeKnowsNoPlatformWhileTheRegistryIsBehind(t *testing.T) {
	b := baseHouse()
	h := newHomeRig(t, b)
	// light.porch has no entry: at rest that is all it is
	if got := unknown(h.loaded()); len(got) != 0 {
		t.Fatalf("unknown at rest: %v", got)
	}
	h.fake.Hold("config/entity_registry/list")
	h.fake.Emit("entity_registry_updated", obj{"action": "create", "entity_id": "number.speaker_volume"})
	h.fake.Emit("state_changed", stateChanged("number.speaker_volume", state("number.speaker_volume", "30", nil)))
	v := h.until("the new entity", func(v ha.View) bool { _, ok := v.ByID("number.speaker_volume"); return ok })
	if got := unknown(v); !slices.Equal(got, []string{"light.porch", "number.speaker_volume"}) {
		t.Fatalf("unknown while the re-list is pending: %v", got)
	}
	eventually(t, "the re-list in flight", func() bool { return h.fake.Held() == 1 })
	if got := unknown(h.view()); !slices.Equal(got, []string{"light.porch", "number.speaker_volume"}) {
		t.Fatalf("unknown while the re-list runs: %v", got)
	}
	b.states = append(b.states, state("number.speaker_volume", "30", nil))
	b.entities = append(b.entities, regEntry("number.speaker_volume", obj{"platform": "cadence"}))
	b.script(h.fake)
	h.fake.Release()
	v = h.until("the entity listed", func(v ha.View) bool { e, _ := v.ByID("number.speaker_volume"); return e.Platform == "cadence" })
	if got := unknown(v); len(got) != 0 {
		t.Fatalf("unknown once listed: %v", got)
	}
	if e, _ := v.ByID("light.kitchen"); e.Platform != "hue" || e.PlatformUnknown {
		t.Fatalf("a listed entity %+v", e)
	}

	h.fake.Hold("config/entity_registry/list")
	h.fake.Drop()
	eventually(t, "the reload in flight", func() bool { return h.fake.Held() == 1 })
	if got := unknown(h.view()); !slices.Equal(got, []string{"light.porch"}) {
		t.Fatalf("unknown while a load lists the registry: %v", got)
	}
	h.fake.Release()
	v = h.until("reloaded", func(v ha.View) bool { return v.Fresh && len(unknown(v)) == 0 })
	if e, _ := v.ByID("number.speaker_volume"); e.Platform != "cadence" {
		t.Fatalf("after the reload %+v", e)
	}
}

// m8-d6: a failed re-list is tried again with backoff, with no event asking
// for it; the entities without an entry stay unknown until one lands.
func TestHomeRetriesAFailedRelist(t *testing.T) {
	b := baseHouse()
	h := newHomeRig(t, b)
	ha.SetRetry(h.home, 20*time.Millisecond, 80*time.Millisecond)
	h.loaded()
	h.fake.Hold("config/entity_registry/list")
	h.fake.Fail("config/area_registry/list", "unknown_error")
	h.fake.Emit("area_registry_updated", obj{"action": "update", "area_id": "kitchen"})
	h.fake.Emit("entity_registry_updated", obj{"action": "create", "entity_id": "light.garden"})
	h.fake.Emit("state_changed", stateChanged("light.garden", state("light.garden", "on", nil)))
	eventually(t, "the re-list in flight", func() bool { return h.fake.Held() == 1 })
	h.fake.Fail("config/entity_registry/list", "unknown_error")
	h.fake.Release()
	eventually(t, "the failed re-list", func() bool {
		return strings.Contains(h.logs.String(), "msg=ha_reload_failed what=entities code=ha_error:unknown_error")
	})
	if got := unknown(h.view()); !slices.Equal(got, []string{"light.garden", "light.porch"}) {
		t.Fatalf("unknown after a failed re-list: %v", got)
	}
	eventually(t, "tried again", func() bool { return h.fake.Received("config/entity_registry/list") >= 3 })
	b.entities = append(b.entities, regEntry("light.garden", obj{"platform": "hue"}))
	b.areas = b.areas[:1]
	b.script(h.fake)
	h.fake.Fail("config/entity_registry/list", "")
	h.fake.Fail("config/area_registry/list", "")
	v := h.until("a retry landed", func(v ha.View) bool {
		e, _ := v.ByID("light.garden")
		return e.Platform == "hue" && len(v.Areas) == 1
	})
	if got := unknown(v); len(got) != 0 {
		t.Fatalf("unknown once a retry landed: %v", got)
	}
	n := h.fake.Received("config/entity_registry/list")
	time.Sleep(200 * time.Millisecond)
	if m := h.fake.Received("config/entity_registry/list"); m != n {
		t.Fatalf("%d more lists once the retry landed", m-n)
	}
	if !h.c.Connected() {
		t.Fatal("a failed re-list closed the connection")
	}
}

// m8-d6: only an entity registry event that can name an id the list lacks
// opens the window: a create, an update that changed an entity's id, or one it
// cannot read; a rename or a removal leaves the entities without an entry as
// they were at rest
func TestHomeOpensTheRegistryWindowOnlyForNewIDs(t *testing.T) {
	h := newHomeRig(t, baseHouse())
	h.loaded()
	for _, c := range []struct {
		ev    any
		opens bool
	}{
		{obj{"action": "update", "entity_id": "light.kitchen", "changes": obj{"name": "Kitchen"}}, false},
		{obj{"action": "update", "entity_id": "light.kitchen", "changes": obj{"name": "K"}, "old_entity_id": "light.kitchen"}, false},
		{obj{"action": "remove", "entity_id": "light.bedroom"}, false},
		{obj{"action": "create", "entity_id": "light.garden"}, true},
		{obj{"action": "update", "entity_id": "light.terrace", "changes": obj{"entity_id": "light.porch"}, "old_entity_id": "light.porch"}, true},
		{obj{"action": "migrate", "entity_id": "light.kitchen"}, true},
		{obj{"action": 1}, true},
	} {
		h.fake.Hold("config/entity_registry/list")
		h.fake.Emit("entity_registry_updated", c.ev)
		eventually(t, "the re-list in flight", func() bool { return h.fake.Held() == 1 })
		if got := unknown(h.view()); (len(got) != 0) != c.opens {
			t.Fatalf("%v: unknown %v", c.ev, got)
		}
		h.fake.Release()
		h.until("the list landed", func(v ha.View) bool { return len(unknown(v)) == 0 })
	}
}

// a failed re-list's retries end with the connection that queued them:
// neither its reconnect nor the load that follows sees another list of the
// old worker's
func TestHomeStopsRetryingARelistWithItsConnection(t *testing.T) {
	h := newHomeRig(t, baseHouse())
	ha.SetRetry(h.home, 20*time.Millisecond, 20*time.Millisecond)
	h.loaded()
	h.fake.Fail("config/entity_registry/list", "unknown_error")
	h.fake.Emit("entity_registry_updated", obj{"action": "create", "entity_id": "light.garden"})
	// the load's list, the event's, then retries
	eventually(t, "retried", func() bool { return h.fake.Received("config/entity_registry/list") >= 4 })
	h.fake.Hold("get_states")
	h.fake.Drop()
	eventually(t, "the reload waits", func() bool { return h.fake.Held() == 1 })
	n := h.fake.Received("config/entity_registry/list")
	time.Sleep(200 * time.Millisecond)
	if m := h.fake.Received("config/entity_registry/list"); m != n {
		t.Fatalf("%d lists after the connection ended", m-n)
	}
	h.fake.Fail("config/entity_registry/list", "")
	h.fake.Release()
	h.until("reloaded", func(v ha.View) bool { return v.Fresh && len(unknown(v)) == 0 })
}

func TestHomeRelistBackoff(t *testing.T) {
	h := newHomeRig(t, baseHouse())
	got := ha.RetryDelays(h.home, 7)
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	if !slices.Equal(got, want) {
		t.Fatalf("delays %v, want %v", got, want)
	}
}

func TestSelectorKinds(t *testing.T) {
	b := baseHouse()
	sel := func(s any) obj { return obj{"selector": s} }
	b.services = obj{"x": obj{"y": obj{"fields": obj{
		"num":       sel(obj{"number": obj{"min": 1.5, "max": 10}}),
		"num_unit":  sel(obj{"number": obj{"unit_of_measurement": "K"}}),
		"num_bare":  sel(obj{"number": nil}),
		"sel":       sel(obj{"select": obj{"options": []any{"a", obj{"value": "b", "label": "B"}, obj{"value": 3}, obj{"label": "x"}}}}),
		"sel_other": sel(obj{"select": obj{"options": "a,b"}}),
		"boolean":   sel(obj{"boolean": obj{}}),
		"text":      sel(obj{"text": obj{"multiline": true}}),
		"ct":        sel(obj{"color_temp": obj{"unit": "kelvin"}}),
		"rgb":       sel(obj{"rgb_color": obj{}}),
		"rgbw":      sel(obj{"rgbw_color": nil}),
		"xy":        sel(obj{"xy_color": nil}),
		"time":      sel(obj{"time": nil}),
		"date":      sel(obj{"date": nil}),
		"duration":  sel(obj{"duration": obj{}}),
		"entity":    sel(obj{"entity": obj{"domain": "light"}}),
		"area":      sel(obj{"area": obj{}}),
		"device":    sel(obj{"device": obj{"integration": "zha"}}),
		"floor":     sel(obj{"floor": nil}),
		"label":     sel(obj{"label": obj{}}),
		"target":    sel(obj{"target": obj{"entity": obj{"domain": "light"}}}),
		"object":    sel(obj{"object": obj{}}),
		"unknown":   sel(obj{"template": obj{}}),
		"none":      obj{"description": "no selector"},
		"quoted":    sel(obj{"select": obj{"options": []any{"say \"hi\"\n"}}}),
	}}}}
	h := newHomeRig(t, b)
	v := h.loaded()
	got, selectors := map[string]string{}, map[string]string{}
	for _, f := range v.Services["x"]["y"].Fields {
		got[f.Name] = f.Kind
		selectors[f.Name] = f.Selector
	}
	want := map[string]string{
		"num": "number 1.5..10", "num_unit": "number K", "num_bare": "", "sel": "one of a, b, 3, ?", "sel_other": "one of …",
		"boolean": "boolean", "text": "text", "ct": "color temperature", "rgb": "color", "rgbw": "color", "xy": "color",
		"time": "time", "date": "date", "duration": "duration", "entity": "entity id", "area": "area", "object": "object",
		"device": "", "floor": "", "label": "", "target": "", "unknown": "", "none": "", "quoted": "one of say hi",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("kinds\n got %v\nwant %v", got, want)
	}
	// the selector's own name, as home assistant wrote it, for the call's checks
	wantSelectors := map[string]string{
		"num": "number", "num_unit": "number", "num_bare": "number", "sel": "select", "sel_other": "select",
		"boolean": "boolean", "text": "text", "ct": "color_temp", "rgb": "rgb_color", "rgbw": "rgbw_color", "xy": "xy_color",
		"time": "time", "date": "date", "duration": "duration", "entity": "entity", "area": "area", "object": "object",
		"device": "device", "floor": "floor", "label": "label", "target": "target", "unknown": "template", "none": "",
		"quoted": "select",
	}
	if !reflect.DeepEqual(selectors, wantSelectors) {
		t.Fatalf("selectors\n got %v\nwant %v", selectors, wantSelectors)
	}
}

// get_entries only adds aliases: home assistant refusing it keeps the load
func TestHomeLoadsWithoutAliasesWhenTheirListFails(t *testing.T) {
	h := newHomeRig(t, baseHouse())
	h.fake.Fail("config/entity_registry/get_entries", "unknown_error")
	v := h.loaded()
	if e, _ := v.ByID("light.kitchen"); e.Name != "Kitchen" || e.Area != "kitchen" || len(e.Aliases) != 0 {
		t.Fatalf("kitchen %+v", e)
	}
	if !strings.Contains(h.logs.String(), "msg=ha_reload_failed what=aliases code=ha_error:unknown_error") {
		t.Fatalf("logs %s", h.logs)
	}
	if n := h.logs.count("ha_loaded"); n != 1 {
		t.Fatalf("%d loads", n)
	}
}

// a registry past the entity cap never costs an entity with a state its aliases
func TestHomeAsksAliasesOnlyForEntitiesWithStates(t *testing.T) {
	b := baseHouse()
	for i := range 4100 {
		b.entities = append(b.entities, regEntry(fmt.Sprintf("button.b%04d", i), nil))
	}
	h := newHomeRig(t, b)
	v := h.loaded()
	if e, _ := v.ByID("light.kitchen"); !slices.Equal(e.Aliases, []string{"big light", "верхний свет"}) {
		t.Fatalf("kitchen %+v", e)
	}
}

// home assistant's startup fires registry events in bursts: one re-list a burst
func TestHomeRelistsOnceAQuietPeriod(t *testing.T) {
	b := baseHouse()
	h := newHomeRig(t, b)
	ha.SetQuiet(h.home, 300*time.Millisecond)
	h.loaded()
	b.areas = b.areas[:1]
	h.fake.SetResult("config/area_registry/list", b.areas)
	for range 5 {
		h.fake.Emit("area_registry_updated", obj{"action": "update", "area_id": "kitchen"})
	}
	time.Sleep(50 * time.Millisecond)
	if n := h.fake.Received("config/area_registry/list"); n != 1 {
		t.Fatalf("%d area lists before the quiet period passed", n)
	}
	h.until("the areas", func(v ha.View) bool { return len(v.Areas) == 1 })
	time.Sleep(400 * time.Millisecond)
	if n := h.fake.Received("config/area_registry/list"); n != 2 {
		t.Fatalf("%d area lists: a burst is one re-list", n)
	}
}

// the states that changed while a load failed are not lost from the view
// served stale meanwhile
func TestHomeFailedLoadKeepsTheChangesItSaw(t *testing.T) {
	h := newHomeRig(t, baseHouse())
	h.loaded()
	h.fake.Hold("get_services")
	h.fake.Drop()
	eventually(t, "the reload waiting", func() bool { return h.fake.Held() == 1 })
	h.fake.Emit("state_changed", stateChanged("light.kitchen", state("light.kitchen", "off", nil)))
	h.fake.Fail("get_services", "home_assistant_error")
	h.fake.Release()
	eventually(t, "the failed load", func() bool {
		return strings.Contains(h.logs.String(), "msg=ha_reload_failed what=services code=ha_error:home_assistant_error")
	})
	v := h.view()
	if e, _ := v.ByID("light.kitchen"); v.Fresh || e.State != "off" || e.Name != "Kitchen" {
		t.Fatalf("fresh %v, kitchen %+v", v.Fresh, e)
	}
}

// a connection that drops mid-load is reported once, by the client
func TestHomeLogsNoReloadFailureForADroppedConnection(t *testing.T) {
	h := newHomeRig(t, baseHouse())
	h.fake.Hold("get_services")
	h.run()
	eventually(t, "the load waiting", func() bool { return h.fake.Held() == 1 })
	h.fake.Drop()
	eventually(t, "the next load waiting", func() bool { return h.fake.Held() == 2 })
	h.fake.Release()
	h.until("loaded", func(v ha.View) bool { return v.Fresh })
	if strings.Contains(h.logs.String(), "ha_reload_failed") {
		t.Fatalf("logs %s", h.logs)
	}
}
