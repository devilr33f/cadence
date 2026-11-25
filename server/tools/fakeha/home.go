package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha/hatest"
)

// services are the ones the ha tools call on this home, in Home Assistant's
// get_services shape
const services = `{
	"homeassistant": {
		"turn_on": {"name": "Generic turn on", "description": "Turns devices on under any domain.", "fields": {}},
		"turn_off": {"name": "Generic turn off", "description": "Turns devices off under any domain.", "fields": {}},
		"toggle": {"name": "Generic toggle", "description": "Toggles devices under any domain.", "fields": {}}
	},
	"light": {
		"turn_on": {"name": "Turn on", "description": "Turns on one or more lights.", "fields": {
			"brightness_pct": {"selector": {"number": {"min": 0, "max": 100, "unit_of_measurement": "%"}}},
			"color_temp_kelvin": {"selector": {"color_temp": {"unit": "kelvin", "min": 2000, "max": 6500}}}
		}},
		"turn_off": {"name": "Turn off", "description": "Turns off one or more lights.", "fields": {}},
		"toggle": {"name": "Toggle", "description": "Toggles one or more lights.", "fields": {}}
	},
	"lock": {
		"lock": {"name": "Lock", "description": "Locks a lock.", "fields": {"code": {"selector": {"text": {}}}}},
		"unlock": {"name": "Unlock", "description": "Unlocks a lock.", "fields": {"code": {"selector": {"text": {}}}}}
	}
}`

type obj = map[string]any

// state is one entity's state object, as get_states and state_changed carry it.
type state struct {
	EntityID    string `json:"entity_id"`
	State       string `json:"state"`
	Attributes  obj    `json:"attributes"`
	LastChanged string `json:"last_changed"`
	LastUpdated string `json:"last_updated"`
	Context     obj    `json:"context"`
}

// house is the e2e's scripted home on hatest's fake: the hall light (on) and
// the hall's temperature sensor (21.5 °C, in the hall through its device) in
// the one area, hall, and the front door lock (locked, no area). A call the
// fake answered switches the light or the lock and emits state_changed, as
// Home Assistant does, and is written to calls as one line `domain service
// entity_id`. The log carries domains and services only.
type house struct {
	fake *hatest.Server
	log  *slog.Logger
	now  func() time.Time

	mu     sync.Mutex
	states map[string]state
	calls  io.Writer
}

func newHouse(token string, calls io.Writer, log *slog.Logger, now func() time.Time) *house {
	h := &house{fake: hatest.NewHandler(token), log: log, now: now, calls: calls, states: map[string]state{}}
	at := now().UTC().Format(time.RFC3339Nano)
	for _, s := range []state{
		{EntityID: "light.hall", State: "on", Attributes: obj{
			"friendly_name": "Hall light", "supported_color_modes": []string{"onoff"}, "color_mode": "onoff",
		}},
		{EntityID: "lock.front", State: "locked", Attributes: obj{"friendly_name": "Front door"}},
		{EntityID: "sensor.hall_temperature", State: "21.5", Attributes: obj{
			"friendly_name": "Hall temperature", "unit_of_measurement": "°C", "device_class": "temperature",
			"state_class": "measurement",
		}},
	} {
		s.LastChanged, s.LastUpdated, s.Context = at, at, obj{"id": "fake-context"}
		h.states[s.EntityID] = s
	}
	f := h.fake
	f.SetResult("get_states", h.list())
	f.SetResult("get_services", json.RawMessage(services))
	f.SetResult("config/area_registry/list", []obj{{"area_id": "hall", "name": "Hall", "aliases": []string{}}})
	f.SetResult("config/device_registry/list", []obj{{"id": "hall_sensor_device", "area_id": "hall"}})
	f.SetResult("config/entity_registry/list", []obj{
		registered("light.hall", "Hall light", "hall", nil),
		registered("lock.front", "Front door", nil, nil),
		registered("sensor.hall_temperature", "Hall temperature", nil, "hall_sensor_device"),
	})
	f.SetResult("config/entity_registry/get_entries", obj{
		"light.hall":              obj{"entity_id": "light.hall", "aliases": []string{}},
		"lock.front":              obj{"entity_id": "lock.front", "aliases": []string{}},
		"sensor.hall_temperature": obj{"entity_id": "sensor.hall_temperature", "aliases": []string{}},
	})
	f.OnCall(h.apply)
	return h
}

func registered(id, originalName string, area, device any) obj {
	return obj{
		"entity_id": id, "name": nil, "original_name": originalName, "area_id": area, "device_id": device,
		"disabled_by": nil,
	}
}

// list is the states sorted by entity id; h.mu held or not yet shared.
func (h *house) list() []state {
	out := make([]state, 0, len(h.states))
	for _, id := range slices.Sorted(maps.Keys(h.states)) {
		out = append(out, h.states[id])
	}
	return out
}

// word keeps a call's names to what Home Assistant's own are made of, so a
// line of calls stays one line
var word = regexp.MustCompile(`^[a-z0-9_.]{1,128}$`)

func clean(s string) string {
	if !word.MatchString(s) {
		return "?"
	}
	return s
}

// apply records c and makes the change it asks of its target, if any.
func (h *house) apply(c hatest.Call) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id, _ := c.Target["entity_id"].(string)
	target := "-"
	if id != "" {
		target = clean(id)
	}
	_, _ = fmt.Fprintf(h.calls, "%s %s %s\n", clean(c.Domain), clean(c.Service), target)
	h.log.Info("call", "domain", clean(c.Domain), "service", clean(c.Service))
	old, ok := h.states[id]
	if !ok {
		return
	}
	next := switched(old, c.Domain, c.Service)
	if next == old.State {
		return
	}
	s := old
	at := h.now().UTC().Format(time.RFC3339Nano)
	s.State, s.LastChanged, s.LastUpdated = next, at, at
	h.states[id] = s
	h.fake.SetResult("get_states", h.list())
	h.fake.Emit("state_changed", obj{"entity_id": id, "old_state": old, "new_state": s})
}

// switched is the state a service leaves s in: a light switches through its
// own domain or the generic one, a lock through its own.
func switched(s state, domain, service string) string {
	kind, _, _ := strings.Cut(s.EntityID, ".")
	switch {
	case kind == "light" && (domain == "light" || domain == "homeassistant"):
		switch service {
		case "turn_on":
			return "on"
		case "turn_off":
			return "off"
		case "toggle":
			if s.State == "on" {
				return "off"
			}
			return "on"
		}
	case kind == "lock" && domain == "lock":
		switch service {
		case "lock":
			return "locked"
		case "unlock":
			return "unlocked"
		}
	}
	return s.State
}
