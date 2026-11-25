package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
)

// the action's own field and the strict union are Run's checks: a oneOf
// would refuse first, in words the model cannot act on. Numbers may come
// quoted ("20"): the pattern takes those, Run checks their range.
const haControlInput = `{
	"type": "object",
	"properties": {
		"action": {
			"enum": ["turn_on", "turn_off", "toggle", "set_brightness", "set_color_temp", "set_temperature", "set_hvac_mode", "call_service"],
			"description": "turn_on, turn_off and toggle work for any controllable device; each other action takes its own field."
		},
		"target": {
			"type": "string", "minLength": 1, "maxLength": 128, "pattern": "\\S",
			"description": "Entity id such as \"light.bedroom\", or the device name as the user said it, exact or partial."
		},
		"brightness": {
			"type": ["integer", "string"], "minimum": 0, "maximum": 100, "pattern": "^-?\\d{1,6}(?:\\.\\d{1,2})?$",
			"description": "set_brightness: new light brightness as a percent, 0 to 100. Lights only."
		},
		"kelvin": {
			"type": ["integer", "string"], "minimum": 1000, "maximum": 10000, "pattern": "^-?\\d{1,6}(?:\\.\\d{1,2})?$",
			"description": "set_color_temp: white color temperature in kelvin; lower is warmer. Lights only."
		},
		"temperature": {
			"type": ["number", "string"], "minimum": 0, "maximum": 100, "pattern": "^-?\\d{1,6}(?:\\.\\d{1,2})?$",
			"description": "set_temperature: target temperature in degrees Celsius. Climate and water heater entities only."
		},
		"mode": {
			"enum": ["off", "heat", "cool", "heat_cool", "auto", "dry", "fan_only"],
			"description": "set_hvac_mode: new climate hvac mode; \"off\" turns the climate entity off. Climate entities only."
		},
		"service": {
			"type": "string", "maxLength": 64, "pattern": "^(?:[a-z][a-z0-9_]*\\.)?[a-z][a-z0-9_]*$",
			"description": "call_service: service to call on the device, such as \"clean_spot\" or \"vacuum.clean_spot\". Use ha_inspect to see what the device supports."
		},
		"fields": {
			"type": "object", "propertyNames": {"maxLength": 64},
			"description": "call_service: service fields as an object, such as {\"fan_speed\": \"max\"}. ha_inspect shows each field; required ones must be given. Never entity_id."
		}
	},
	"required": ["action", "target"],
	"additionalProperties": false
}`

const (
	usageTimeout   = 2 * time.Second
	maxFieldsChars = 2000
	// cadencePlatform is Cadence's own Home Assistant integration: its
	// entities are a speaker's volume and microphone, which only Cadence's
	// policy-gated tools may change (rule 7, Ruling M5c-R8)
	cadencePlatform = "cadence"
)

