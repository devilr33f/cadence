package tools_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha/hatest"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

func TestHAToolsNeedHomeAssistant(t *testing.T) {
	r := newHARig(t, baseHouse())
	set := settings.Values{HomeAssistant: "home"}
	for _, mk := range []func(tools.HADeps) agent.Tool{tools.HAControl, tools.HASearch, tools.HAInspect} {
		running, missing := mk(r.deps()), mk(tools.HADeps{})
		if running.Available == nil || !running.Available(set) || running.Available(settings.Values{}) || missing.Available(set) {
			t.Fatalf("%s offered without home assistant", running.Name)
		}
		if running.Output != agent.Trusted {
			t.Fatalf("%s output not trusted", running.Name)
		}
	}
	control, search, inspect := tools.HAControl(r.deps()), tools.HASearch(r.deps()), tools.HAInspect(r.deps())
	if control.Effects != agent.Write || control.Timeout != 5*time.Second || control.SilentOnSuccess || control.EventTurns {
		t.Fatalf("control %+v", control)
	}
	if search.Effects != agent.Read || search.Timeout != 4*time.Second || inspect.Effects != agent.Read || inspect.Timeout != 5*time.Second {
		t.Fatal("search and inspect are reads of 4 s and 5 s")
	}
	// never offered where nobody spoke
	reg := agent.NewRegistry(control, search, inspect)
	var names []string
	for _, tl := range reg.Offer(nil, false, cadencev2.Trigger_TRIGGER_EVENT) {
		names = append(names, tl.Name)
	}
	if !slices.Equal(names, []string{"ha_search", "ha_inspect"}) {
		t.Fatalf("event turn offered %v", names)
	}
	// v1's descriptions, naming the v2 tools
	if !strings.Contains(control.Description, "ha_inspect") || !strings.Contains(search.Description, "ha_control") ||
		!strings.Contains(search.Description, "ha_inspect") || !strings.Contains(inspect.Description, "ha_control") ||
		!strings.Contains(inspect.Description, "ha_search") {
		t.Fatal("a description names the wrong tools")
	}
	for _, tl := range []agent.Tool{control, search, inspect} {
		if strings.Contains(tl.Description, "home_assistant") {
			t.Fatalf("%s names a v1 tool", tl.Name)
		}
	}
}

func TestHAControlByName(t *testing.T) {
	r := newHARig(t, baseHouse())
	control := tools.HAControl(r.deps())
	// already off: the cache shows it at once
	out := r.raw(control, `{"action":"turn_off","target":"bedroom light"}`)
	if out != `{"done":"turn_off","entity":{"id":"light.bedroom","name":"Bedroom light","state":"off"}}` {
		t.Fatalf("%s", out)
	}
	out = r.raw(control, `{"action":"turn_on","target":" Light.Bedroom "}`, haState("light.bedroom", "on", named("Bedroom light")))
	if out != `{"done":"turn_on","entity":{"id":"light.bedroom","name":"Bedroom light","state":"on"}}` {
		t.Fatalf("%s", out)
	}
	// home assistant's names are sanitized
	out = r.raw(control, `{"action":"turn_on","target":"switch.odd"}`, haState("switch.odd", "on", named("Odd \"plug\"\u0007\n  name")))
	if out != `{"done":"turn_on","entity":{"id":"switch.odd","name":"Odd plug name","state":"on"}}` {
		t.Fatalf("%s", out)
	}
	want := []string{"homeassistant.turn_off light.bedroom", "homeassistant.turn_on light.bedroom", "homeassistant.turn_on switch.odd"}
	if got := r.calls(); !slices.Equal(got, want) {
		t.Fatalf("calls %v", got)
	}
	// recorded beside the answers, so in any order
	got := r.usage.count(t, 3)
	slices.SortFunc(got, func(a, b usageCall) int { return strings.Compare(a.entity, b.entity) })
	if want := []usageCall{{"d1", "light.bedroom"}, {"d1", "light.bedroom"}, {"d1", "switch.odd"}}; !slices.Equal(got, want) {
		t.Fatalf("usage %v", got)
	}
}

// usage is counted beside the tool's budget: a slow store never holds the
// answer, and gets its own deadline
func TestHAControlCountsUseAfterAnswering(t *testing.T) {
	r := newHARig(t, baseHouse())
	r.usage.hold = make(chan struct{})
	start := time.Now()
	r.ok(tools.HAControl(r.deps()), `{"action":"turn_off","target":"bedroom light"}`)
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("answered after %v", d)
	}
	if len(r.usage.got()) != 0 {
		t.Fatal("recorded through a held store")
	}
	close(r.usage.hold)
	if got := r.usage.count(t, 1); got[0] != (usageCall{"d1", "light.bedroom"}) || r.usage.bounded != 1 {
		t.Fatalf("usage %v, %d bounded", got, r.usage.bounded)
	}
}

