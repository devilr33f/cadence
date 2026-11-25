package cards_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha/hatest"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

const haToken = "cards-ha-token-never-printed"

type obj = map[string]any

func haState(id, st string, attrs obj) obj {
	return obj{"entity_id": id, "state": st, "attributes": attrs, "context": obj{"id": "c"}}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type usage struct {
	mu   sync.Mutex
	recs []string
}

func (u *usage) Record(_ context.Context, dev, entity string) {
	u.mu.Lock()
	u.recs = append(u.recs, dev+"|"+entity)
	u.mu.Unlock()
}

type haRig struct {
	t         *testing.T
	fake      *hatest.Server
	home      *ha.Home
	svc       *cards.Service
	deps      cards.HADeps
	logs      *syncBuf
	usage     *usage
	mu        sync.Mutex
	sensitive string
	records   []cards.Record
	// then maps a service to the states the fake emits after it
	then map[string][]obj
}

func newHARig(t *testing.T) *haRig {
	t.Helper()
	f := hatest.New(haToken)
	t.Cleanup(f.Close)
	f.SetResult("get_states", []any{
		haState("light.kitchen", "on", obj{"friendly_name": "Kitchen light", "brightness": 128}),
		haState("lock.front", "locked", obj{"friendly_name": "Front door"}),
		haState("sensor.temp", "21.5", obj{"friendly_name": "Temperature", "unit_of_measurement": "°C"}),
		haState("climate.living", "heat", obj{"friendly_name": "Living room", "temperature": 21,
			"current_temperature": 20.5, "min_temp": 15, "max_temp": 28}),
		haState("climate.range", "heat_cool", obj{"friendly_name": "Bedroom", "current_temperature": 21,
			"target_temp_low": 20, "target_temp_high": 24}),
		haState("water_heater.kettle", "off", obj{"friendly_name": "Kettle", "temperature": 100,
			"current_temperature": 65, "supported_features": 1, "operation_list": []string{"off", "on"},
			"min_temp": 40, "max_temp": 100}),
		haState("cover.garage", "closed", obj{"friendly_name": "Garage", "device_class": "garage"}),
		haState("switch.plug", "off", obj{"friendly_name": "Plug"}),
		haState("media_player.kitchen_speaker", "on", obj{"friendly_name": "Kitchen speaker"}),
		haState("light.bedroom", "off", obj{"friendly_name": "Bedroom Chandelier"}),
		haState("media_player.tv", "playing", obj{"friendly_name": "Living room TV"}),
		haState("binary_sensor.door", "on", obj{"friendly_name": "Balcony door", "device_class": "door"}),
		haState("sensor.humidity", "48.5", obj{"friendly_name": "Bathroom humidity", "unit_of_measurement": "%", "device_class": "humidity"}),
	})
	f.SetResult("get_services", json.RawMessage(`{"homeassistant":{"turn_on":{"fields":{}},"turn_off":{"fields":{}}},
		"light":{"turn_on":{"fields":{}}},"lock":{"lock":{"fields":{}},"unlock":{"fields":{}}},
		"climate":{"set_temperature":{"fields":{}},"turn_off":{"fields":{}}},"cover":{"open_cover":{"fields":{}},"close_cover":{"fields":{}}}}`))
	f.SetResult("config/area_registry/list", []any{})
	f.SetResult("config/device_registry/list", []any{})
	reg := func(id, platform string) obj {
		return obj{"entity_id": id, "name": nil, "original_name": nil, "area_id": nil, "device_id": nil, "disabled_by": nil, "platform": platform}
	}
	f.SetResult("config/entity_registry/list", []any{
		reg("light.kitchen", "hue"), reg("lock.front", "zwave"), reg("sensor.temp", "zha"), reg("climate.living", "zha"),
		reg("climate.range", "zha"), reg("water_heater.kettle", "miio"), reg("cover.garage", "zha"),
		reg("switch.plug", "zha"), reg("media_player.kitchen_speaker", "cadence"),
		reg("light.bedroom", "hue"), reg("media_player.tv", "androidtv"), reg("binary_sensor.door", "zha"),
		reg("sensor.humidity", "zha"),
	})
	f.SetResult("config/entity_registry/get_entries", obj{})
	logs := &syncBuf{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	c, err := ha.NewClient(ha.ClientConfig{BaseURL: f.URL(), Token: haToken, Roots: f.CA(), Log: log})
	if err != nil {
		t.Fatal(err)
	}
	r := &haRig{t: t, fake: f, home: ha.NewHome(c, ha.HomeOptions{Log: log}), logs: logs, usage: &usage{}, then: map[string][]obj{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		// rule 8: no entity id, name or state reaches a line
		for _, leak := range []string{haToken, "light.kitchen", "Kitchen light", "lock.front", "Front door"} {
			if strings.Contains(logs.String(), leak) {
				t.Errorf("a log line carries %q:\n%s", leak, logs)
			}
		}
	})
	f.OnCall(func(call hatest.Call) {
		r.mu.Lock()
		then := r.then[call.Domain+"."+call.Service]
		r.mu.Unlock()
		for _, s := range then {
			f.Emit("state_changed", obj{"entity_id": s["entity_id"], "old_state": nil, "new_state": s})
		}
	})
	waitUntil(t, "the home loaded", func() bool { v, err := r.home.View(); return err == nil && v.Fresh })
	policy := tools.HASensitivePolicy(func(context.Context, string) (settings.Values, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		return settings.Values{HomeAssistant: "home", HASensitive: r.sensitive}, nil
	})
	r.deps = cards.HADeps{Home: r.home, Policy: policy, Usage: r.usage}
	r.svc = cards.New(cards.Deps{
		Lang: func(context.Context, string) string { return "ru" }, Log: log,
		Record: func(_ context.Context, _ string, rec cards.Record) {
			r.mu.Lock()
			r.records = append(r.records, rec)
			r.mu.Unlock()
		},
	})
	return r
}

func (r *haRig) after(service string, states ...obj) {
	r.mu.Lock()
	r.then[service] = states
	r.mu.Unlock()
}

func (r *haRig) act(card *cardsv1.Card, actionID string, value any) calls.Reply {
	r.t.Helper()
	args := obj{"cardId": card.GetId(), "revision": card.GetRevision(), "actionId": actionID}
	if value != nil {
		args["value"] = value
	}
	b, _ := json.Marshal(args)
	return r.svc.ActionMethod()(context.Background(), calls.Call{DeviceID: "screen-1", ID: "r1", Method: rpc.CardAction, Args: b})
}

// find answers the first node of kind under the card's root
func find[T any](c *cardsv1.Card) *T {
	var walk func(n *cardsv1.Node) *T
	walk = func(n *cardsv1.Node) *T {
		var kids []*cardsv1.Node
		switch k := n.GetKind().(type) {
		case *cardsv1.Node_Card:
			kids = k.Card.GetChildren()
			for _, b := range k.Card.GetActions() {
				kids = append(kids, pcards.N(b))
			}
		case *cardsv1.Node_PagedList:
			kids = k.PagedList.GetItems()
		case *cardsv1.Node_Toggle:
			if v, ok := any(k.Toggle).(*T); ok {
				return v
			}
		case *cardsv1.Node_Slider:
			if v, ok := any(k.Slider).(*T); ok {
				return v
			}
		case *cardsv1.Node_Button:
			if v, ok := any(k.Button).(*T); ok {
				return v
			}
		case *cardsv1.Node_State:
			if v, ok := any(k.State).(*T); ok {
				return v
			}
		case *cardsv1.Node_Value:
			if v, ok := any(k.Value).(*T); ok {
				return v
			}
		}
		for _, kid := range kids {
			if v := walk(kid); v != nil {
				return v
			}
		}
		return nil
	}
	return walk(c.GetRoot())
}

func TestHACardToggleRoundTrip(t *testing.T) {
	r := newHARig(t)
	ctx := context.Background()
	c, err := r.svc.Show(ctx, "screen-1", r.deps.Entity("light.kitchen"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.GetId(), "kitchen") || c.GetRevision() != 1 {
		t.Fatalf("card id %q rev %d: the id must be opaque", c.GetId(), c.GetRevision())
	}
	tg := find[cardsv1.Toggle](c)
	if tg == nil || !tg.GetOn() || tg.GetAction().GetConfirm() != "" {
		t.Fatalf("toggle %v", tg)
	}
	sl := find[cardsv1.Slider](c)
	if sl == nil || sl.GetValue() != 50 {
		t.Fatalf("brightness slider %v", sl)
	}
	r.after("homeassistant.turn_off", haState("light.kitchen", "off", obj{"friendly_name": "Kitchen light"}))
	rep := r.act(c, tg.GetAction().GetId(), nil)
	if rep.Status != rpc.StatusOK || resultCard(rep) == nil {
		t.Fatalf("reply %+v", rep)
	}
	next := resultCard(rep)
	if next.GetId() != c.GetId() || next.GetRevision() != 2 || find[cardsv1.Toggle](next).GetOn() {
		t.Fatalf("next revision %v", next)
	}
	if find[cardsv1.Slider](next) != nil {
		t.Fatal("a light that is off shows no brightness")
	}
	calls := r.fake.Calls()
	last := calls[len(calls)-1]
	if last.Domain != "homeassistant" || last.Service != "turn_off" || last.Target["entity_id"] == nil {
		t.Fatalf("call %+v", last)
	}
	r.mu.Lock()
	recs := r.records
	r.mu.Unlock()
	if len(recs) != 1 || recs[0].Tool != "ha_control" {
		t.Fatalf("records %+v", recs)
	}
	if len(r.usage.recs) != 1 || r.usage.recs[0] != "screen-1|light.kitchen" {
		t.Fatalf("usage %v", r.usage.recs)
	}
	// the action was single use, and its revision is gone
	if again := r.act(c, tg.GetAction().GetId(), nil); again.Status != rpc.StatusExpired {
		t.Fatalf("a used action ran again: %+v", again)
	}
	// another device holds no such action
	b, _ := json.Marshal(obj{"cardId": next.GetId(), "revision": 2, "actionId": find[cardsv1.Toggle](next).GetAction().GetId()})
	if rep := r.svc.ActionMethod()(ctx, calls2("screen-2", b)); rep.Status != rpc.StatusNotFound {
		t.Fatalf("another device ran it: %+v", rep)
	}
	// a stale revision is expired
	b, _ = json.Marshal(obj{"cardId": next.GetId(), "revision": 1, "actionId": find[cardsv1.Toggle](next).GetAction().GetId()})
	if rep := r.svc.ActionMethod()(ctx, calls2("screen-1", b)); rep.Status != rpc.StatusExpired {
		t.Fatalf("stale revision: %+v", rep)
	}
}

func calls2(dev string, args []byte) calls.Call {
	return calls.Call{DeviceID: dev, ID: "r2", Method: rpc.CardAction, Args: args}
}

func TestHACardSliderChecksAndCalls(t *testing.T) {
	r := newHARig(t)
	c, err := r.svc.Show(context.Background(), "screen-1", r.deps.Entity("light.kitchen"))
	if err != nil {
		t.Fatal(err)
	}
	sl := find[cardsv1.Slider](c)
	if rep := r.act(c, sl.GetAction().GetId(), 150); rep.Status != rpc.StatusInvalid {
		t.Fatalf("out of range: %+v", rep)
	}
	if rep := r.act(c, sl.GetAction().GetId(), "x"); rep.Status != rpc.StatusInvalid {
		t.Fatalf("not a number: %+v", rep)
	}
	r.after("light.turn_on", haState("light.kitchen", "on", obj{"friendly_name": "Kitchen light", "brightness": 76}))
	rep := r.act(c, sl.GetAction().GetId(), 30)
	if rep.Status != rpc.StatusOK {
		t.Fatalf("reply %+v", rep)
	}
	calls := r.fake.Calls()
	last := calls[len(calls)-1]
	if last.Domain != "light" || last.Service != "turn_on" || last.ServiceData["brightness_pct"] != float64(30) {
		t.Fatalf("call %+v", last)
	}
	if got := find[cardsv1.Slider](resultCard(rep)).GetValue(); got != 30 {
		t.Fatalf("next slider %v", got)
	}
}

func TestHACardSensitiveConfirmsAndFollowsTheSetting(t *testing.T) {
	r := newHARig(t)
	c, err := r.svc.Show(context.Background(), "screen-1", r.deps.Entity("lock.front"))
	if err != nil {
		t.Fatal(err)
	}
	b := find[cardsv1.Button](c)
	if b == nil || b.GetAction().GetConfirm() == "" {
		t.Fatalf("a lock's button must confirm: %v", b)
	}
	r.mu.Lock()
	r.sensitive = settings.HASensitiveDeny
	r.mu.Unlock()
	before := len(r.fake.Calls())
	rep := r.act(c, b.GetAction().GetId(), nil)
	ans, _ := rep.JSON.(cards.ActionAnswer)
	if rep.Status != rpc.StatusRefused || ans.Code != "not_supported" || ans.Toast == "" || resultCard(rep) == nil {
		t.Fatalf("deny: %+v", rep)
	}
	if len(r.fake.Calls()) != before {
		t.Fatal("a refused action reached home assistant")
	}
	// the refusal drew the card again with fresh actions: allowed now, the
	// press counts as a pressed session
	r.mu.Lock()
	r.sensitive = ""
	r.mu.Unlock()
	r.after("lock.unlock", haState("lock.front", "unlocked", obj{"friendly_name": "Front door"}))
	next := resultCard(rep)
	rep = r.act(next, find[cardsv1.Button](next).GetAction().GetId(), nil)
	if rep.Status != rpc.StatusOK || find[cardsv1.State](resultCard(rep)).GetKind() != cardsv1.StateKind_STATE_KIND_UNLOCKED {
		t.Fatalf("unlock: %+v", rep)
	}
	calls := r.fake.Calls()
	if last := calls[len(calls)-1]; last.Domain != "lock" || last.Service != "unlock" {
		t.Fatalf("call %+v", last)
	}
}

func TestHACardNeverControlsCadenceOwn(t *testing.T) {
	r := newHARig(t)
	c, err := r.svc.Show(context.Background(), "screen-1", r.deps.Entity("media_player.kitchen_speaker"))
	if err != nil {
		t.Fatal(err)
	}
	if find[cardsv1.Toggle](c) != nil || find[cardsv1.Slider](c) != nil || find[cardsv1.Button](c) != nil {
		t.Fatalf("a cadence entity got a control: %v", c)
	}
}

func TestHACardsOfEveryDomainValidate(t *testing.T) {
	r := newHARig(t)
	for _, id := range []string{"light.kitchen", "lock.front", "sensor.temp", "climate.living", "climate.range",
		"water_heater.kettle", "cover.garage", "switch.plug", "light.gone"} {
		c, err := r.svc.Show(context.Background(), "screen-1", r.deps.Entity(id))
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if err := pcards.ValidateCard(c); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	c, _ := r.svc.Show(context.Background(), "screen-1", r.deps.Entity("cover.garage"))
	if b := find[cardsv1.Button](c); b == nil || b.GetAction().GetConfirm() == "" {
		t.Fatalf("a garage door must confirm: %v", b)
	}
}

// the owner's kettle: the water it holds now, its power and mode in home
// assistant's words, and the target on the kettle's own 40..100 scale
func TestHAWaterHeaterCard(t *testing.T) {
	r := newHARig(t)
	c, err := r.svc.Show(context.Background(), "screen-1", r.deps.Entity("water_heater.kettle"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.GetRoot().GetCard().GetIcon(); got != "kettle" {
		t.Fatalf("icon %q, want the heating flame", got)
	}
	cur := find[cardsv1.Value](c)
	if cur == nil || cur.GetValue() != "65" || cur.GetUnit() != "°C" || cur.GetLabel() != "Сейчас" {
		t.Fatalf("current temperature %v", cur)
	}
	st := find[cardsv1.State](c)
	if st == nil || st.GetLabel() != "Выключен" || st.GetKind() != cardsv1.StateKind_STATE_KIND_OFF {
		t.Fatalf("power and mode %v", st)
	}
	sl := find[cardsv1.Slider](c)
	if sl == nil || sl.GetMin() != 40 || sl.GetMax() != 100 || sl.GetValue() != 100 || sl.GetStep() != 1 ||
		sl.GetLabel() != "Нагревать до" || sl.GetValueLabel() != "100 °C" {
		t.Fatalf("target slider %v", sl)
	}
	// heating in a named mode: the word carries it, the tone says on
	r.fake.Emit("state_changed", obj{"entity_id": "water_heater.kettle", "old_state": nil, "new_state": haState(
		"water_heater.kettle", "eco", obj{"friendly_name": "Kettle", "temperature": 100, "current_temperature": 70,
			"supported_features": 1, "operation_list": []string{"off", "eco", "on"}, "min_temp": 40, "max_temp": 100})})
	waitUntil(t, "the kettle heats", func() bool {
		next, err := r.svc.Show(context.Background(), "screen-1", r.deps.Entity("water_heater.kettle"))
		return err == nil && find[cardsv1.State](next).GetLabel() == "Нагрев (эко)"
	})
	next, _ := r.svc.Show(context.Background(), "screen-1", r.deps.Entity("water_heater.kettle"))
	if got := find[cardsv1.State](next).GetKind(); got != cardsv1.StateKind_STATE_KIND_ON {
		t.Fatalf("a heating kettle reads on: %v", got)
	}
	if got := next.GetRoot().GetCard().GetTone(); got != cardsv1.Tone_TONE_SUCCESS {
		t.Fatalf("a heating kettle's tone %v", got)
	}
	// the slider's action sets the target on the kettle's own domain
	r.after("water_heater.set_temperature", haState("water_heater.kettle", "eco", obj{"friendly_name": "Kettle",
		"temperature": 80, "current_temperature": 70, "supported_features": 1,
		"operation_list": []string{"off", "eco", "on"}, "min_temp": 40, "max_temp": 100}))
	rep := r.act(next, find[cardsv1.Slider](next).GetAction().GetId(), 80)
	if rep.Status != rpc.StatusOK {
		t.Fatalf("reply %+v", rep)
	}
	calls := r.fake.Calls()
	if last := calls[len(calls)-1]; last.Domain != "water_heater" || last.Service != "set_temperature" ||
		last.ServiceData["temperature"] != float64(80) {
		t.Fatalf("call %+v", last)
	}
	if got := find[cardsv1.Slider](resultCard(rep)).GetValue(); got != 80 {
		t.Fatalf("next slider %v", got)
	}
}

// a range climate keeps a window, not one number: nothing on the card sets it
func TestHAClimateRangeCard(t *testing.T) {
	r := newHARig(t)
	c, err := r.svc.Show(context.Background(), "screen-1", r.deps.Entity("climate.range"))
	if err != nil {
		t.Fatal(err)
	}
	if find[cardsv1.Slider](c) != nil {
		t.Fatal("a range climate got a single-number slider")
	}
	var values []*cardsv1.Value
	var walk func(n *cardsv1.Node)
	walk = func(n *cardsv1.Node) {
		switch k := n.GetKind().(type) {
		case *cardsv1.Node_Card:
			for _, kid := range k.Card.GetChildren() {
				walk(kid)
			}
		case *cardsv1.Node_Row:
			for _, kid := range k.Row.GetChildren() {
				walk(kid)
			}
		case *cardsv1.Node_Column:
			for _, kid := range k.Column.GetChildren() {
				walk(kid)
			}
		case *cardsv1.Node_Value:
			values = append(values, k.Value)
		}
	}
	walk(c.GetRoot())
	// the current temperature and the window, in that order
	want := []struct{ value, label string }{{"21", "Сейчас"}, {"20–24", "Поддерживать"}}
	if len(values) != len(want) {
		t.Fatalf("values %v", values)
	}
	for i, w := range want {
		if values[i].GetValue() != w.value || values[i].GetLabel() != w.label {
			t.Fatalf("value %d: %v, want %v", i, values[i], w)
		}
	}
}

// a card on screen gets a new revision when its entity changes elsewhere
func TestHACardLiveRevisions(t *testing.T) {
	r := newHARig(t)
	var mu sync.Mutex
	var sent []*cadencev2.Envelope
	router := calls.New(func(_ string, env *cadencev2.Envelope) error {
		mu.Lock()
		sent = append(sent, env)
		mu.Unlock()
		return nil
	}, slog.New(slog.DiscardHandler))
	router.Register(rpc.CardsWatch, r.svc.WatchMethod())
	t.Cleanup(router.Close)
	c, err := r.svc.Show(context.Background(), "screen-1", r.deps.Entity("switch.plug"))
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(obj{"cardIds": []string{c.GetId()}})
	router.Handle("screen-1", &cadencev2.Envelope{Body: &cadencev2.Envelope_RpcRequest{RpcRequest: &cadencev2.RpcRequest{
		Id: "w1", Method: rpc.CardsWatch, Json: string(args), Subscribe: true,
	}}})
	waitUntil(t, "the watch answer", func() bool { mu.Lock(); defer mu.Unlock(); return len(sent) == 1 })
	r.fake.Emit("state_changed", obj{"entity_id": "switch.plug", "old_state": nil,
		"new_state": haState("switch.plug", "on", obj{"friendly_name": "Plug"})})
	var got *cadencev2.Cards
	waitUntil(t, "a live revision", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, e := range sent {
			if cs := e.GetCards(); cs != nil {
				got = cs
				return true
			}
		}
		return false
	})
	if got.GetContextId() != "w1" || len(got.GetCards()) != 1 || got.GetCards()[0].GetRevision() != 2 ||
		!find[cardsv1.Toggle](got.GetCards()[0]).GetOn() {
		t.Fatalf("live revision %v", got)
	}
	router.Handle("screen-1", &cadencev2.Envelope{Body: &cadencev2.Envelope_RpcCancel{RpcCancel: &cadencev2.RpcCancel{Id: "w1"}}})
	waitUntil(t, "the last event", func() bool {
		mu.Lock()
		defer mu.Unlock()
		e := sent[len(sent)-1].GetRpcEvent()
		return e != nil && e.GetLast()
	})
}

// resultCard is the card a card action answered: its next revision, or the one a refusal rolls back to
func resultCard(rep calls.Reply) *cardsv1.Card {
	if c := rep.CardAction.GetCard(); c != nil {
		return c
	}
	return rep.CardAction.GetRefusal().GetCard()
}

// each domain's card in ru, as the screen draws it (pkg/cards/testdata/cards/ha_*.json; make screen-sim's
// cards script shows them); its random action ids written as fixed ones
func TestHACardsMatchTheirExamples(t *testing.T) {
	r := newHARig(t)
	for _, id := range []string{"light.kitchen", "light.bedroom", "switch.plug", "climate.living", "climate.range",
		"water_heater.kettle", "cover.garage", "lock.front", "sensor.temp", "sensor.humidity", "binary_sensor.door",
		"media_player.tv"} {
		c, err := r.svc.Show(context.Background(), "screen-1", r.deps.Entity(id))
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		n := 0
		fixActions(c.ProtoReflect(), &n)
		golden(t, "ha_"+strings.ReplaceAll(id, ".", "_"), c)
	}
}

func fixActions(m protoreflect.Message, n *int) {
	if a, ok := m.Interface().(*cardsv1.Action); ok {
		*n++
		a.Id = "a:" + strconv.Itoa(*n)
		return
	}
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsList() && fd.Kind() == protoreflect.MessageKind:
			for i := range v.List().Len() {
				fixActions(v.List().Get(i).Message(), n)
			}
		case !fd.IsMap() && fd.Kind() == protoreflect.MessageKind:
			fixActions(v.Message(), n)
		}
		return true
	})
}