// HAControl changes the home (M5c-D4). A quick confirmation is its result; a
// slow one answers pending and finishes on the live card and in the history.
func HAControl(d HADeps) agent.Tool {
	if d.Confirmations == nil {
		d.Confirmations = NewHAConfirmations(d.Home, HAConfirmationOptions{})
	}
	return agent.Tool{
		Name: agent.HAControl,
		Description: "Control a home device through Home Assistant: turn it on or off, toggle it, set light brightness or color " +
			"temperature, a climate or water heater temperature, a climate hvac mode, or call another service the device supports " +
			"(list them with ha_inspect first). Call it for every such request, follow-ups too. An ok answer without pending means " +
			"the state is confirmed. If ok has pending:true, the command was accepted and is still being confirmed: say the action " +
			"is in progress (for example, «Выключаю кондиционер»), never that it completed or failed, and do not retry. state is " +
			"the current state and expected the target; confirmation continues in the background. target takes the device's name " +
			"as the user said it (the home note lists them); names resolve against the known devices; if several match, ask the user which one.",
		Input:   haControlInput,
		Effects: agent.Write, Output: agent.Trusted,
		Timeout:   5 * time.Second,
		Available: d.available,
		Run: func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			a, e := parseControl(c.Args)
			if e != nil {
				return nil, e
			}
			v, e := d.view()
			if e != nil {
				return nil, e
			}
			can := func(x ha.Entity) bool { return canTake(a, x, v.Services) }
			ms, e := d.narrow(ctx, c.DeviceID, matches(v.Entities, a.target), can)
			switch {
			case e != nil:
				return nil, e
			case len(ms) == 0:
				return nil, haNotFound(fmt.Sprintf("no entity matched %q; search for candidates with ha_search", a.target),
					"Search with ha_search, or tell the user no such device was found.")
			case len(ms) > 1:
				return nil, ambiguous(ms, v.Areas)
			}
			ent := ms[0]
			switch {
			case ent.Platform == cadencePlatform:
				return nil, cadenceOwn()
			case ent.EntryUnreadable:
				return nil, unreadable()
			case ent.PlatformUnknown:
				return nil, unlisted()
			}
			// a heater without the on/off feature is already on in any
			// operation mode: answering needs no call, and none would say
			// what state to put it in
			if a.action == "turn_on" && ent.Domain == "water_heater" && !ent.HasFeature(ha.WaterHeaterOnOff) && heaterOn(ent) {
				return encode(controlOut{Done: a.action, Entity: controlEntity{ID: ent.ID, Name: displayName(ent), State: ha.ModelState(ent)}})
			}
			call, e := planCall(a, ent, v.Services)
			if e != nil {
				return nil, e
			}
			if d.Policy != nil {
				if e := d.Policy(ctx, c, ent, call.domain, call.service); e != nil {
					return nil, e
				}
			}
			// a barge-in ends the turn, and an ended turn must not change the home
			if ctx.Err() != nil {
				return nil, haCanceled()
			}
			if err := d.Home.Client().Call(ctx, call.domain, call.service, call.data, ent.ID); err != nil {
				return nil, callError(ctx, err, a.action)
			}
			d.record(ctx, c.DeviceID, ent.ID)
			out, e := d.done(ctx, c, a, ent)
			if e != nil {
				return nil, e
			}
			return encode(out)
		},
	}
}

type controlArgs struct {
	action, target, mode, service string
	brightness, kelvin            int
	temperature                   float64
	// fields nil: none given
	fields map[string]any
}

// actionFields are the fields each action takes, beside target.
var actionFields = map[string][]string{
	"set_brightness": {"brightness"}, "set_color_temp": {"kelvin"}, "set_temperature": {"temperature"},
	"set_hvac_mode": {"mode"}, "call_service": {"service", "fields"},
}

// fieldOwners name the action of each field, in the order they are checked.
var fieldOwners = []struct{ field, action string }{
	{"brightness", "set_brightness"}, {"kelvin", "set_color_temp"}, {"temperature", "set_temperature"},
	{"mode", "set_hvac_mode"}, {"service", "call_service"}, {"fields", "call_service"},
}

var quotedNumber = regexp.MustCompile(`^-?\d{1,6}(?:\.\d{1,2})?$`)

