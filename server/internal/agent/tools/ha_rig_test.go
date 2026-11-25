package tools_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha/hatest"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

const haToken = "ha-tools-token-never-printed"

type obj = map[string]any

// haHouse is what the fake answers to the home's load commands.
type haHouse struct {
	states   []any
	services any
	areas    []any
	devices  []any
	entities []any
	// entries is config/entity_registry/get_entries by entity id
	entries map[string]any
}

func haState(id, st string, attrs obj) obj {
	return obj{"entity_id": id, "state": st, "attributes": attrs, "context": obj{"id": "c"}}
}

func named(name string) obj { return obj{"friendly_name": name} }

func haReg(id string, f obj) obj {
	m := obj{"entity_id": id, "name": nil, "original_name": nil, "area_id": nil, "device_id": nil, "disabled_by": nil}
	for k, v := range f {
		m[k] = v
	}
	return m
}

// baseHouse has a zigbee bulb and a vacuum beside their firmware update twins,
// a name two areas share, names that differ only by alias, exact name or
// substring, and text that needs sanitizing.
func baseHouse() haHouse {
	return haHouse{
		states: []any{
			haState("light.bedroom", "off", obj{"friendly_name": "Bedroom light", "brightness": nil}),
			haState("light.kitchen", "on", obj{"friendly_name": "Kitchen light", "brightness": 128, "color_temp_kelvin": 2700}),
			haState("light.bulb", "on", named("Light Bulb")),
			haState("update.bulb", "off", named("Light Bulb")),
			haState("vacuum.cleaner", "docked", named("Cleaner")),
			haState("update.cleaner", "off", named("Cleaner")),
			haState("climate.living_room", "heat", obj{
				"friendly_name": "Living room", "temperature": 21, "current_temperature": 20.5, "hvac_action": "heating",
			}),
			haState("water_heater.boiler", "eco", obj{
				"friendly_name": "Boiler", "temperature": 55, "operation_mode": "eco", "supported_features": 9,
				"operation_list": []string{"off", "eco", "electric"}, "min_temp": 20, "max_temp": 90, "current_temperature": 48,
			}),
			// a kettle without the on/off feature: it heats by operation mode
			haState("water_heater.kettle", "off", obj{
				"friendly_name": "Kettle", "temperature": 100, "current_temperature": 65, "supported_features": 1,
				"operation_list": []string{"off", "on"}, "min_temp": 40, "max_temp": 100,
			}),
			haState("light.hall", "off", named("Hall light")),
			haState("sensor.hall", "on", named("Hall sensor")),
			haState("switch.hallway", "off", named("Hallway")),
			haState("light.ceiling_bedroom", "off", named("Ceiling")),
			haState("light.ceiling_kitchen", "off", named("Ceiling")),
			haState("light.desk", "off", named("Desk")),
			haState("light.reading", "off", named("Reading lamp")),
			haState("switch.desk_fan", "off", named("Desk fan")),
			haState("switch.lamp_plug", "off", named("Lamp")),
			haState("switch.odd", "on", named("Odd \"plug\"\u0007\n  name")),
			haState("sensor.feed", "breaking: someone wrote a long headline", named("News feed")),
			haState("input_text.note", "buy milk", named("Note")),
			haState("sensor.temp", "21.5", obj{"friendly_name": "Temperature", "unit_of_measurement": "°C", "device_class": "temperature"}),
			haState("light.wall_switch", "off", named("Wall switch")),
		},
		// raw, so the fields keep home assistant's order
		services: json.RawMessage(`{
			"homeassistant": {
				"turn_on": {"fields": {}}, "turn_off": {"fields": {}}, "toggle": {"fields": {}},
				"stop": {"fields": {}}, "restart": {"fields": {}}, "reload_all": {"fields": {}},
				"set_location": {"fields": {"latitude": {"required": true}, "longitude": {"required": true}}}
			},
			"light": {
				"turn_on": {"description": "Turn on one or more lights.", "fields": {
					"brightness_pct": {"selector": {"number": {"min": 0, "max": 100, "unit_of_measurement": "%"}}},
					"color_temp_kelvin": {"selector": {"color_temp": {}}}
				}},
				"turn_off": {"fields": {}}, "toggle": {"fields": {}}
			},
			"switch": {"turn_on": {"fields": {}}, "turn_off": {"fields": {}}, "toggle": {"fields": {}}},
			"climate": {
				"set_temperature": {"fields": {"temperature": {"required": true, "selector": {"number": {"min": 7, "max": 35}}}}},
				"set_hvac_mode": {"fields": {"hvac_mode": {"required": true}}},
				"turn_on": {"fields": {}}, "turn_off": {"fields": {}}
			},
			"water_heater": {
				"set_temperature": {"fields": {"temperature": {"required": true}}},
				"set_operation_mode": {"fields": {"operation_mode": {"required": true}}},
				"turn_on": {"fields": {}}, "turn_off": {"fields": {}}
			},
			"vacuum": {
				"start": {"fields": {}},
				"clean_spot": {"description": "Spot \"clean\"\u0007 now.", "fields": {}},
				"set_fan_speed": {"fields": {"fan_speed": {"required": true, "selector": {"text": {}}}}}
			},
			"update": {"install": {"fields": {"version": {}, "backup": {"selector": {"boolean": {}}}}}}
		}`),
		areas: []any{
			obj{"area_id": "bedroom", "name": "Bedroom", "aliases": []string{}},
			obj{"area_id": "kitchen", "name": "Kitchen \"main\"", "aliases": []string{"кухня"}},
		},
		devices: []any{obj{"id": "devk", "area_id": "kitchen"}},
		entities: []any{
			haReg("light.bedroom", obj{"area_id": "bedroom"}),
			haReg("light.kitchen", obj{"device_id": "devk"}),
			haReg("light.ceiling_bedroom", obj{"area_id": "bedroom"}),
			haReg("light.ceiling_kitchen", obj{"area_id": "kitchen"}),
			haReg("light.desk", nil),
			haReg("sensor.hall", obj{"area_id": "bedroom"}),
			haReg("light.wall_switch", obj{"area_id": "bedroom"}),
		},
		entries: map[string]any{
			"light.kitchen": obj{"entity_id": "light.kitchen", "aliases": []any{"big light"}},
			"light.desk":    obj{"entity_id": "light.desk", "aliases": []any{"reading lamp"}},
		},
	}
}

