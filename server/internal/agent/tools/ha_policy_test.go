package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

// guardedHouse is baseHouse with a device of each sensitive kind, and covers
// that open a way into the house beside ones that do not.
func guardedHouse(t *testing.T) haHouse {
	t.Helper()
	h := baseHouse()
	h.states = append(h.states,
		haState("lock.front_door", "locked", named("Front door lock")),
		haState("alarm_control_panel.home", "armed_away", named("House alarm")),
		haState("valve.water_main", "open", named("Water main")),
		haState("siren.hall", "off", named("Hall siren")),
		haState("script.goodnight", "off", named("Goodnight")),
		haState("automation.morning", "on", named("Morning routine")),
		haState("button.gate_open", "unknown", named("Open the gate")),
		haState("cover.garage", "closed", obj{"friendly_name": "Garage door", "device_class": "garage"}),
		haState("cover.driveway_gate", "closed", obj{"friendly_name": "Driveway gate", "device_class": "gate"}),
		haState("cover.back_door", "closed", obj{"friendly_name": "Back door", "device_class": "door"}),
		haState("cover.curtain", "closed", obj{"friendly_name": "Curtain", "device_class": "curtain"}),
		haState("cover.awning", "closed", named("Awning")),
		haState("scene.evening", "unknown", named("Evening")),
		haState("group.downstairs", "off", named("Downstairs")),
		haState("input_button.gate_bell", "unknown", named("Gate bell")),
		haState("conversation.home_assistant", "unknown", named("Home Assistant")),
	)
	var svcs map[string]map[string]any
	if err := json.Unmarshal(h.services.(json.RawMessage), &svcs); err != nil {
		t.Fatal(err)
	}
	plain := func(names ...string) map[string]any {
		m := map[string]any{}
		for _, n := range names {
			m[n] = obj{"fields": obj{}}
		}
		return m
	}
	svcs["lock"] = plain("lock", "unlock", "open")
	svcs["alarm_control_panel"] = map[string]any{"alarm_disarm": obj{"fields": obj{"code": obj{}}}, "alarm_arm_away": obj{"fields": obj{"code": obj{}}}}
	svcs["valve"] = plain("open_valve", "close_valve", "toggle")
	svcs["siren"] = plain("turn_on", "turn_off", "toggle")
	svcs["script"] = plain("turn_on", "turn_off", "toggle")
	svcs["automation"] = plain("turn_on", "turn_off", "toggle", "trigger")
	svcs["button"] = plain("press")
	svcs["cover"] = plain("open_cover", "close_cover", "toggle")
	svcs["scene"] = plain("turn_on")
	svcs["input_button"] = plain("press")
	svcs["conversation"] = map[string]any{"process": obj{"fields": obj{
		"text": obj{"required": true, "selector": obj{"text": obj{}}}, "agent_id": obj{"selector": obj{"conversation_agent": obj{}}},
	}}}
	h.services = svcs
	return h
}

type guarded struct{ args, call string }

// sensitiveControls reach each sensitive kind, the indirect paths included:
// the generic domain, a bare or prefixed service, a cover by name, and a
// scene or group that fans out.
var sensitiveControls = []guarded{
	{`{"action":"turn_off","target":"Front door lock"}`, "homeassistant.turn_off lock.front_door"},
	{`{"action":"call_service","target":"lock.front_door","service":"homeassistant.turn_off"}`, "homeassistant.turn_off lock.front_door"},
	{`{"action":"call_service","target":"lock.front_door","service":"unlock"}`, "lock.unlock lock.front_door"},
	{`{"action":"call_service","target":"lock.front_door","service":"lock.unlock"}`, "lock.unlock lock.front_door"},
	{`{"action":"call_service","target":"alarm_control_panel.home","service":"alarm_disarm"}`, "alarm_control_panel.alarm_disarm alarm_control_panel.home"},
	{`{"action":"call_service","target":"water main","service":"open_valve"}`, "valve.open_valve valve.water_main"},
	{`{"action":"turn_on","target":"siren.hall"}`, "homeassistant.turn_on siren.hall"},
	{`{"action":"turn_on","target":"goodnight"}`, "homeassistant.turn_on script.goodnight"},
	{`{"action":"call_service","target":"automation.morning","service":"trigger"}`, "automation.trigger automation.morning"},
	{`{"action":"call_service","target":"update.cleaner","service":"update.install"}`, "update.install update.cleaner"},
	{`{"action":"call_service","target":"button.gate_open","service":"press"}`, "button.press button.gate_open"},
	{`{"action":"call_service","target":"garage door","service":"open_cover"}`, "cover.open_cover cover.garage"},
	{`{"action":"toggle","target":"cover.driveway_gate"}`, "homeassistant.toggle cover.driveway_gate"},
	{`{"action":"call_service","target":"back door","service":"cover.close_cover"}`, "cover.close_cover cover.back_door"},
	// ruling m5c-r6: a scene or an old-style group fans out to whatever it holds
	{`{"action":"call_service","target":"scene.evening","service":"turn_on"}`, "scene.turn_on scene.evening"},
	{`{"action":"turn_on","target":"Downstairs"}`, "homeassistant.turn_on group.downstairs"},
	// a helper button runs whatever is bound to it; a conversation agent acts
	// on whatever its text asks
	{`{"action":"call_service","target":"input_button.gate_bell","service":"press"}`, "input_button.press input_button.gate_bell"},
	{`{"action":"call_service","target":"conversation.home_assistant","service":"process","fields":{"text":"hello"}}`,
		`conversation.process conversation.home_assistant {"text":"hello"}`},
}