func TestHAControlLightsAndClimate(t *testing.T) {
	r := newHARig(t, baseHouse())
	control := tools.HAControl(r.deps())
	out := r.raw(control, `{"action":"set_brightness","target":"big light","brightness":70}`,
		haState("light.kitchen", "on", obj{"friendly_name": "Kitchen light", "brightness": 179, "color_temp_kelvin": 2700}))
	if out != `{"done":"set_brightness","entity":{"id":"light.kitchen","name":"Kitchen light","state":"on"}}` {
		t.Fatalf("%s", out)
	}
	r.ok(control, `{"action":"set_color_temp","target":"kitchen light","kelvin":2700}`)
	r.ok(control, `{"action":"set_temperature","target":"living room","temperature":21.5}`,
		haState("climate.living_room", "heat", obj{"friendly_name": "Living room", "temperature": 21.5}))
	r.ok(control, `{"action":"set_temperature","target":"boiler","temperature":60}`,
		haState("water_heater.boiler", "eco", obj{"friendly_name": "Boiler", "temperature": 60, "operation_mode": "eco"}))
	r.ok(control, `{"action":"set_hvac_mode","target":"living room","mode":"heat"}`)
	out = r.raw(control, `{"action":"set_hvac_mode","target":"living room","mode":"off"}`, haState("climate.living_room", "off", named("Living room")))
	if out != `{"done":"set_hvac_mode","entity":{"id":"climate.living_room","name":"Living room","state":"off"}}` {
		t.Fatalf("%s", out)
	}
	want := []string{
		`light.turn_on light.kitchen {"brightness_pct":70}`,
		`light.turn_on light.kitchen {"color_temp_kelvin":2700}`,
		`climate.set_temperature climate.living_room {"temperature":21.5}`,
		`water_heater.set_temperature water_heater.boiler {"temperature":60}`,
		`climate.set_hvac_mode climate.living_room {"hvac_mode":"heat"}`,
		`climate.set_hvac_mode climate.living_room {"hvac_mode":"off"}`,
	}
	if got := r.calls(); !slices.Equal(got, want) {
		t.Fatalf("calls\n got %v\nwant %v", got, want)
	}
}

// 2026-10-30 (v1): the model sent "temperature": "20" and got invalid_arguments
func TestHAControlAcceptsQuotedNumbers(t *testing.T) {
	r := newHARig(t, baseHouse())
	control := tools.HAControl(r.deps())
	r.ok(control, `{"action":"set_temperature","target":"living room","temperature":"20"}`, haState("climate.living_room", "heat", obj{"friendly_name": "Living room", "temperature": 20}))
	r.ok(control, `{"action":"set_temperature","target":"boiler","temperature":"20.5"}`, haState("water_heater.boiler", "eco", obj{"friendly_name": "Boiler", "temperature": 20.5, "operation_mode": "eco"}))
	r.ok(control, `{"action":"set_brightness","target":"kitchen light","brightness":"70"}`, haState("light.kitchen", "on", obj{"friendly_name": "Kitchen light", "brightness": 179, "color_temp_kelvin": 2700}))
	r.ok(control, `{"action":"set_color_temp","target":"kitchen light","kelvin":"2700"}`)
	want := []string{
		`climate.set_temperature climate.living_room {"temperature":20}`,
		`water_heater.set_temperature water_heater.boiler {"temperature":20.5}`,
		`light.turn_on light.kitchen {"brightness_pct":70}`,
		`light.turn_on light.kitchen {"color_temp_kelvin":2700}`,
	}
	if got := r.calls(); !slices.Equal(got, want) {
		t.Fatalf("calls %v", got)
	}
	for _, args := range []string{
		`{"action":"set_temperature","target":"living room","temperature":"20°"}`,
		`{"action":"set_brightness","target":"kitchen light","brightness":"70%"}`,
		`{"action":"set_brightness","target":"kitchen light","brightness":"70.5"}`,
		`{"action":"set_brightness","target":"kitchen light","brightness":"101"}`,
		`{"action":"set_color_temp","target":"kitchen light","kelvin":"900"}`,
		`{"action":"set_temperature","target":"living room","temperature":"-1"}`,
		`{"action":"set_temperature","target":"living room","temperature":"warm"}`,
	} {
		if e := r.refused(control, args, "invalid_arguments"); !e.Retryable {
			t.Fatalf("%s: %+v", args, e)
		}
	}
}

func TestHAControlRejectsJunk(t *testing.T) {
	r := newHARig(t, baseHouse())
	control := tools.HAControl(r.deps())
	cases := map[string]string{
		`{"action":"turn_on"}`:                                                              "",
		`{"action":"dim","target":"bedroom light"}`:                                         "",
		`{"action":"turn_on","target":"   "}`:                                               "",
		`{"action":"set_brightness","target":"bedroom light"}`:                              "set_brightness needs brightness.",
		`{"action":"set_brightness","target":"bedroom light","brightness":101}`:             "",
		`{"action":"set_color_temp","target":"bedroom light","kelvin":100}`:                 "",
		`{"action":"set_hvac_mode","target":"living room","mode":"blast"}`:                  "",
		`{"action":"set_hvac_mode","target":"living room"}`:                                 "set_hvac_mode needs mode.",
		`{"action":"call_service","target":"cleaner"}`:                                      "call_service needs service.",
		`{"action":"call_service","target":"cleaner","service":"Clean Spot!"}`:              "",
		`{"action":"turn_on","target":"bedroom light","brightness":50}`:                     "brightness is only for set_brightness.",
		`{"action":"set_brightness","target":"bedroom light","brightness":5,"kelvin":3000}`: "kelvin is only for set_color_temp.",
		`{"action":"turn_off","target":"cleaner","fields":{}}`:                              "fields is only for call_service.",
		`{"action":"turn_on","target":"bedroom light","color":"red"}`:                       "",
		`"turn on the light"`: "",
	}
	for args, msg := range cases {
		e := r.refused(control, args, "invalid_arguments")
		if msg != "" && e.Message != msg {
			t.Fatalf("%s: message %q, want %q", args, e.Message, msg)
		}
	}
}

