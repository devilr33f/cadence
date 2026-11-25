package tools

import (
	"math"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
)

// haCall is one service call; data nil sends no service data.
type haCall struct {
	domain, service string
	data            map[string]any
}

// heaterModes are the operation modes a water heater heats in, best first,
// when its supported_features has no on/off of its own.
var heaterModes = []string{"electric", "heat_pump", "eco", "performance", "on"}

// heaterOn says whether a water heater is heating: any operation mode but off.
func heaterOn(e ha.Entity) bool {
	switch e.State {
	case "off", "unavailable", "unknown", "":
		return false
	}
	return true
}

// heatMode is the operation mode to heat in: the one it already shows when it
// heats, else the best of its operation_list, never off.
func heatMode(e ha.Entity) (string, bool) {
	modes := e.Attrs.OperationList
	if heaterOn(e) {
		for _, m := range modes {
			if m == e.State {
				return m, true
			}
		}
	}
	for _, want := range heaterModes {
		for _, m := range modes {
			if m == want {
				return m, true
			}
		}
	}
	for _, m := range modes {
		if m != "off" {
			return m, true
		}
	}
	return "", false
}

// fixedCall is the call of a fixed action on e; ok is false when e's domain
// cannot take it. A water heater without the on/off feature is switched by
// its operation mode, as home assistant's own dialog does.
func fixedCall(a controlArgs, e ha.Entity) (haCall, bool) {
	switch a.action {
	case "turn_on", "turn_off", "toggle":
		if e.Domain == "water_heater" && !e.HasFeature(ha.WaterHeaterOnOff) {
			return heaterCall(a, e)
		}
		// the homeassistant domain forwards to the entity's own one
		return haCall{"homeassistant", a.action, nil}, true
	case "set_brightness":
		return haCall{"light", "turn_on", map[string]any{"brightness_pct": a.brightness}}, e.Domain == "light"
	case "set_color_temp":
		return haCall{"light", "turn_on", map[string]any{"color_temp_kelvin": a.kelvin}}, e.Domain == "light"
	case "set_temperature":
		return haCall{e.Domain, "set_temperature", map[string]any{"temperature": a.temperature}},
			e.Domain == "climate" || e.Domain == "water_heater"
	case "set_hvac_mode":
		return haCall{"climate", "set_hvac_mode", map[string]any{"hvac_mode": a.mode}}, e.Domain == "climate"
	}
	return haCall{}, false
}

// heaterCall is a water heater's switch by operation mode. turn_on of one
// already heating never reaches here: run answers it without a call.
func heaterCall(a controlArgs, e ha.Entity) (haCall, bool) {
	mode := "off"
	if !heaterOn(e) && a.action != "turn_off" {
		m, ok := heatMode(e)
		if !ok {
			return haCall{}, false
		}
		mode = m
	}
	return haCall{"water_heater", "set_operation_mode", map[string]any{"operation_mode": mode}}, true
}

var serviceRe = regexp.MustCompile(`^(?:([a-z][a-z0-9_]*)\.)?([a-z][a-z0-9_]*)$`)

// targetKeys are what Home Assistant reads as a call's target in its data:
// one in the fields would reach past the resolved entity, its policy and
// its usage.
var targetKeys = []string{"entity_id", "area_id", "device_id", "floor_id", "label_id"}

// genericServices are the homeassistant services a device may be called
// with: they switch it, where the others act on Home Assistant itself
// (stop, restart, set_location; Ruling M5c-R5).
var genericServices = map[string]bool{"turn_on": true, "turn_off": true, "toggle": true}