// ordinaryControls never need a woken turn: a curtain, a cover nobody
// classed, a light.
var ordinaryControls = []guarded{
	{`{"action":"call_service","target":"curtain","service":"open_cover"}`, "cover.open_cover cover.curtain"},
	{`{"action":"toggle","target":"cover.awning"}`, "homeassistant.toggle cover.awning"},
	{`{"action":"turn_off","target":"bedroom light"}`, "homeassistant.turn_off light.bedroom"},
}

func newGuardedRig(t *testing.T) *haRig {
	t.Helper()
	return guardRig(t, guardedHouse(t))
}

// guardRig serves h behind the real sensitive policy.
func guardRig(t *testing.T, h haHouse) *haRig {
	t.Helper()
	r := newHARig(t, h)
	r.policy = tools.HASensitivePolicy(r.settings)
	attrs := map[string]any{}
	for _, s := range h.states {
		attrs[s.(obj)["entity_id"].(string)] = s.(obj)["attributes"]
	}
	r.attrs = attrs
	return r
}

// moved is the state home assistant reports once c's call landed: on, or off
// when it was on (a cover opens and closes), the attributes kept, so the
// control's read-back (a toggle's too) ends at once.
func (r *haRig) moved(c guarded) obj {
	r.t.Helper()
	id := strings.Fields(c.call)[1]
	v, err := r.home.View()
	if err != nil {
		r.t.Fatal(err)
	}
	e, _ := v.ByID(id)
	next := "on"
	if e.Domain == "cover" {
		next = "open"
		if e.State == "open" || e.State == "opening" {
			next = "closed"
		}
	} else if e.State == "on" {
		next = "off"
	}
	return haState(id, next, r.attrs[id].(obj))
}

// acts runs c in a session trig began and checks the one call it made.
func (r *haRig) acts(trig cadencev2.Trigger, control agent.Tool, c guarded) {
	r.t.Helper()
	before := len(r.fake.Calls())
	if _, e := r.actIn(trig, control, c.args, r.moved(c)); e != nil {
		r.t.Fatalf("%s %s: %+v", trig, c.args, e)
	}
	if got := r.calls()[before:]; !slices.Equal(got, []string{c.call}) {
		r.t.Fatalf("%s %s: calls %v", trig, c.args, got)
	}
}

func TestHASensitiveDevicesNeedAWokenTurn(t *testing.T) {
	r := newGuardedRig(t)
	control := tools.HAControl(r.deps())
	for _, c := range sensitiveControls {
		if e := r.refusedIn(followUp, control, c.args, "wake_required"); !reflect.DeepEqual(e, agent.WakeRequired()) {
			t.Fatalf("%s: %+v", c.args, e)
		}
	}
	for _, c := range sensitiveControls {
		r.acts(wake, control, c)
	}
	r.acts(button, control, sensitiveControls[2])
	for _, c := range ordinaryControls {
		r.acts(followUp, control, c)
	}
	r.usage.count(t, len(sensitiveControls)+1+len(ordinaryControls))
}