// parseControl reads arguments the schema passed: the strict union on
// action, quoted numbers in range, and fields at most 2000 characters of
// JSON, before anything is looked up.
func parseControl(raw json.RawMessage) (controlArgs, *agent.ToolError) {
	var in struct {
		Action      string          `json:"action"`
		Target      string          `json:"target"`
		Brightness  json.RawMessage `json:"brightness"`
		Kelvin      json.RawMessage `json:"kelvin"`
		Temperature json.RawMessage `json:"temperature"`
		Mode        json.RawMessage `json:"mode"`
		Service     json.RawMessage `json:"service"`
		Fields      json.RawMessage `json:"fields"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return controlArgs{}, badArgs("The arguments are not a JSON object.")
	}
	given := map[string]bool{
		"brightness": in.Brightness != nil, "kelvin": in.Kelvin != nil, "temperature": in.Temperature != nil,
		"mode": in.Mode != nil, "service": in.Service != nil, "fields": in.Fields != nil,
	}
	for _, f := range fieldOwners {
		if given[f.field] && f.action != in.Action {
			return controlArgs{}, badArgs(f.field + " is only for " + f.action + ".")
		}
	}
	if own := actionFields[in.Action]; len(own) > 0 && !given[own[0]] {
		return controlArgs{}, badArgs(in.Action + " needs " + own[0] + ".")
	}
	a := controlArgs{action: in.Action, target: strings.TrimSpace(in.Target)}
	var ok bool
	switch in.Action {
	case "set_brightness":
		if a.brightness, ok = whole(in.Brightness, 0, 100); !ok {
			return controlArgs{}, badArgs("brightness must be a whole number from 0 to 100.")
		}
	case "set_color_temp":
		if a.kelvin, ok = whole(in.Kelvin, 1000, 10000); !ok {
			return controlArgs{}, badArgs("kelvin must be a whole number from 1000 to 10000.")
		}
	case "set_temperature":
		if a.temperature, ok = number(in.Temperature); !ok || a.temperature < 0 || a.temperature > 100 {
			return controlArgs{}, badArgs("temperature must be a number from 0 to 100 (Celsius).")
		}
	case "set_hvac_mode":
		_ = json.Unmarshal(in.Mode, &a.mode)
	case "call_service":
		_ = json.Unmarshal(in.Service, &a.service)
		if in.Fields != nil {
			var e *agent.ToolError
			if a.fields, e = parseFields(in.Fields); e != nil {
				return controlArgs{}, e
			}
		}
	}
	if a.target == "" {
		return controlArgs{}, badArgs("target is empty.")
	}
	return a, nil
}

// number reads a JSON number, or one the model quoted ("20").
func number(raw json.RawMessage) (float64, bool) {
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return f, true
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || !quotedNumber.MatchString(s) {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

func whole(raw json.RawMessage, lo, hi int) (int, bool) {
	f, ok := number(raw)
	if !ok || f != math.Trunc(f) || f < float64(lo) || f > float64(hi) {
		return 0, false
	}
	return int(f), true
}

// parseFields keeps numbers as the model wrote them.
func parseFields(raw json.RawMessage) (map[string]any, *agent.ToolError) {
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return nil, badArgs("fields is not a JSON object.")
	}
	if utf8.RuneCount(compact.Bytes()) > maxFieldsChars {
		return nil, badArgs("fields JSON is over 2000 characters")
	}
	dec := json.NewDecoder(&compact)
	dec.UseNumber()
	var fields map[string]any
	if dec.Decode(&fields) != nil || fields == nil {
		return nil, badArgs("fields is not a JSON object.")
	}
	return fields, nil
}

// planCall is the service call of a on e, or why there is none.
func planCall(a controlArgs, e ha.Entity, svcs map[string]map[string]ha.Service) (haCall, *agent.ToolError) {
	if a.action == "call_service" {
		return serviceCall(svcs, e, a.service, a.fields)
	}
	if a.action == "set_temperature" {
		if e := outOfRange(a, e); e != nil {
			return haCall{}, e
		}
	}
	call, ok := fixedCall(a, e)
	if !ok {
		return haCall{}, notSupported(a.action)
	}
	return call, nil
}

// outOfRange refuses a temperature the entity cannot take, naming its range,
// before the call reaches home assistant. Its bounds are in its own unit, so
// a heater that does not speak celsius is left to home assistant's own check.
func outOfRange(a controlArgs, e ha.Entity) *agent.ToolError {
	lo, hi := e.Attrs.MinTemp, e.Attrs.MaxTemp
	if lo == nil || hi == nil || *lo > *hi {
		return nil
	}
	if u := e.Attrs.TemperatureUnit; u != nil && *u != "°C" && *u != "C" {
		return nil
	}
	if a.temperature < *lo || a.temperature > *hi {
		return &agent.ToolError{
			Code:      "invalid_arguments",
			Message:   fmt.Sprintf("The device takes temperatures from %s to %s degrees.", figure(*lo), figure(*hi)),
			Hint:      "Call again with a temperature in that range.",
			Retryable: true,
		}
	}
	return nil
}

func figure(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// record counts a change the call made, beside the tool's own budget and
// under a deadline of its own: the home changed even when the turn ends
// meanwhile, and a slow store must not hold the answer.
func (d HADeps) record(ctx context.Context, deviceID, entityID string) {
	if d.Usage == nil {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), usageTimeout)
	go func() {
		defer cancel()
		// usage is a ranking hint: a store that panics must not take the server down
		defer func() { _ = recover() }()
		d.Usage.Record(rctx, deviceID, entityID)
	}()
}

type controlEntity struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state,omitempty"`
}

type controlOut struct {
	Done         string        `json:"done"`
	Entity       controlEntity `json:"entity"`
	Pending      bool          `json:"pending,omitempty"`
	State        string        `json:"state,omitempty"`
	Expected     string        `json:"expected,omitempty"`
	Confirmation string        `json:"confirmation,omitempty"`
	Late         bool          `json:"late,omitempty"`
}

// done waits the short window through the shared tracker. Its pending result
// names the current state and the fixed action's target; the full budget runs
// beside the turn, not inside its reply.
func (d HADeps) done(ctx context.Context, call agent.Call, a controlArgs, before ha.Entity) (controlOut, *agent.ToolError) {
	out := controlOut{Done: a.action, Entity: controlEntity{ID: before.ID, Name: displayName(before)}}
	return d.Confirmations.confirm(ctx, call, a, before, out)
}

// cadenceOwn refuses a Cadence device's own entity, in every turn.
func cadenceOwn() *agent.ToolError {
	return &agent.ToolError{
		Code:    "not_supported",
		Message: "Cadence devices are never controlled through Home Assistant.",
		Hint:    "Use the device's own controls: its volume and settings change through Cadence's own tools, never Home Assistant.",
	}
}

// unlisted refuses an entity whose platform the registry does not show
// (M8-D6), in every turn: it may be a Cadence device's own.
func unlisted() *agent.ToolError {
	return &agent.ToolError{
		Code:    "unavailable",
		Message: "Home Assistant has not finished listing this device, so it can't be controlled right now.",
		Hint:    "Tell the user it can't be controlled right now; it may work again in a moment.",
	}
}

// unreadable refuses an entity whose registry entry cannot be read: its
// platform stays unknown (M8-D6) until Home Assistant's entry changes.
func unreadable() *agent.ToolError {
	return &agent.ToolError{
		Code:    "unavailable",
		Message: "Home Assistant's record of this device can't be read, so it can't be controlled.",
		Hint:    "Tell the user it can't be controlled from here; trying again won't help until its settings change in Home Assistant.",
	}
}

// haCanceled answers a turn that ended before the call; the runner reports
// the turn's own end instead.
func haCanceled() *agent.ToolError {
	return &agent.ToolError{Code: "canceled", Message: "The turn ended before Home Assistant was called."}
}

// callError is the model's view of a failed call: never Home Assistant's text.
// A timed-out call may still land, so it is not offered for a retry.
func callError(ctx context.Context, err error, action string) *agent.ToolError {
	switch ha.Code(err) {
	case ha.CodeTimeout:
		return &agent.ToolError{Code: "timeout", Message: "Home Assistant did not answer in time; the change may still happen."}
	case "ha_error:invalid_format", "ha_error:service_validation_error":
		return refusedData(action)
	case "ha_error:not_found":
		return notSupported(action)
	case ha.CodeUnavailable:
		return haUnavailable()
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		// the call may have gone out before the turn ended
		return &agent.ToolError{Code: "canceled", Message: "The turn ended; the change may still happen."}
	}
	return unavailable("Home Assistant could not carry out the call.")
}
