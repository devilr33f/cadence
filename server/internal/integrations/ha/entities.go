package ha

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

// limits on what is kept from Home Assistant, as v1 had them
const (
	maxEntities    = 4096
	maxEntityID    = 128
	maxDomain      = 64
	maxState       = 255
	maxName        = 100
	maxDescription = 500
	maxEnum        = 32
	maxAliases     = 32
	maxRegistryID  = 128
	maxBrightness  = 255
	maxPercentage  = 100
	maxModes       = 8
	maxModeRunes   = 32
)

// WaterHeaterOnOff is the water heater supported_features bit that lets the
// domain's own turn_on/turn_off switch it; without it a heater is switched by
// its operation mode.
const WaterHeaterOnOff = 8

// HasFeature reports whether the entity's supported_features has the bit.
func (e Entity) HasFeature(bit int) bool {
	f := e.Attrs.SupportedFeatures
	return f != nil && int64(*f)&int64(bit) != 0
}

var (
	entityIDRe = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z0-9_]+$`)
	domainRe   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	// area ids are slugs and device ids hex, both of these characters
	registryIDRe = regexp.MustCompile(`^[a-z0-9_]+$`)
)

// Entity is one Home Assistant entity as the cache keeps it. Name, Area,
// Aliases and State are Home Assistant's text: sanitize them (Sanitize,
// ModelState) before the model sees them, and never log them.
type Entity struct {
	ID, Domain string
	// Platform is the integration that provides it, from the registry;
	// empty for an entity the registry does not know
	Platform string
	// PlatformUnknown is true when the registry cannot say which integration
	// provides it: its entry could not be read, or it has none while an
	// entity re-list is pending, running or failed (home assistant lists a
	// new entity in its registry before its first state). It may be any
	// integration's, Cadence's own included.
	PlatformUnknown bool
	// EntryUnreadable is true when the platform is unknown because its entry
	// could not be read: that lasts until Home Assistant's entry changes
	EntryUnreadable bool
	// Name is the registry name, else friendly_name, else the registry's
	// original_name, else the id
	Name string
	// Area is an area id: the entity's own, else its device's; empty when none
	Area    string
	Aliases []string
	State   string
	Attrs   Attrs
}

// Attrs are the only attributes kept; nil is absent (Home Assistant's null
// included).
type Attrs struct {
	Brightness         *float64 // 0-255
	ColorTempKelvin    *float64
	Temperature        *float64
	CurrentTemperature *float64
	// MinTemp and MaxTemp bound a climate or water heater's target temperature
	MinTemp *float64
	MaxTemp *float64
	// TargetTempLow and TargetTempHigh are a range climate's target window
	TargetTempLow  *float64
	TargetTempHigh *float64
	// TemperatureUnit is a climate or water heater's ("°C", "°F"); empty when absent
	TemperatureUnit *string
	// SupportedFeatures is the entity's supported_features bit set, when it has one
	SupportedFeatures *float64
	// OperationList is a water heater's operation modes, when it has them
	OperationList     []string
	HVACAction        *string
	OperationMode     *string
	UnitOfMeasurement *string
	DeviceClass       *string
	Percentage        *float64 // 0-100
}

// entityState is a parsed state object: what get_states lists and a
// state_changed event carries.
type entityState struct {
	id, state, friendly string
	attrs               Attrs
}

func validEntityID(id string) bool {
	return len(id) <= maxEntityID && entityIDRe.MatchString(id) && strings.IndexByte(id, '.') <= maxDomain
}

func domainOf(id string) string { return id[:strings.IndexByte(id, '.')] }

// parseState reads one state object; ok is false when it is malformed, which
// drops the entity. A null attribute is absent.
func parseState(raw json.RawMessage) (entityState, bool) {
	var s struct {
		EntityID   *string                    `json:"entity_id"`
		State      *string                    `json:"state"`
		Attributes map[string]json.RawMessage `json:"attributes"`
	}
	if json.Unmarshal(raw, &s) != nil || s.EntityID == nil || s.State == nil ||
		!validEntityID(*s.EntityID) || utf8.RuneCountInString(*s.State) > maxState {
		return entityState{}, false
	}
	e := entityState{id: *s.EntityID, state: *s.State}
	a := s.Attributes
	ok := text(a["friendly_name"], maxName, &e.friendly) &&
		number(a["brightness"], 0, maxBrightness, &e.attrs.Brightness) &&
		number(a["color_temp_kelvin"], math.Inf(-1), math.Inf(1), &e.attrs.ColorTempKelvin) &&
		number(a["temperature"], math.Inf(-1), math.Inf(1), &e.attrs.Temperature) &&
		number(a["current_temperature"], math.Inf(-1), math.Inf(1), &e.attrs.CurrentTemperature) &&
		number(a["min_temp"], math.Inf(-1), math.Inf(1), &e.attrs.MinTemp) &&
		number(a["max_temp"], math.Inf(-1), math.Inf(1), &e.attrs.MaxTemp) &&
		number(a["target_temp_low"], math.Inf(-1), math.Inf(1), &e.attrs.TargetTempLow) &&
		number(a["target_temp_high"], math.Inf(-1), math.Inf(1), &e.attrs.TargetTempHigh) &&
		enum(a["temperature_unit"], &e.attrs.TemperatureUnit) &&
		number(a["supported_features"], 0, math.MaxFloat64, &e.attrs.SupportedFeatures) &&
		modes(a["operation_list"], &e.attrs.OperationList) &&
		enum(a["hvac_action"], &e.attrs.HVACAction) &&
		enum(a["operation_mode"], &e.attrs.OperationMode) &&
		enum(a["unit_of_measurement"], &e.attrs.UnitOfMeasurement) &&
		enum(a["device_class"], &e.attrs.DeviceClass) &&
		number(a["percentage"], 0, maxPercentage, &e.attrs.Percentage)
	return e, ok
}

// parseStates reads get_states: the first maxEntities well-formed states, in
// Home Assistant's order.
func parseStates(raw json.RawMessage) (map[string]entityState, error) {
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return nil, fail(CodeBadResponse)
	}
	out := make(map[string]entityState, min(len(list), maxEntities))
	for _, item := range list {
		if len(out) == maxEntities {
			break
		}
		if s, ok := parseState(item); ok {
			out[s.id] = s
		}
	}
	return out, nil
}

// parseStateChange reads a state_changed event: the entity and its new state,
// or removed when the new state is null or malformed (a reload would drop it
// too). ok is false when the event names no valid entity.
func parseStateChange(raw json.RawMessage) (id string, s entityState, removed, ok bool) {
	var ev struct {
		EntityID string          `json:"entity_id"`
		NewState json.RawMessage `json:"new_state"`
	}
	if json.Unmarshal(raw, &ev) != nil || !validEntityID(ev.EntityID) {
		return "", entityState{}, false, false
	}
	if isNull(ev.NewState) {
		return ev.EntityID, entityState{}, true, true
	}
	s, valid := parseState(ev.NewState)
	if !valid || s.id != ev.EntityID {
		return ev.EntityID, entityState{}, true, true
	}
	return ev.EntityID, s, false, true
}

func isNull(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(raw, []byte("null"))
}

// text reads an optional string of at most n code points into dst; null and
// absent leave dst alone, anything else fails.
func text(raw json.RawMessage, n int, dst *string) bool {
	if isNull(raw) {
		return true
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || utf8.RuneCountInString(s) > n {
		return false
	}
	*dst = s
	return true
}

func enum(raw json.RawMessage, dst **string) bool {
	var s string
	if !text(raw, maxEnum, &s) {
		return false
	}
	if !isNull(raw) {
		*dst = &s
	}
	return true
}

func number(raw json.RawMessage, lo, hi float64, dst **float64) bool {
	if isNull(raw) {
		return true
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil || f < lo || f > hi {
		return false
	}
	*dst = &f
	return true
}

// modes reads a short list of mode words (a water heater's operation_list):
// absent leaves dst alone, anything but a list of at most maxModeRunes code
// points fails, and only the first maxModes are kept.
func modes(raw json.RawMessage, dst *[]string) bool {
	if isNull(raw) {
		return true
	}
	var list []string
	if json.Unmarshal(raw, &list) != nil {
		return false
	}
	if len(list) > maxModes {
		list = list[:maxModes]
	}
	for _, m := range list {
		if utf8.RuneCountInString(m) > maxModeRunes {
			return false
		}
	}
	*dst = list
	return true
}