func TestHASensitiveDevicesDenied(t *testing.T) {
	r := newGuardedRig(t)
	r.set(func(r *haRig) { r.sensitive = settings.HASensitiveDeny })
	control := tools.HAControl(r.deps())
	for _, trig := range []cadencev2.Trigger{wake, button, followUp} {
		for _, c := range sensitiveControls {
			e := r.refusedIn(trig, control, c.args, "not_supported")
			if e.Retryable || !strings.Contains(e.Message, "turned off in the settings") || e.Hint == "" {
				t.Fatalf("%s %s: %+v", trig, c.args, e)
			}
		}
	}
	for _, c := range ordinaryControls {
		r.acts(followUp, control, c)
	}
	r.usage.count(t, len(ordinaryControls))
}

// a sensitive device stays guarded when the policy cannot be read; the others
// never read it
func TestHASensitiveDevicesWhenSettingsFail(t *testing.T) {
	r := newGuardedRig(t)
	r.set(func(r *haRig) { r.settingsErr = errors.New("db down") })
	control := tools.HAControl(r.deps())
	for _, trig := range []cadencev2.Trigger{wake, followUp} {
		r.refusedIn(trig, control, sensitiveControls[0].args, "internal")
	}
	r.acts(followUp, control, ordinaryControls[2])
}

// an event turn is never offered ha_control; were it run there anyway,
// nothing in the home changes
func TestHAControlRefusedInEventTurns(t *testing.T) {
	r := newGuardedRig(t)
	control := tools.HAControl(r.deps())
	for _, c := range append([]guarded{sensitiveControls[0]}, ordinaryControls...) {
		if e := r.refusedIn(event, control, c.args, "not_in_event_turn"); !reflect.DeepEqual(e, agent.NotInEventTurn()) {
			t.Fatalf("%s: %+v", c.args, e)
		}
	}
}

// task 3's guards, kept here beside the policy so a refactor of either cannot
// drop them unseen: home assistant itself is never a device's service, and
// the model never names a target in the fields
func TestHAControlNeverActsOnHomeAssistantItself(t *testing.T) {
	r := newGuardedRig(t)
	control := tools.HAControl(r.deps())
	for _, trig := range []cadencev2.Trigger{wake, button, followUp} {
		for _, args := range []string{
			`{"action":"call_service","target":"bedroom light","service":"homeassistant.restart"}`,
			`{"action":"call_service","target":"bedroom light","service":"homeassistant.stop"}`,
			`{"action":"call_service","target":"lock.front_door","service":"homeassistant.reload_all"}`,
		} {
			r.refusedIn(trig, control, args, "not_supported")
		}
		r.refusedIn(trig, control, `{"action":"call_service","target":"curtain","service":"homeassistant.turn_on","fields":{"entity_id":"lock.front_door"}}`, "invalid_arguments")
	}
}

// cadenceHouse is guardedHouse with a Cadence speaker's own entities (Home
// Assistant platform cadence: its player, its microphone switch, its volume)
// beside a television another integration provides.
func cadenceHouse(t *testing.T) haHouse {
	t.Helper()
	h := guardedHouse(t)
	h.states = append(h.states,
		haState("media_player.kitchen_speaker", "idle", named("Kitchen speaker")),
		haState("switch.kitchen_speaker_mic", "on", named("Kitchen speaker microphone off")),
		haState("number.kitchen_speaker_volume", "30", named("Kitchen speaker volume")),
		haState("media_player.living_tv", "on", named("Living room TV")),
	)
	h.entities = append(h.entities,
		haReg("media_player.kitchen_speaker", obj{"platform": "cadence"}),
		haReg("switch.kitchen_speaker_mic", obj{"platform": "cadence"}),
		haReg("number.kitchen_speaker_volume", obj{"platform": "cadence"}),
		haReg("media_player.living_tv", obj{"platform": "cast"}),
	)
	svcs := h.services.(map[string]map[string]any)
	svcs["media_player"] = map[string]any{
		"volume_set":  obj{"fields": obj{"volume_level": obj{"required": true, "selector": obj{"number": obj{"min": 0, "max": 1}}}}},
		"volume_mute": obj{"fields": obj{"is_volume_muted": obj{"required": true, "selector": obj{"boolean": obj{}}}}},
		"turn_on":     obj{"fields": obj{}}, "turn_off": obj{"fields": obj{}},
	}
	svcs["number"] = map[string]any{"set_value": obj{"fields": obj{"value": obj{"required": true, "selector": obj{"number": obj{}}}}}}
	return h
}

