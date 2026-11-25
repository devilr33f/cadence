package ha_test

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha/hatest"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// house is what the fake answers to the load commands.
type house struct {
	states   []any
	services any
	areas    []any
	devices  []any
	entities []any
	// entries is config/entity_registry/get_entries by entity id
	entries map[string]any
}

func (h house) script(f *hatest.Server) {
	f.SetResult("get_states", h.states)
	f.SetResult("get_services", h.services)
	f.SetResult("config/area_registry/list", h.areas)
	f.SetResult("config/device_registry/list", h.devices)
	f.SetResult("config/entity_registry/list", h.entities)
	f.SetResult("config/entity_registry/get_entries", h.entries)
}

type obj = map[string]any

func state(id, st string, attrs obj) obj {
	m := obj{"entity_id": id, "state": st, "last_changed": "2026-11-09T08:00:00+00:00", "context": obj{"id": "c"}}
	if attrs != nil {
		m["attributes"] = attrs
	}
	return m
}

func regEntry(id string, f obj) obj {
	m := obj{
		"entity_id": id, "name": nil, "original_name": nil, "area_id": nil, "device_id": nil,
		"disabled_by": nil, "hidden_by": nil, "platform": "demo", "id": "x" + id, "labels": []string{},
	}
	for k, v := range f {
		m[k] = v
	}
	return m
}

// baseHouse is a small home: names from each source, an area from the
// entity and one from its device, a disabled and a hidden entity, null and
// unlisted attributes, a service with a section.
func baseHouse() house {
	return house{
		states: []any{
			state("light.kitchen", "on", obj{
				"friendly_name": "Kitchen ceiling", "brightness": 128, "color_temp_kelvin": nil,
				"entity_picture": "/api/x", "supported_color_modes": []string{"brightness"},
			}),
			state("light.bedroom", "off", obj{"friendly_name": "Bedroom lamp", "brightness": nil}),
			state("switch.fan", "on", obj{"friendly_name": "Fan plug"}),
			state("sensor.old", "12", obj{"friendly_name": "Retired sensor"}),
			state("climate.ac", "cool", obj{
				"friendly_name": "AC", "temperature": 26, "current_temperature": 24.5, "hvac_action": "cooling",
				"hvac_modes": []string{"off", "cool"},
			}),
			state("light.porch", "on", nil),
			state("light.hall", "off", obj{"friendly_name": nil}),
			state("cover.garage", "closed", obj{"friendly_name": "Garage door", "device_class": "garage"}),
		},
		// raw, so the fields keep home assistant's order
		services: json.RawMessage(`{
			"light": {
				"turn_on": {"name": "Turn on", "description": "Turn a light on", "fields": {
					"transition": {"selector": {"number": {"min": 0, "max": 300, "unit_of_measurement": "seconds"}}},
					"brightness_pct": {"description": "Percent", "selector": {"number": {"min": 0, "max": 100, "unit_of_measurement": "%"}}},
					"advanced_fields": {"collapsed": true, "fields": {"flash": {"selector": {"select": {"options": ["short", "long"]}}}}}
				}},
				"turn_off": {"fields": {}}
			},
			"homeassistant": {"turn_on": {"fields": {}}, "turn_off": {"fields": {}}, "toggle": {"fields": {}}},
			"climate": {"set_temperature": {"fields": {
				"temperature": {"required": true, "description": "Target", "selector": {"number": {"min": 7, "max": 35}}}
			}}}
		}`),
		areas: []any{
			obj{"area_id": "kitchen", "name": "Kitchen", "aliases": []string{"кухня"}, "floor_id": nil, "icon": nil, "labels": []string{}},
			obj{"area_id": "bedroom", "name": "Bedroom", "aliases": []string{}, "floor_id": "up"},
		},
		devices: []any{
			obj{"id": "devk", "area_id": "kitchen", "name": "Hue bridge", "manufacturer": "x"},
			obj{"id": "devg", "area_id": nil, "name": "Opener"},
		},
		entities: []any{
			regEntry("light.kitchen", obj{"name": "Kitchen", "original_name": "Ceiling", "device_id": "devk", "platform": "hue"}),
			regEntry("light.bedroom", obj{"original_name": "Bedroom original", "area_id": "bedroom", "device_id": "devk"}),
			regEntry("switch.fan", obj{"area_id": "kitchen", "hidden_by": "user"}),
			regEntry("sensor.old", obj{"disabled_by": "user"}),
			regEntry("light.hall", obj{"original_name": "Hall light"}),
			regEntry("climate.ac", obj{"name": ""}),
			regEntry("cover.garage", obj{"device_id": "devg"}),
		},
		entries: map[string]any{
			"light.kitchen": obj{"entity_id": "light.kitchen", "aliases": []any{"big light", nil, strings.Repeat("x", 101), "верхний свет"}},
			"light.bedroom": obj{"entity_id": "light.bedroom", "aliases": []any{}},
			"sensor.old":    obj{"entity_id": "sensor.old", "aliases": []any{"retired alias"}},
		},
	}
}

// houseText is every id, name, alias and area of baseHouse, none of which
// may reach a log.
var houseText = []string{
	"light.kitchen", "light.bedroom", "switch.fan", "sensor.old", "climate.ac", "light.porch", "light.hall", "cover.garage",
	"Kitchen", "Ceiling", "Bedroom", "Fan plug", "Retired", "Hall light", "Garage door", "кухня", "big light", "верхний свет",
	"kitchen", "bedroom", "devk", "devg", "Turn a light on", "Percent",
}

type homeRig struct {
	*rig
	home  *ha.Home
	clock *clock
}

// newHomeRig scripts h into a fresh fake and builds a Home on the rig's
// client; logs are checked for houseText at cleanup.
func newHomeRig(t *testing.T, h house, opts ...rigOpt) *homeRig {
	t.Helper()
	r := newRig(t, opts...)
	h.script(r.fake)
	clk := &clock{t: time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)}
	home := ha.NewHome(r.c, ha.HomeOptions{Now: clk.Now, Log: r.log})
	ha.SetQuiet(home, 20*time.Millisecond)
	t.Cleanup(func() {
		r.stop()
		out := r.logs.String()
		for _, s := range houseText {
			if strings.Contains(out, s) {
				t.Errorf("a log line carries %q:\n%s", s, out)
			}
		}
	})
	return &homeRig{rig: r, home: home, clock: clk}
}

// loaded runs the client and waits for the first view.
func (h *homeRig) loaded() ha.View {
	h.t.Helper()
	h.run()
	var v ha.View
	eventually(h.t, "loaded", func() bool {
		var err error
		v, err = h.home.View()
		return err == nil && v.Fresh
	})
	return v
}

func (h *homeRig) view() ha.View {
	h.t.Helper()
	v, err := h.home.View()
	if err != nil {
		h.t.Fatalf("view: %v", err)
	}
	return v
}

// until waits for a view that satisfies cond.
func (h *homeRig) until(what string, cond func(ha.View) bool) ha.View {
	h.t.Helper()
	var v ha.View
	eventually(h.t, what, func() bool {
		var err error
		v, err = h.home.View()
		return err == nil && cond(v)
	})
	return v
}

func ptr[T any](v T) *T { return &v }

func ids(v ha.View) []string {
	out := make([]string, len(v.Entities))
	for i, e := range v.Entities {
		out[i] = e.ID
	}
	return out
}

func stateChanged(id string, newState any) obj {
	return obj{"entity_id": id, "old_state": nil, "new_state": newState}
}