func (h haHouse) script(f *hatest.Server) {
	f.SetResult("get_states", h.states)
	f.SetResult("get_services", h.services)
	f.SetResult("config/area_registry/list", h.areas)
	f.SetResult("config/device_registry/list", h.devices)
	f.SetResult("config/entity_registry/list", h.entities)
	f.SetResult("config/entity_registry/get_entries", h.entries)
}

type haClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *haClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *haClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type usageCall struct{ device, entity string }

type usageRec struct {
	mu    sync.Mutex
	calls []usageCall
	// hold, when set, keeps Record until it closes or Record's ctx ends
	hold chan struct{}
	// bounded counts the Records whose ctx had a deadline
	bounded int
}

func (u *usageRec) Record(ctx context.Context, deviceID, entityID string) {
	u.mu.Lock()
	hold := u.hold
	u.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
		}
	}
	_, bounded := ctx.Deadline()
	u.mu.Lock()
	u.calls = append(u.calls, usageCall{deviceID, entityID})
	if bounded {
		u.bounded++
	}
	u.mu.Unlock()
}

// count waits for n records.
func (u *usageRec) count(t *testing.T, n int) []usageCall {
	t.Helper()
	waitUntil(t, "the usage records", func() bool { return len(u.got()) >= n })
	got := u.got()
	if len(got) != n {
		t.Fatalf("usage %v, want %d records", got, n)
	}
	return got
}

func (u *usageRec) got() []usageCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]usageCall(nil), u.calls...)
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type haRig struct {
	t      *testing.T
	fake   *hatest.Server
	home   *ha.Home
	clock  *haClock
	usage  *usageRec
	policy tools.HAPolicy
	// confirm is shared with cards in production; late receives its history
	// records without making the existing control tests inspect them
	confirm *tools.HAConfirmations
	late    chan tools.HAConfirmationRecord
	// logs is the home's and the client's log
	logs *syncBuffer

	mu        sync.Mutex
	area      string
	sensitive string
	// settingsErr fails every settings read
	settingsErr error

	// attrs are the house's attributes by entity id (guarded rigs)
	attrs map[string]any
}

// newHARig serves h from a fresh fake and waits for the home's first view.
func newHARig(t *testing.T, h haHouse) *haRig {
	t.Helper()
	return newHARigWithConfirm(t, h, tools.HAConfirmationOptions{})
}

// newHARigWithConfirm allows the lifecycle tests to shorten real waits while
// using the same live state stream and follower as production.
func newHARigWithConfirm(t *testing.T, h haHouse, options tools.HAConfirmationOptions) *haRig {
	t.Helper()
	f := hatest.New(haToken)
	t.Cleanup(f.Close)
	h.script(f)
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	c, err := ha.NewClient(ha.ClientConfig{BaseURL: f.URL(), Token: haToken, Roots: f.CA(), Log: log})
	if err != nil {
		t.Fatal(err)
	}
	clk := &haClock{t: time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)}
	r := &haRig{
		t: t, fake: f, home: ha.NewHome(c, ha.HomeOptions{Now: clk.Now, Log: log}), clock: clk, usage: &usageRec{}, logs: logs,
		late: make(chan tools.HAConfirmationRecord, 16),
	}
	if options.Record == nil {
		options.Record = func(_ context.Context, rec tools.HAConfirmationRecord) { r.late <- rec }
	}
	r.confirm = tools.NewHAConfirmations(r.home, options)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		if strings.Contains(logs.String(), haToken) {
			t.Errorf("a log line carries the token:\n%s", logs)
		}
	})
	t.Cleanup(r.confirm.Close)
	waitUntil(t, "the home loaded", func() bool { v, err := r.home.View(); return err == nil && v.Fresh })
	return r
}