// serviceCall is a service e's domain or the generic homeassistant one
// offers, its fields checked against Home Assistant's own description, so a
// made-up field never reaches it; the model never sets a target itself.
func serviceCall(svcs map[string]map[string]ha.Service, e ha.Entity, service string, fields map[string]any) (haCall, *agent.ToolError) {
	m := serviceRe.FindStringSubmatch(service)
	if m == nil {
		return haCall{}, notSupported("call_service")
	}
	prefix, name := m[1], m[2]
	if prefix != "" && prefix != e.Domain && prefix != "homeassistant" {
		return haCall{}, notSupported("call_service")
	}
	for _, key := range targetKeys {
		if _, ok := fields[key]; ok {
			return haCall{}, badArgs(key + " is set by the tool; never pass it as a field")
		}
	}
	domains := []string{e.Domain, "homeassistant"}
	if prefix != "" {
		domains = []string{prefix}
	}
	for _, domain := range domains {
		svc, ok := svcs[domain][name]
		if !ok || domain == "homeassistant" && !genericServices[name] {
			continue
		}
		// generic services forward their data to the entity's own service of
		// that name: its fields may name others, and the rest goes unchecked,
		// since the generic description has none
		own := svc
		if domain == "homeassistant" {
			own = svcs[e.Domain][name]
		}
		if err := namesOthers(own, fields); err != nil {
			return haCall{}, err
		}
		if domain != "homeassistant" {
			if err := checkFields(svc, fields); err != nil {
				return haCall{}, err
			}
		}
		return haCall{domain, name, fields}, nil
	}
	return haCall{}, notSupported("call_service")
}

// otherSelectors are the selectors whose value names other entities, devices
// or places: such a field would reach past the one resolved entity, its
// policy and its usage (Ruling M5c-R9).
var otherSelectors = map[string]bool{
	"entity": true, "device": true, "area": true, "floor": true, "label": true, "target": true,
}

// namesOthers refuses a service field that names other devices, given or
// required: a service that needs one never runs on one device alone.
func namesOthers(svc ha.Service, fields map[string]any) *agent.ToolError {
	for _, f := range svc.Fields {
		if _, given := fields[f.Name]; otherSelectors[f.Selector] && (given || f.Required) {
			return badArgs(f.Name + " names other devices; the tool acts on one device only")
		}
	}
	return nil
}

// notSupported refuses an action e's domain cannot take; only call_service
// is pointed at ha_inspect, a fixed action says what takes it.
func notSupported(action string) *agent.ToolError {
	hint := "Home Assistant has no such service for this device; tell the user it can't."
	switch action {
	case "call_service":
		hint = "Tell the user; ha_inspect lists what the device can do."
	case "set_brightness":
		hint = "Only lights take a brightness; tell the user this device can't."
	case "set_color_temp":
		hint = "Only lights take a color temperature; tell the user this device can't."
	case "set_temperature":
		hint = "Only climate and water heater devices take a target temperature; tell the user this device can't."
	case "set_hvac_mode":
		hint = "Only climate devices take an hvac mode; tell the user this device can't."
	}
	return &agent.ToolError{Code: "not_supported", Message: "This device can't do that.", Hint: hint}
}

// refusedData answers Home Assistant refusing a call's data.
func refusedData(action string) *agent.ToolError {
	hint := "Tell the user it did not work."
	switch action {
	case "call_service":
		hint = "Check the fields with ha_inspect and call again, or tell the user it did not work."
	case "set_brightness", "set_color_temp", "set_temperature":
		hint = "The value may be out of the device's range; tell the user it did not work."
	case "set_hvac_mode":
		hint = "The device may not have that mode; tell the user it did not work."
	}
	return &agent.ToolError{Code: "invalid_arguments", Message: "Home Assistant refused the service data.", Hint: hint, Retryable: true}
}

