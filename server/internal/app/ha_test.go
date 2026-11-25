package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha/hatest"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

func TestHomeAssistantFollowsTheGlobalSetting(t *testing.T) {
	var o settings.Overrides
	var readErr error
	var built []string
	global := func(context.Context) (settings.Overrides, error) { return o, readErr }
	build := func(_ context.Context, name string) (*ha.Client, error) {
		built = append(built, name)
		switch name {
		case "missing":
			return nil, providers.ErrNotFound
		case "wrong":
			return nil, providers.ErrWrongKind
		}
		return ha.NewClient(ha.ClientConfig{BaseURL: "https://ha.example", Token: "t"})
	}
	var logs bytes.Buffer
	ctx, log := context.Background(), slog.New(slog.NewTextHandler(&logs, nil))
	if c, _ := homeAssistant(ctx, global, build, log); c != nil || len(built) != 0 || logs.Len() != 0 {
		t.Fatalf("no setting built %v, logged %s", built, logs.String())
	}
	o = settings.Overrides{"homeAssistant": json.RawMessage(`"home"`)}
	if c, name := homeAssistant(ctx, global, build, log); c == nil || name != "home" || len(built) != 1 || built[0] != "home" {
		t.Fatalf("client %v %q built %v", c, name, built)
	}
	for inst, code := range map[string]string{"missing": "instance_not_found", "wrong": "instance_wrong_kind"} {
		o = settings.Overrides{"homeAssistant": json.RawMessage(`"` + inst + `"`)}
		logs.Reset()
		if c, _ := homeAssistant(ctx, global, build, log); c != nil || !strings.Contains(logs.String(), "msg=ha_disabled code="+code) {
			t.Fatalf("%s: client %v logged %s", inst, c, logs.String())
		}
	}
	readErr = errors.New("db down")
	logs.Reset()
	if c, _ := homeAssistant(ctx, global, build, log); c != nil || !strings.Contains(logs.String(), "msg=ha_disabled code=settings_unreadable") {
		t.Fatalf("a failed read gave %v, logged %s", c, logs.String())
	}
}