func TestHAControlToggleReportsWhatTheCacheSees(t *testing.T) {
	r := newHARig(t, baseHouse())
	control := tools.HAControl(r.deps())
	out := r.raw(control, `{"action":"toggle","target":"bedroom light"}`, haState("light.bedroom", "on", named("Bedroom light")))
	if out != `{"done":"toggle","entity":{"id":"light.bedroom","name":"Bedroom light","state":"on"}}` {
		t.Fatalf("%s", out)
	}
	// no change within the wait: the call went out, but nothing happened
	start := time.Now()
	_, e := r.act(control, `{"action":"toggle","target":"bedroom light"}`)
	if e == nil || e.Code != "no_effect" || e.Retryable {
		t.Fatalf("%+v", e)
	}
	if d := time.Since(start); d < 2*time.Second || d > 4*time.Second {
		t.Fatalf("waited %v for the state", d)
	}
	if got := r.calls(); got[len(got)-1] != "homeassistant.toggle light.bedroom" {
		t.Fatalf("the call went out: %v", got)
	}
}

// 2026-11-14 (v2, the owner's kettle): ha_control turn_on answered ok, the
// model said «Чайник включён», and the kettle stayed off: a water heater
// without the on/off feature ignores the domain's turn_on, so it is switched
// by its operation mode
func TestHAControlWaterHeaterByOperationMode(t *testing.T) {
	r := newHARig(t, baseHouse())
	control := tools.HAControl(r.deps())
	kettle := func(state string, cur float64) obj {
		return haState("water_heater.kettle", state, obj{
			"friendly_name": "Kettle", "temperature": 100, "current_temperature": cur,
			"supported_features": 1, "operation_list": []string{"off", "on"}, "min_temp": 40, "max_temp": 100,
		})
	}
	// on: the mode it heats in, never off
	out := r.raw(control, `{"action":"turn_on","target":"kettle"}`, kettle("on", 65))
	if out != `{"done":"turn_on","entity":{"id":"water_heater.kettle","name":"Kettle","state":"on"}}` {
		t.Fatalf("%s", out)
	}
	// off: the operation mode goes to off
	r.ok(control, `{"action":"turn_off","target":"kettle"}`, kettle("off", 65))
	// toggle from off heats again
	r.ok(control, `{"action":"toggle","target":"kettle"}`, kettle("on", 70))
	// turn_on of one already heating answers without a call
	before := len(r.fake.Calls())
	out = r.raw(control, `{"action":"turn_on","target":"kettle"}`)
	if out != `{"done":"turn_on","entity":{"id":"water_heater.kettle","name":"Kettle","state":"on"}}` {
		t.Fatalf("%s", out)
	}
	if len(r.fake.Calls()) != before {
		t.Fatal("turn_on of a heating kettle called home assistant")
	}
	// a heater with the on/off feature keeps the domain's own switch
	r.ok(control, `{"action":"turn_on","target":"boiler"}`,
		haState("water_heater.boiler", "on", obj{"friendly_name": "Boiler", "temperature": 55, "operation_mode": "eco"}))
	want := []string{
		`water_heater.set_operation_mode water_heater.kettle {"operation_mode":"on"}`,
		`water_heater.set_operation_mode water_heater.kettle {"operation_mode":"off"}`,
		`water_heater.set_operation_mode water_heater.kettle {"operation_mode":"on"}`,
		"homeassistant.turn_on water_heater.boiler",
	}
	if got := r.calls(); !slices.Equal(got, want) {
		t.Fatalf("calls\n got %v\nwant %v", got, want)
	}
}

// a kettle whose home registers none of the domain's turn_on: it still heats,
// through set_operation_mode alone
func TestHAControlWaterHeaterWithoutTheDomainSwitch(t *testing.T) {
	h := baseHouse()
	var svcs map[string]map[string]map[string]any
	if json.Unmarshal(h.services.(json.RawMessage), &svcs) != nil {
		t.Fatal("the base house's services do not parse")
	}
	delete(svcs["water_heater"], "turn_on")
	delete(svcs["water_heater"], "turn_off")
	b, _ := json.Marshal(svcs)
	h.services = json.RawMessage(b)
	r := newHARig(t, h)
	control := tools.HAControl(r.deps())
	r.ok(control, `{"action":"turn_on","target":"kettle"}`,
		haState("water_heater.kettle", "on", obj{"friendly_name": "Kettle", "temperature": 100, "current_temperature": 65,
			"supported_features": 1, "operation_list": []string{"off", "on"}, "min_temp": 40, "max_temp": 100}))
	if got := r.calls(); !slices.Equal(got, []string{`water_heater.set_operation_mode water_heater.kettle {"operation_mode":"on"}`}) {
		t.Fatalf("calls %v", got)
	}
	// nothing to heat in and no on/off: the honest refusal
	noModes := baseHouse()
	noModes.services = h.services
	for _, s := range noModes.states {
		if s.(obj)["entity_id"] == "water_heater.kettle" {
			delete(s.(obj)["attributes"].(obj), "operation_list")
		}
	}
	r2 := newHARig(t, noModes)
	r2.refused(tools.HAControl(r2.deps()), `{"action":"turn_on","target":"kettle"}`, "not_supported")
}