func checkFields(svc ha.Service, fields map[string]any) *agent.ToolError {
	known := map[string]bool{}
	var missing []string
	for _, f := range svc.Fields {
		known[f.Name] = true
		if _, ok := fields[f.Name]; f.Required && !ok {
			missing = append(missing, f.Name)
		}
	}
	var unknown []string
	for k := range fields {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	slices.Sort(unknown)
	switch {
	case len(unknown) > 0:
		return badArgs("unknown fields: " + strings.Join(unknown, ", "))
	case len(missing) > 0:
		return badArgs("missing required fields: " + strings.Join(missing, ", "))
	}
	return nil
}

// canTake narrows a name several entities share (a bulb and its firmware
// update entity) to the ones whose domain can do a; on, off and toggle ask
// the entity's own domain, not the generic one that takes anything. A water
// heater without the on/off feature switches by its operation mode instead.
func canTake(a controlArgs, e ha.Entity, svcs map[string]map[string]ha.Service) bool {
	switch a.action {
	case "call_service":
		_, err := serviceCall(svcs, e, a.service, a.fields)
		return err == nil || err.Code != "not_supported"
	case "turn_on", "turn_off", "toggle":
		if _, ok := svcs[e.Domain][a.action]; ok {
			return true
		}
		_, byMode := svcs["water_heater"]["set_operation_mode"]
		return e.Domain == "water_heater" && !e.HasFeature(ha.WaterHeaterOnOff) && byMode
	}
	_, ok := fixedCall(a, e)
	return ok
}

// readsOn says whether e's state reads as on for its domain: a water heater
// heats in any operation mode but off, a cover is on when it opens, a player
// when it plays or stands ready.
func readsOn(e ha.Entity) bool {
	switch e.Domain {
	case "water_heater":
		return heaterOn(e)
	case "cover":
		return e.State == "open" || e.State == "opening"
	case "media_player":
		return e.State == "on" || e.State == "playing" || e.State == "idle" || e.State == "paused"
	}
	return e.State == "on"
}

// readsOff says whether e's state reads as off for its domain.
func readsOff(e ha.Entity) bool {
	if e.Domain == "cover" {
		return e.State == "closed" || e.State == "closing"
	}
	return e.State == "off"
}

// gone is a state that never shows an effect: the entity dropped out rather
// than changed.
func gone(e ha.Entity) bool {
	return e.State == "unavailable" || e.State == "unknown" || e.State == "none"
}

// shown reports when the cache shows what a did to the entity that was
// before: the state or the attribute it sets reached its value or moved.
// A toggle counts only once the state truly turned.
func shown(a controlArgs, before ha.Entity) func(ha.Entity) bool {
	moved := func(e ha.Entity) bool { return e.State != before.State }
	turned := func(e ha.Entity) bool { return moved(e) && !gone(e) }
	switch a.action {
	case "turn_on":
		return func(e ha.Entity) bool { return readsOn(e) || turned(e) }
	case "turn_off":
		return func(e ha.Entity) bool { return readsOff(e) || turned(e) }
	case "toggle":
		return func(e ha.Entity) bool { return (readsOn(e) || readsOff(e)) && turned(e) }
	case "set_hvac_mode":
		return func(e ha.Entity) bool { return e.State == a.mode || moved(e) }
	case "set_brightness":
		// home assistant scales the percent to 0-255
		return setTo(before, func(at ha.Attrs) *float64 { return at.Brightness }, float64(a.brightness)*255/100, 1.5)
	case "set_color_temp":
		return setTo(before, func(at ha.Attrs) *float64 { return at.ColorTempKelvin }, float64(a.kelvin), 0)
	case "set_temperature":
		return setTo(before, func(at ha.Attrs) *float64 { return at.Temperature }, a.temperature, 0)
	}
	return func(e ha.Entity) bool { return moved(e) || !reflect.DeepEqual(e.Attrs, before.Attrs) }
}

func setTo(before ha.Entity, attr func(ha.Attrs) *float64, want, within float64) func(ha.Entity) bool {
	was := attr(before.Attrs)
	return func(e ha.Entity) bool {
		now := attr(e.Attrs)
		switch {
		case e.State != before.State:
			return true
		case now == nil:
			return was != nil
		}
		return math.Abs(*now-want) <= within || was == nil || *now != *was
	}
}