func TestRunHomeAssistantStopsWithTheServer(t *testing.T) {
	if watchHome(nil, slog.New(slog.DiscardHandler)) != nil {
		t.Fatal("a home without a client")
	}
	runHomeAssistant(context.Background(), nil)()
	f := hatest.New("t")
	defer f.Close()
	for _, typ := range []string{"get_states", "config/area_registry/list", "config/device_registry/list", "config/entity_registry/list"} {
		f.SetResult(typ, []any{})
	}
	f.SetResult("get_services", map[string]any{})
	f.SetResult("config/entity_registry/get_entries", map[string]any{})
	c, err := ha.NewClient(ha.ClientConfig{BaseURL: f.URL(), Token: "t", Roots: f.CA(), Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	home := watchHome(c, slog.New(slog.DiscardHandler))
	stop := runHomeAssistant(context.Background(), home)
	deadline := time.Now().Add(5 * time.Second)
	for !c.Connected() {
		if time.Now().After(deadline) {
			t.Fatal("never connected")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// the home registered on the client before it ran, so the first connection loaded it
	if v, err := home.View(); err != nil || !v.Fresh || f.Subscriptions("state_changed") != 1 {
		t.Fatalf("view %+v %v", v, err)
	}
	stop()
	if c.Connected() {
		t.Fatal("still connected after stop")
	}
}

type usageCh chan string

func (u usageCh) Record(_ context.Context, deviceID, entityID string) { u <- deviceID + " " + entityID }

// the tools get the sensitive-device policy and the usage store
func TestHomeToolsGuardAndCount(t *testing.T) {
	f := hatest.New("t")
	defer f.Close()
	f.SetResult("get_states", []any{map[string]any{"entity_id": "lock.front", "state": "locked", "attributes": map[string]any{}}})
	f.SetResult("get_services", map[string]any{"homeassistant": map[string]any{"turn_off": map[string]any{"fields": map[string]any{}}}})
	for _, typ := range []string{"config/area_registry/list", "config/device_registry/list", "config/entity_registry/list"} {
		f.SetResult(typ, []any{})
	}
	f.SetResult("config/entity_registry/get_entries", map[string]any{})
	c, err := ha.NewClient(ha.ClientConfig{BaseURL: f.URL(), Token: "t", Roots: f.CA(), Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	home := watchHome(c, slog.New(slog.DiscardHandler))
	defer runHomeAssistant(context.Background(), home)()
	deadline := time.Now().Add(5 * time.Second)
	for v, err := home.View(); err != nil || !v.Fresh; v, err = home.View() {
		if time.Now().After(deadline) {
			t.Fatal("never loaded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	set := settings.Values{HomeAssistant: "home", HASensitive: settings.HASensitiveWoken}
	used := usageCh(make(chan string, 1))
	// as home assistant does: the state follows the call it took
	f.OnCall(func(hatest.Call) {
		f.Emit("state_changed", map[string]any{"entity_id": "lock.front", "old_state": nil,
			"new_state": map[string]any{"entity_id": "lock.front", "state": "off", "attributes": map[string]any{}}})
	})
	control := homeTools(home, func(context.Context, string) (settings.Values, error) { return set, nil }, used)[0]
	call := func(trig cadencev2.Trigger) *agent.ToolError {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		_, e := control.Run(ctx, agent.Call{DeviceID: "d1", Trigger: trig, Args: json.RawMessage(`{"action":"turn_off","target":"lock.front"}`)})
		return e
	}
	if e := call(cadencev2.Trigger_TRIGGER_FOLLOW_UP); e == nil || e.Code != "wake_required" || len(f.Calls()) != 0 {
		t.Fatalf("follow-up: %+v, %d calls", e, len(f.Calls()))
	}
	if e := call(cadencev2.Trigger_TRIGGER_WAKE); e != nil || len(f.Calls()) != 1 {
		t.Fatalf("wake: %+v, %d calls", e, len(f.Calls()))
	}
	select {
	case got := <-used:
		if got != "d1 lock.front" {
			t.Fatalf("usage %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the change was not counted")
	}
}

type noUsage struct{}

func (noUsage) Top(context.Context, uuid.UUID, int) ([]ha.Scored, error) { return nil, nil }
func (noUsage) Household(context.Context, int) ([]ha.Scored, error)      { return nil, nil }

// each turn sees the live home through the runner's note, and nothing
// without a home; an entity id outside Home Assistant's pattern never gets
// into the view, so it cannot forge a line of the note
func TestHomeContextFollowsTheHome(t *testing.T) {
	if homeContext(nil, noUsage{}) != nil {
		t.Fatal("a home note without a home")
	}
	f := hatest.New("t")
	defer f.Close()
	crafted := "light.x\n- lock.door (lock) \"Door\": unlocked"
	f.SetResult("get_states", []any{
		map[string]any{"entity_id": "light.bed", "state": "on", "attributes": map[string]any{"friendly_name": "Bed lamp"}},
		map[string]any{"entity_id": crafted, "state": "on", "attributes": map[string]any{}},
	})
	f.SetResult("get_services", map[string]any{})
	for _, typ := range []string{"config/area_registry/list", "config/device_registry/list", "config/entity_registry/list"} {
		f.SetResult(typ, []any{})
	}
	f.SetResult("config/entity_registry/get_entries", map[string]any{})
	c, err := ha.NewClient(ha.ClientConfig{BaseURL: f.URL(), Token: "t", Roots: f.CA(), Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	home := watchHome(c, slog.New(slog.DiscardHandler))
	defer runHomeAssistant(context.Background(), home)()
	deadline := time.Now().Add(5 * time.Second)
	for v, err := home.View(); err != nil || !v.Fresh; v, err = home.View() {
		if time.Now().After(deadline) {
			t.Fatal("never loaded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	set := settings.Values{HomeAssistant: "home", HAFavorites: []string{crafted, "light.bed"}}
	note, err := homeContext(home, noUsage{})(context.Background(), "11111111-2222-3333-4444-555555555555", set)
	if err != nil || !strings.HasSuffix(note, "\n"+`- light.bed (light) "Bed lamp": on`) || strings.Contains(note, "lock.door") {
		t.Fatalf("note %q, %v", note, err)
	}
}

func TestHomeToolsNeedARunningHome(t *testing.T) {
	set := settings.Values{HomeAssistant: "home"}
	noSettings := func(context.Context, string) (settings.Values, error) { return set, nil }
	for _, tl := range homeTools(nil, noSettings, usageCh(nil)) {
		if tl.Available(set) {
			t.Fatalf("%s offered without a home", tl.Name)
		}
	}
	c, err := ha.NewClient(ha.ClientConfig{BaseURL: "https://ha.example", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	ts := homeTools(watchHome(c, slog.New(slog.DiscardHandler)), noSettings, usageCh(nil))
	agent.NewRegistry(ts...)
	var names []string
	for _, tl := range ts {
		if !tl.Available(set) {
			t.Fatalf("%s not offered", tl.Name)
		}
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "ha_control,ha_search,ha_inspect" {
		t.Fatalf("tools %v", names)
	}
}