// home assistant took the call, the kettle has not answered by the short
// window: the reply is pending, and only the whole budget's no-change is a
// no_effect in the late history
func TestHAControlReportsNoEffect(t *testing.T) {
	r := newHARigWithConfirm(t, baseHouse(), tools.HAConfirmationOptions{Short: 30 * time.Millisecond, Cap: 140 * time.Millisecond})
	control := tools.HAControl(r.deps())
	start := time.Now()
	raw, e := r.act(control, `{"action":"turn_on","target":"kettle"}`)
	if e != nil {
		t.Fatalf("the short window failed instead of pending: %+v", e)
	}
	var out map[string]any
	if json.Unmarshal([]byte(raw), &out) != nil || out["pending"] != true || out["state"] != "off" || out["expected"] != "on" {
		t.Fatalf("pending result %s", raw)
	}
	if d := time.Since(start); d < 20*time.Millisecond || d > time.Second {
		t.Fatalf("returned pending after %v", d)
	}
	id, _ := out["confirmation"].(string)
	snap := r.confirmation(t, id, tools.HAConfirmationFailed)
	if snap.State != "off" || snap.Expected != "on" {
		t.Fatalf("late state %+v", snap)
	}
	rec := r.lateRecord(t)
	var final struct {
		OK    bool             `json:"ok"`
		Error *agent.ToolError `json:"error"`
	}
	if json.Unmarshal(rec.Result, &final) != nil || final.OK || final.Error == nil || final.Error.Code != "no_effect" ||
		final.Error.Retryable || final.Error.Hint == "" || !strings.Contains(final.Error.Message, `"off"`) {
		t.Fatalf("late result %s", rec.Result)
	}
	if strings.Contains(final.Error.Message, hatest.Leak) {
		t.Fatalf("upstream text: %q", final.Error.Message)
	}
	// the call went out and was taken; usage counts it
	if got := r.calls(); got[len(got)-1] != `water_heater.set_operation_mode water_heater.kettle {"operation_mode":"on"}` {
		t.Fatalf("calls %v", got)
	}
	r.usage.count(t, 1)
	// a call the model picked itself may change nothing the cache keeps, so
	// it answers ok without a state rather than no_effect
	outRaw := r.raw(control, `{"action":"call_service","target":"kettle","service":"set_temperature","fields":{"temperature":95}}`)
	if outRaw != `{"done":"call_service","entity":{"id":"water_heater.kettle","name":"Kettle"}}` {
		t.Fatalf("%s", outRaw)
	}
}

func TestHAControlTemperatureRange(t *testing.T) {
	r := newHARig(t, baseHouse())
	control := tools.HAControl(r.deps())
	for temp, bounds := range map[string][2]string{
		`{"action":"set_temperature","target":"kettle","temperature":39}`:      {"40", "100"},
		`{"action":"set_temperature","target":"boiler","temperature":95}`:      {"20", "90"},
		`{"action":"set_temperature","target":"kettle","temperature":100}`:     {},
		`{"action":"set_temperature","target":"living room","temperature":21}`: {}, // no bounds of its own
	} {
		if bounds[0] != "" {
			e := r.refused(control, temp, "invalid_arguments")
			if !strings.Contains(e.Message, bounds[0]) || !strings.Contains(e.Message, bounds[1]) {
				t.Fatalf("%s: %+v", temp, e)
			}
		} else {
			r.ok(control, temp)
		}
	}
	// a heater that speaks fahrenheit is left to home assistant's own check
	h := baseHouse()
	h.states = append(h.states, haState("water_heater.usa", "off", obj{
		"friendly_name": "US heater", "temperature": 140, "temperature_unit": "°F",
		"supported_features": 1, "operation_list": []string{"off", "on"}, "min_temp": 100, "max_temp": 150,
	}))
	r2 := newHARig(t, h)
	r2.ok(tools.HAControl(r2.deps()), `{"action":"set_temperature","target":"usa","temperature":50}`,
		haState("water_heater.usa", "off", obj{
			"friendly_name": "US heater", "temperature": 50, "temperature_unit": "°F",
			"supported_features": 1, "operation_list": []string{"off", "on"}, "min_temp": 100, "max_temp": 150,
		}))
}

func TestHAControlRefusesWhatTheDomainCannotDo(t *testing.T) {
	r := newHARig(t, baseHouse())
	control := tools.HAControl(r.deps())
	hints := map[string]bool{}
	for args, inspect := range map[string]bool{
		`{"action":"set_brightness","target":"boiler","brightness":50}`:                                                       false,
		`{"action":"set_color_temp","target":"boiler","kelvin":3000}`:                                                         false,
		`{"action":"set_hvac_mode","target":"boiler","mode":"heat"}`:                                                          false,
		`{"action":"set_temperature","target":"kitchen light","temperature":20}`:                                              false,
		`{"action":"call_service","target":"bedroom light","service":"clean_spot"}`:                                           true,
		`{"action":"call_service","target":"vacuum.cleaner","service":"climate.set_temperature","fields":{"temperature":20}}`: true,
		`{"action":"call_service","target":"vacuum.cleaner","service":"dance"}`:                                               true,
	} {
		e := r.refused(control, args, "not_supported")
		if e.Retryable || e.Message != "This device can't do that." || e.Hint == "" || strings.Contains(e.Hint, "ha_inspect") != inspect {
			t.Fatalf("%s: %+v", args, e)
		}
		hints[e.Hint] = true
	}
	// each fixed action says what takes it; call_service points at ha_inspect
	if len(hints) != 5 {
		t.Fatalf("hints %v", hints)
	}
	if len(r.usage.got()) != 0 {
		t.Fatal("a refusal counted as use")
	}
}