func (r *haRig) setArea(area string) {
	r.mu.Lock()
	r.area = area
	r.mu.Unlock()
}

func (r *haRig) settings(context.Context, string) (settings.Values, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.settingsErr != nil {
		return settings.Values{}, r.settingsErr
	}
	return settings.Values{HomeAssistant: "home", HAArea: r.area, HASensitive: r.sensitive}, nil
}

func (r *haRig) set(f func(r *haRig)) {
	r.mu.Lock()
	f(r)
	r.mu.Unlock()
}

func (r *haRig) deps() tools.HADeps {
	return tools.HADeps{Home: r.home, Settings: r.settings, Policy: r.policy, Usage: r.usage, Confirmations: r.confirm}
}

// act runs tool in a wake session; once its call reached the fake, the fake
// emits then as state changes, as home assistant does after a service call.
func (r *haRig) act(tool agent.Tool, args string, then ...obj) (json.RawMessage, *agent.ToolError) {
	r.t.Helper()
	return r.actIn(cadencev2.Trigger_TRIGGER_WAKE, tool, args, then...)
}

// actIn is act in a session trig began.
func (r *haRig) actIn(trig cadencev2.Trigger, tool agent.Tool, args string, then ...obj) (json.RawMessage, *agent.ToolError) {
	r.t.Helper()
	before := len(r.fake.Calls())
	type result struct {
		out json.RawMessage
		err *agent.ToolError
	}
	done := make(chan result, 1)
	go func() {
		out, err := run(r.t, tool, trig, args)
		done <- result{out, err}
	}()
	if len(then) > 0 {
		waitUntil(r.t, "the service call", func() bool { return len(r.fake.Calls()) > before })
		for _, s := range then {
			r.fake.Emit("state_changed", obj{"entity_id": s["entity_id"], "old_state": nil, "new_state": s})
		}
	}
	res := <-done
	return res.out, res.err
}

// raw is act that must succeed, its result as the tool encoded it.
func (r *haRig) raw(tool agent.Tool, args string, then ...obj) string {
	r.t.Helper()
	out, e := r.act(tool, args, then...)
	if e != nil {
		r.t.Fatalf("%s: %+v", args, e)
	}
	return string(out)
}

// ok is raw, decoded.
func (r *haRig) ok(tool agent.Tool, args string, then ...obj) map[string]any {
	r.t.Helper()
	var out map[string]any
	if raw := r.raw(tool, args, then...); json.Unmarshal([]byte(raw), &out) != nil {
		r.t.Fatalf("result %s", raw)
	}
	return out
}

// refused is act that must fail with code and send nothing.
func (r *haRig) refused(tool agent.Tool, args, code string) *agent.ToolError {
	r.t.Helper()
	return r.refusedIn(cadencev2.Trigger_TRIGGER_WAKE, tool, args, code)
}

// refusedIn is refused in a session trig began.
func (r *haRig) refusedIn(trig cadencev2.Trigger, tool agent.Tool, args, code string) *agent.ToolError {
	r.t.Helper()
	before := len(r.fake.Calls())
	out, e := r.actIn(trig, tool, args)
	if e == nil || e.Code != code {
		r.t.Fatalf("%s: got %v %+v, want %s", args, out, e, code)
	}
	if strings.Contains(e.Message, hatest.Leak) || strings.Contains(e.Hint, hatest.Leak) {
		r.t.Fatalf("%s: upstream text in %+v", args, e)
	}
	if n := len(r.fake.Calls()); n != before {
		r.t.Fatalf("%s: %d calls sent on a refusal", args, n-before)
	}
	return e
}

// calls are the fake's service calls as domain.service entity data.
func (r *haRig) calls() []string {
	var out []string
	for _, c := range r.fake.Calls() {
		s := c.Domain + "." + c.Service
		if id, ok := c.Target["entity_id"]; ok {
			s += " " + id.(string)
		}
		if c.ServiceData != nil {
			b, _ := json.Marshal(c.ServiceData)
			s += " " + string(b)
		}
		out = append(out, s)
	}
	return out
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// confirmation waits past the production slow-confirm test's six seconds.
func (r *haRig) confirmation(t *testing.T, id string, phase tools.HAConfirmationPhase) tools.HAConfirmationSnapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		snap, _, ok := r.confirm.Watch(id)
		if ok && snap.Phase == phase {
			return snap
		}
		if time.Now().After(deadline) {
			t.Fatalf("confirmation %q never became %s (last %+v, found %v)", id, phase, snap, ok)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (r *haRig) lateRecord(t *testing.T) tools.HAConfirmationRecord {
	t.Helper()
	select {
	case rec := <-r.late:
		return rec
	case <-time.After(10 * time.Second):
		t.Fatal("no late history record")
		return tools.HAConfirmationRecord{}
	}
}