// ruling m5c-r8: a Cadence speaker's own entities (its volume, its
// microphone) change only through Cadence's own tools, never through Home
// Assistant: in every turn, with or without the policy. Another
// integration's player still takes its volume.
func TestHAControlLeavesCadenceDevicesAlone(t *testing.T) {
	r := guardRig(t, cadenceHouse(t))
	own := []string{
		`{"action":"call_service","target":"Kitchen speaker","service":"volume_set","fields":{"volume_level":1}}`,
		`{"action":"call_service","target":"media_player.kitchen_speaker","service":"media_player.volume_mute","fields":{"is_volume_muted":false}}`,
		`{"action":"turn_on","target":"media_player.kitchen_speaker"}`,
		`{"action":"turn_off","target":"switch.kitchen_speaker_mic"}`,
		`{"action":"call_service","target":"number.kitchen_speaker_volume","service":"set_value","fields":{"value":100}}`,
		`{"action":"call_service","target":"number.kitchen_speaker_volume","service":"homeassistant.turn_on"}`,
	}
	unguarded := r.deps()
	unguarded.Policy = nil
	for _, control := range []agent.Tool{tools.HAControl(r.deps()), tools.HAControl(unguarded)} {
		for _, trig := range []cadencev2.Trigger{followUp, wake, button} {
			for _, args := range own {
				e := r.refusedIn(trig, control, args, "not_supported")
				if e.Retryable || !strings.Contains(e.Message, "Cadence") || !strings.Contains(e.Hint, "Cadence's own tools") {
					t.Fatalf("%s %s: %+v", trig, args, e)
				}
			}
		}
	}
	r.acts(followUp, tools.HAControl(r.deps()), guarded{
		`{"action":"call_service","target":"Living room TV","service":"volume_set","fields":{"volume_level":0.5}}`,
		`media_player.volume_set media_player.living_tv {"volume_level":0.5}`,
	})
	r.usage.count(t, 1)
}

// m8-d6: home assistant lists a new entity in its registry before its first
// state, so while that list is pending or failed, an entity with a state and
// no entry may be a Cadence device's own: ha_control refuses it, and any
// other entity without an entry, until a list lands (a failed one is tried
// again), then acts on another integration's and still leaves Cadence's alone.
func TestHAControlRefusesEntitiesTheRegistryHasNotListed(t *testing.T) {
	h := cadenceHouse(t)
	r := guardRig(t, h)
	control := tools.HAControl(r.deps())
	r.fake.Hold("config/entity_registry/list")
	for _, s := range []obj{
		haState("number.porch_speaker_volume", "30", named("Porch speaker volume")),
		haState("light.garden", "off", named("Garden light")),
	} {
		id := s["entity_id"].(string)
		r.fake.Emit("entity_registry_updated", obj{"action": "create", "entity_id": id})
		r.fake.Emit("state_changed", obj{"entity_id": id, "old_state": nil, "new_state": s})
		h.states = append(h.states, s)
		r.attrs[id] = s["attributes"]
	}
	waitUntil(t, "the new entities", func() bool {
		v, err := r.home.View()
		_, porch := v.ByID("number.porch_speaker_volume")
		_, garden := v.ByID("light.garden")
		return err == nil && porch && garden
	})
	unlisted := []string{
		`{"action":"call_service","target":"Porch speaker volume","service":"set_value","fields":{"value":100}}`,
		`{"action":"turn_on","target":"Garden light"}`,
		// no entry at rest either: in the window it may be anyone's
		`{"action":"turn_off","target":"light.bulb"}`,
	}
	refuse := func(when string) {
		t.Helper()
		for _, args := range unlisted {
			e := r.refusedIn(button, control, args, "unavailable")
			if e.Retryable || !strings.Contains(e.Message, "not finished listing") {
				t.Fatalf("%s %s: %+v", when, args, e)
			}
		}
	}
	refuse("pending")
	waitUntil(t, "the re-list in flight", func() bool { return r.fake.Held() == 1 })
	refuse("running")
	r.fake.Fail("config/entity_registry/list", "unknown_error")
	r.fake.Release()
	waitUntil(t, "the failed re-list", func() bool {
		return strings.Contains(r.logs.String(), "msg=ha_reload_failed what=entities code=ha_error:unknown_error")
	})
	refuse("failed")
	h.entities = append(h.entities,
		haReg("number.porch_speaker_volume", obj{"platform": "cadence"}),
		haReg("light.garden", obj{"platform": "hue"}))
	h.script(r.fake)
	r.fake.Fail("config/entity_registry/list", "")
	waitUntil(t, "the retried list", func() bool {
		v, _ := r.home.View()
		e, _ := v.ByID("light.garden")
		return e.Platform == "hue"
	})
	r.acts(button, control, guarded{`{"action":"turn_on","target":"Garden light"}`, "homeassistant.turn_on light.garden"})
	r.acts(button, control, guarded{`{"action":"turn_off","target":"light.bulb"}`, "homeassistant.turn_off light.bulb"})
	if e := r.refusedIn(button, control, unlisted[0], "not_supported"); !strings.Contains(e.Message, "Cadence") {
		t.Fatalf("listed as cadence: %+v", e)
	}
}