func TestHAControlAsksWhichOne(t *testing.T) {
	r := newHARig(t, baseHouse())
	control := tools.HAControl(r.deps())
	// the sensor's domain cannot be turned on, so only two are asked about
	e := r.refused(control, `{"action":"turn_on","target":"hall"}`, "ambiguous")
	if e.Message != "Several devices match: Hall light, Hallway." || !e.Retryable || !slices.Equal(e.Candidates, []string{"Hall light", "Hallway"}) {
		t.Fatalf("%+v", e)
	}
	// at most three names, by entity id
	e = r.refused(control, `{"action":"turn_on","target":"light"}`, "ambiguous")
	if e.Message != "Several devices match: Bedroom light, Light Bulb, Ceiling." {
		t.Fatalf("%+v", e)
	}
	e = r.refused(control, `{"action":"turn_on","target":"plug"}`, "ambiguous")
	if e.Message != "Several devices match: Lamp, Odd plug name." {
		t.Fatalf("names are sanitized: %+v", e)
	}
}

func TestHAControlNotFound(t *testing.T) {
	r := newHARig(t, baseHouse())
	e := r.refused(tools.HAControl(r.deps()), `{"action":"turn_on","target":"garage door"}`, "not_found")
	if e.Message != `no entity matched "garage door"; search for candidates with ha_search` || !e.Retryable {
		t.Fatalf("%+v", e)
	}
}

func TestHAControlResolutionOrder(t *testing.T) {
	r := newHARig(t, baseHouse())
	control := tools.HAControl(r.deps())
	for target, id := range map[string]string{
		"Reading Lamp":    "light.desk",      // an alias before a name
		"desk":            "light.desk",      // an exact name before a substring
		"switch.desk_fan": "switch.desk_fan", // an exact id
		"  DESK   fan ":   "switch.desk_fan", // whitespace and case do not count
		"fan":             "switch.desk_fan", // a substring of one
		"lamp_plug":       "switch.lamp_plug",
	} {
		out := r.ok(control, `{"action":"turn_off","target":"`+target+`"}`)
		if got := out["entity"].(map[string]any)["id"]; got != id {
			t.Fatalf("%q resolved to %v, want %s", target, got, id)
		}
	}
}

// seen live (v1): a zigbee bulb and its firmware update entity share a name,
// so «выключи свет» asked which one and switched nothing
func TestHAControlActsOnTheOneThatCanTakeTheAction(t *testing.T) {
	r := newHARig(t, baseHouse())
	control := tools.HAControl(r.deps())
	r.ok(control, `{"action":"turn_off","target":"Light Bulb"}`, haState("light.bulb", "off", named("Light Bulb")))
	r.ok(control, `{"action":"set_brightness","target":"Light Bulb","brightness":40}`, haState("light.bulb", "on", obj{"friendly_name": "Light Bulb", "brightness": 102}))
	r.ok(control, `{"action":"call_service","target":"Cleaner","service":"clean_spot"}`, haState("vacuum.cleaner", "cleaning", named("Cleaner")))
	want := []string{"homeassistant.turn_off light.bulb", `light.turn_on light.bulb {"brightness_pct":40}`, "vacuum.clean_spot vacuum.cleaner"}
	if got := r.calls(); !slices.Equal(got, want) {
		t.Fatalf("calls %v", got)
	}
}

func TestHAControlPrefersTheDevicesArea(t *testing.T) {
	r := newHARig(t, baseHouse())
	control := tools.HAControl(r.deps())
	if e := r.refused(control, `{"action":"turn_off","target":"Ceiling"}`, "ambiguous"); e.Message != "Several devices match: Ceiling (Bedroom), Ceiling (Kitchen main)." ||
		!slices.Equal(e.Candidates, []string{"Ceiling (Bedroom)", "Ceiling (Kitchen main)"}) {
		t.Fatalf("%+v", e)
	}
	r.setArea("garage")
	r.refused(control, `{"action":"turn_off","target":"Ceiling"}`, "ambiguous")
	r.setArea("bedroom")
	out := r.ok(control, `{"action":"turn_off","target":"Ceiling"}`)
	if got := out["entity"].(map[string]any)["id"]; got != "light.ceiling_bedroom" {
		t.Fatalf("resolved %v", got)
	}
	// the area narrows only among the ones that can take the action: the
	// sensor in it cannot be turned on
	if e := r.refused(control, `{"action":"turn_on","target":"hall"}`, "ambiguous"); e.Message != "Several devices match: Hall light, Hallway." {
		t.Fatalf("%+v", e)
	}
}

func TestHAControlCallsACheckedService(t *testing.T) {
	r := newHARig(t, baseHouse())
	control := tools.HAControl(r.deps())
	r.ok(control, `{"action":"call_service","target":"vacuum.cleaner","service":"set_fan_speed","fields":{"fan_speed":"max"}}`,
		haState("vacuum.cleaner", "docked", obj{"friendly_name": "Cleaner", "percentage": 100}))
	out := r.raw(control, `{"action":"call_service","target":"vacuum.cleaner","service":"vacuum.start"}`, haState("vacuum.cleaner", "cleaning", named("Cleaner")))
	if out != `{"done":"call_service","entity":{"id":"vacuum.cleaner","name":"Cleaner","state":"cleaning"}}` {
		t.Fatalf("%s", out)
	}
	// generic services pass their fields on unchecked
	r.ok(control, `{"action":"call_service","target":"vacuum.cleaner","service":"homeassistant.toggle","fields":{"x":1}}`,
		haState("vacuum.cleaner", "returning", named("Cleaner")))
	want := []string{
		`vacuum.set_fan_speed vacuum.cleaner {"fan_speed":"max"}`,
		"vacuum.start vacuum.cleaner",
		`homeassistant.toggle vacuum.cleaner {"x":1}`,
	}
	if got := r.calls(); !slices.Equal(got, want) {
		t.Fatalf("calls %v", got)
	}
	for args, msg := range map[string]string{
		`{"action":"call_service","target":"vacuum.cleaner","service":"set_fan_speed"}`:                                                   "missing required fields: fan_speed",
		`{"action":"call_service","target":"vacuum.cleaner","service":"set_fan_speed","fields":{"fan_speed":"max","made_up":1,"also":2}}`: "unknown fields: also, made_up",
		`{"action":"call_service","target":"vacuum.cleaner","service":"start","fields":{"entity_id":"vacuum.other"}}`:                     "entity_id is set by the tool; never pass it as a field",
		// before anything else: even an unknown device
		`{"action":"call_service","target":"nowhere","service":"start","fields":{"note":"` + strings.Repeat("x", 2000) + `"}}`: "fields JSON is over 2000 characters",
	} {
		if e := r.refused(control, args, "invalid_arguments"); e.Message != msg || !e.Retryable {
			t.Fatalf("%.80s: %+v", args, e)
		}
	}
}