// an entry the registry holds but the server cannot read is refused for good:
// its platform stays unknown until Home Assistant's registry changes, so the
// model is not told to try again in a moment
func TestHAControlRefusesAnUnreadableEntryForGood(t *testing.T) {
	h := cadenceHouse(t)
	h.states = append(h.states, haState("switch.porch_fan", "off", named("Porch fan")))
	h.entities = append(h.entities, haReg("switch.porch_fan", obj{"name": strings.Repeat("n", 101), "platform": "hue"}))
	r := guardRig(t, h)
	control := tools.HAControl(r.deps())
	e := r.refusedIn(button, control, `{"action":"turn_on","target":"switch.porch_fan"}`, "unavailable")
	if e.Retryable || !strings.Contains(e.Message, "can't be read") || strings.Contains(e.Hint, "moment") {
		t.Fatalf("unreadable: %+v", e)
	}
}

// the real store behind ha_control: a change counts once it landed, never a
// refusal or a failed call, and a count that cannot be kept never fails the
// control
func TestHAControlRanksWhatItChanged(t *testing.T) {
	pool := storetest.Pool(t)
	ctx := context.Background()
	dev, err := db.New(pool).CreateDevice(ctx, db.CreateDeviceParams{Name: "d", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	var logs syncBuffer
	usage := ha.NewUsage(db.New(pool), slog.New(slog.NewTextHandler(&logs, nil)))
	r := newGuardedRig(t)
	d := r.deps()
	d.Usage = usage
	control := tools.HAControl(d)
	call := func(trig cadencev2.Trigger, device, args string) *agent.ToolError {
		_, e := control.Run(ctx, agent.Call{DeviceID: device, Trigger: trig, Args: json.RawMessage(args)})
		return e
	}
	r.fake.Fail("call_service", "home_assistant_error")
	if e := call(wake, dev.ID.String(), `{"action":"turn_on","target":"kitchen light"}`); e == nil {
		t.Fatal("a failed call answered")
	}
	r.fake.Fail("call_service", "")
	if e := call(followUp, dev.ID.String(), `{"action":"turn_off","target":"Front door lock"}`); e == nil || e.Code != "wake_required" {
		t.Fatalf("follow-up lock: %+v", e)
	}
	for range 2 {
		if e := call(wake, dev.ID.String(), `{"action":"turn_off","target":"bedroom light"}`); e != nil {
			t.Fatal(e)
		}
	}
	var top []ha.Scored
	waitUntil(t, "the uses counted", func() bool {
		top, err = usage.Top(ctx, dev.ID.Bytes, 10)
		return err == nil && len(top) > 0 && top[0].Score > 1.9
	})
	if len(top) != 1 || top[0].EntityID != "light.bedroom" || top[0].Score > 2 {
		t.Fatalf("top %+v", top)
	}
	if e := call(wake, "d1", `{"action":"turn_off","target":"bedroom light"}`); e != nil {
		t.Fatalf("a count the store refused failed the control: %+v", e)
	}
	waitUntil(t, "the refused count logged", func() bool { return strings.Contains(logs.String(), "msg=ha_usage_failed code=bad_device") })
	if strings.Contains(logs.String(), "light.") || strings.Contains(logs.String(), "bedroom") {
		t.Fatalf("an id in the log: %s", logs.String())
	}
}