func TestHAControlNeverWritesOnceTheTurnEnded(t *testing.T) {
	r := newHARig(t, baseHouse())
	ctx, cancel := context.WithCancel(context.Background())
	// the turn ends after the entity resolved, right before the call
	r.policy = func(context.Context, agent.Call, ha.Entity, string, string) *agent.ToolError {
		cancel()
		return nil
	}
	control := tools.HAControl(r.deps())
	c := agent.Call{DeviceID: "d1", Trigger: cadencev2.Trigger_TRIGGER_WAKE, Args: json.RawMessage(`{"action":"turn_on","target":"bedroom light"}`)}
	if _, e := control.Run(ctx, c); e == nil {
		t.Fatal("acted after the turn ended")
	}
	r.policy = nil
	if _, e := tools.HAControl(r.deps()).Run(ctx, c); e == nil {
		t.Fatal("acted in an ended turn")
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(r.fake.Calls()); n != 0 || len(r.usage.got()) != 0 {
		t.Fatalf("%d calls sent", n)
	}
}

func TestHAControlPolicy(t *testing.T) {
	r := newHARig(t, baseHouse())
	type seen struct{ entity, domain, service string }
	var got []seen
	r.policy = func(_ context.Context, c agent.Call, e ha.Entity, domain, service string) *agent.ToolError {
		got = append(got, seen{e.ID, domain, service})
		if c.Trigger != cadencev2.Trigger_TRIGGER_WAKE {
			return agent.WakeRequired()
		}
		return nil
	}
	control := tools.HAControl(r.deps())
	r.ok(control, `{"action":"turn_off","target":"bedroom light"}`)
	r.ok(control, `{"action":"call_service","target":"Cleaner","service":"clean_spot"}`, haState("vacuum.cleaner", "cleaning", named("Cleaner")))
	if !slices.Equal(got, []seen{{"light.bedroom", "homeassistant", "turn_off"}, {"vacuum.cleaner", "vacuum", "clean_spot"}}) {
		t.Fatalf("policy saw %v", got)
	}
	_, e := run(t, control, cadencev2.Trigger_TRIGGER_FOLLOW_UP, `{"action":"turn_off","target":"bedroom light"}`)
	if e == nil || e.Code != "wake_required" {
		t.Fatalf("%+v", e)
	}
	r.usage.count(t, 2)
	if n := len(r.fake.Calls()); n != 2 {
		t.Fatalf("%d calls after a refusal", n)
	}
}

func TestHAControlFailures(t *testing.T) {
	r := newHARig(t, baseHouse())
	control := tools.HAControl(r.deps())
	args := `{"action":"turn_off","target":"bedroom light"}`
	for code, want := range map[string]string{
		"service_validation_error": "invalid_arguments",
		"invalid_format":           "invalid_arguments",
		"not_found":                "not_supported",
		"home_assistant_error":     "unavailable",
		"unknown_error":            "unavailable",
	} {
		r.fake.Fail("call_service", code)
		out, e := r.act(control, args)
		if e == nil || e.Code != want || strings.Contains(e.Message, hatest.Leak) {
			t.Fatalf("%s: %v %+v", code, out, e)
		}
	}
	// refused data: only call_service points at ha_inspect; a fixed action's
	// value may be out of the device's range
	r.fake.Fail("call_service", "service_validation_error")
	for args, inspect := range map[string]bool{
		`{"action":"set_color_temp","target":"kitchen light","kelvin":1000}`:                                       false,
		`{"action":"call_service","target":"vacuum.cleaner","service":"set_fan_speed","fields":{"fan_speed":"x"}}`: true,
	} {
		_, e := r.act(control, args)
		if e == nil || e.Code != "invalid_arguments" || e.Hint == "" || strings.Contains(e.Hint, "ha_inspect") != inspect {
			t.Fatalf("%s: %+v", args, e)
		}
	}
	r.fake.Fail("call_service", "")
	if len(r.usage.got()) != 0 {
		t.Fatalf("failed calls counted as use: %v", r.usage.got())
	}
	// home assistant hangs: the call times out, and is not offered for a retry
	r.fake.Hold("call_service")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, e := control.Run(ctx, agent.Call{DeviceID: "d1", Trigger: cadencev2.Trigger_TRIGGER_WAKE, Args: json.RawMessage(args)})
	if e == nil || e.Code != "timeout" || e.Retryable {
		t.Fatalf("hang: %+v", e)
	}
	r.fake.Release()
	if len(r.usage.got()) != 0 {
		t.Fatal("a timed-out call counted as use")
	}
	// the turn ends while home assistant holds the call: it went out, so the
	// answer never says nothing happened
	r.fake.Hold("call_service")
	turn, end := context.WithCancel(context.Background())
	defer end()
	done := make(chan *agent.ToolError, 1)
	go func() {
		_, e := control.Run(turn, agent.Call{DeviceID: "d1", Trigger: cadencev2.Trigger_TRIGGER_WAKE, Args: json.RawMessage(args)})
		done <- e
	}()
	waitUntil(t, "the held call", func() bool { return r.fake.Held() > 0 })
	end()
	if e := <-done; e == nil || e.Code != "canceled" || e.Message != "The turn ended; the change may still happen." || e.Retryable {
		t.Fatalf("ended mid-call: %+v", e)
	}
	r.fake.Release()
	if len(r.usage.got()) != 0 {
		t.Fatal("a call the turn left counted as use")
	}
}

func TestHAToolsWhileDisconnected(t *testing.T) {
	r := newHARig(t, baseHouse())
	control, search, inspect := tools.HAControl(r.deps()), tools.HASearch(r.deps()), tools.HAInspect(r.deps())
	for _, tl := range []agent.Tool{search, inspect} {
		if !strings.Contains(tl.Description, "may be old while Home Assistant reconnects") {
			t.Fatalf("%s: the description does not say a read may be old", tl.Name)
		}
	}
	for _, out := range []map[string]any{r.ok(search, `{"query":"boiler"}`), r.ok(inspect, `{"target":"boiler"}`)} {
		if _, ok := out["stale"]; ok {
			t.Fatalf("a fresh read marked stale: %v", out)
		}
	}
	r.fake.Restart(time.Hour)
	waitUntil(t, "disconnected", func() bool { return !r.home.Client().Connected() })
	if e := r.refused(control, `{"action":"turn_off","target":"bedroom light"}`, "unavailable"); e.Message == "" {
		t.Fatalf("%+v", e)
	}
	// reads keep the last view for ten minutes, marked stale, then refuse
	for _, out := range []map[string]any{r.ok(search, `{"query":"boiler"}`), r.ok(inspect, `{"target":"boiler"}`)} {
		if out["stale"] != true {
			t.Fatalf("a read of the old view not marked stale: %v", out)
		}
	}
	r.clock.Advance(11 * time.Minute)
	r.refused(search, `{"query":"boiler"}`, "unavailable")
	r.refused(inspect, `{"target":"boiler"}`, "unavailable")
	r.refused(control, `{"action":"turn_off","target":"bedroom light"}`, "unavailable")
	// never set up at all
	for _, tl := range []agent.Tool{tools.HAControl(tools.HADeps{}), tools.HASearch(tools.HADeps{}), tools.HAInspect(tools.HADeps{})} {
		if _, e := tl.Run(context.Background(), agent.Call{Args: json.RawMessage(`{"action":"turn_on","target":"x","query":"x"}`)}); e == nil || e.Code != "unavailable" {
			t.Fatalf("%s: %+v", tl.Name, e)
		}
	}
}

// home assistant reads these in service data as targets: one in fields would
// reach past the resolved entity, its policy and its usage
func TestHAControlRefusesTargetsInFields(t *testing.T) {
	r := newHARig(t, baseHouse())
	control := tools.HAControl(r.deps())
	for _, key := range []string{"entity_id", "area_id", "device_id", "floor_id", "label_id"} {
		for _, service := range []string{"homeassistant.turn_on", "toggle", "vacuum.start"} {
			args := `{"action":"call_service","target":"vacuum.cleaner","service":"` + service + `","fields":{"` + key + `":"hall"}}`
			if e := r.refused(control, args, "invalid_arguments"); e.Message != key+" is set by the tool; never pass it as a field" || !e.Retryable {
				t.Fatalf("%s: %+v", args, e)
			}
		}
	}
}

// speakerHouse is baseHouse with a speech engine and two players whose
// services take fields that name other devices, as home assistant's tts.speak
// and media_player.join do, beside a made-up service with each other kind
// of such selector.
func speakerHouse() haHouse {
	h := baseHouse()
	h.states = append(h.states,
		haState("tts.google", "unknown", named("Google speech")),
		haState("media_player.den", "idle", named("Den speaker")),
		haState("media_player.patio", "idle", named("Patio speaker")),
	)
	h.services = json.RawMessage(strings.Replace(string(h.services.(json.RawMessage)), `"update":`, `
		"tts": {"speak": {"fields": {
			"media_player_entity_id": {"required": true, "selector": {"entity": {"domain": "media_player", "multiple": true}}},
			"message": {"required": true, "selector": {"text": {}}},
			"cache": {"selector": {"boolean": {}}}
		}}},
		"media_player": {
			"join": {"fields": {"group_members": {"required": true, "selector": {"entity": {"domain": "media_player", "multiple": true}}}}},
			"volume_set": {"fields": {"volume_level": {"required": true, "selector": {"number": {"min": 0, "max": 1}}}}},
			"announce": {"fields": {
				"message": {"required": true, "selector": {"text": {}}},
				"devices": {"selector": {"device": {}}}, "rooms": {"selector": {"area": {}}}, "storey": {"selector": {"floor": {}}},
				"tags": {"selector": {"label": {}}}, "where": {"selector": {"target": {}}}
			}},
			"turn_on": {"fields": {"sync_with": {"selector": {"entity": {"domain": "media_player"}}}}}
		},
		"update":`, 1))
	return h
}

// ruling m5c-r9: one call acts on the one resolved entity, so a field whose
// selector names other devices (entity, device, area, floor, label, target)
// is refused, a required one even when left out: the service can never run
// on one device. the generic domain forwards its fields to the entity's own
// service of that name, whose fields count.
func TestHAControlRefusesFieldsNamingOtherDevices(t *testing.T) {
	r := newHARig(t, speakerHouse())
	control := tools.HAControl(r.deps())
	for args, field := range map[string]string{
		`{"action":"call_service","target":"tts.google","service":"tts.speak","fields":{"message":"hi","media_player_entity_id":"media_player.patio"}}`: "media_player_entity_id",
		`{"action":"call_service","target":"tts.google","service":"speak","fields":{"message":"hi"}}`:                                                   "media_player_entity_id",
		`{"action":"call_service","target":"media_player.den","service":"join","fields":{"group_members":["media_player.patio"]}}`:                      "group_members",
		`{"action":"call_service","target":"media_player.den","service":"media_player.join"}`:                                                           "group_members",
		`{"action":"call_service","target":"media_player.den","service":"announce","fields":{"message":"hi","devices":"abc"}}`:                          "devices",
		`{"action":"call_service","target":"media_player.den","service":"announce","fields":{"message":"hi","rooms":"hall"}}`:                           "rooms",
		`{"action":"call_service","target":"media_player.den","service":"announce","fields":{"message":"hi","storey":"up"}}`:                            "storey",
		`{"action":"call_service","target":"media_player.den","service":"announce","fields":{"message":"hi","tags":"all"}}`:                             "tags",
		`{"action":"call_service","target":"media_player.den","service":"announce","fields":{"message":"hi","where":{"area_id":"hall"}}}`:               "where",
		`{"action":"call_service","target":"media_player.den","service":"homeassistant.turn_on","fields":{"sync_with":"media_player.patio"}}`:           "sync_with",
		`{"action":"call_service","target":"media_player.den","service":"turn_on","fields":{"sync_with":"media_player.patio"}}`:                         "sync_with",
	} {
		e := r.refused(control, args, "invalid_arguments")
		if e.Message != field+" names other devices; the tool acts on one device only" {
			t.Fatalf("%s: %+v", args, e)
		}
	}
	// the same services without such a field act on the one device
	r.ok(control, `{"action":"call_service","target":"media_player.den","service":"announce","fields":{"message":"hi"}}`)
	r.ok(control, `{"action":"call_service","target":"media_player.den","service":"volume_set","fields":{"volume_level":0.2}}`)
	r.ok(control, `{"action":"call_service","target":"media_player.den","service":"homeassistant.turn_on"}`)
	want := []string{
		`media_player.announce media_player.den {"message":"hi"}`,
		`media_player.volume_set media_player.den {"volume_level":0.2}`,
		`homeassistant.turn_on media_player.den`,
	}
	if got := r.calls(); !slices.Equal(got, want) {
		t.Fatalf("calls %v", got)
	}
}

// ruling m5c-r5: the generic domain only switches the device; stopping or
// restarting home assistant itself is never a device's service
func TestHAControlOnlySwitchesThroughTheGenericDomain(t *testing.T) {
	r := newHARig(t, baseHouse())
	control := tools.HAControl(r.deps())
	for _, args := range []string{
		`{"action":"call_service","target":"bedroom light","service":"homeassistant.stop"}`,
		`{"action":"call_service","target":"bedroom light","service":"homeassistant.restart"}`,
		`{"action":"call_service","target":"bedroom light","service":"restart"}`,
		`{"action":"call_service","target":"bedroom light","service":"homeassistant.reload_all"}`,
		`{"action":"call_service","target":"bedroom light","service":"homeassistant.set_location","fields":{"latitude":1,"longitude":2}}`,
		`{"action":"call_service","target":"vacuum.cleaner","service":"set_location","fields":{"latitude":1,"longitude":2}}`,
	} {
		r.refused(control, args, "not_supported")
	}
	r.ok(control, `{"action":"call_service","target":"bedroom light","service":"homeassistant.turn_off"}`)
	if got := r.calls(); !slices.Equal(got, []string{"homeassistant.turn_off light.bedroom"}) {
		t.Fatalf("calls %v", got)
	}
}
