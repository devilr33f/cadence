package tools_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
)

// the direct path settles what the model's own tool would settle the same way — one match, called and counted — and
// answers a reason, never a call, for everything it must leave to the model
func TestDirectHomeControl(t *testing.T) {
	r := newHARig(t, baseHouse())
	r.policy = tools.HASensitivePolicy(r.settings)
	d := r.deps()
	call := agent.Call{DeviceID: "dev1", SessionID: "s1", Trigger: cadencev2.Trigger_TRIGGER_WAKE}

	if name, reason := tools.DirectHomeControl(context.Background(), d, call, true, "kitchen light", ""); reason != "" || name != "Kitchen light" {
		t.Fatalf("on: %q %q", name, reason)
	}
	if name, reason := tools.DirectHomeControl(context.Background(), d, call, false, "light.bedroom", ""); reason != "" || name != "Bedroom light" {
		t.Fatalf("off: %q %q", name, reason)
	}
	if got := r.calls(); len(got) != 2 ||
		got[0] != "homeassistant.turn_on light.kitchen" || got[1] != "homeassistant.turn_off light.bedroom" {
		t.Fatalf("calls %v", got)
	}
	if got := r.usage.count(t, 2); got[0].entity != "light.kitchen" || got[1].entity != "light.bedroom" {
		t.Fatalf("usage %v", got)
	}

	// a follow-up may turn a light as the model's tool may
	follow := call
	follow.Trigger = cadencev2.Trigger_TRIGGER_FOLLOW_UP
	if _, reason := tools.DirectHomeControl(context.Background(), d, follow, true, "hallway", ""); reason != "" {
		t.Fatalf("follow-up: %q", reason)
	}

	for name, tc := range map[string]struct {
		target, reason string
		on             bool
	}{
		"no match":  {"the garage door", "not_found", true},
		"two match": {"ceiling", "ambiguous", true},
	} {
		before := len(r.calls())
		if _, reason := tools.DirectHomeControl(context.Background(), d, call, tc.on, tc.target, ""); reason != tc.reason {
			t.Errorf("%s: %q, want %q", name, reason, tc.reason)
		}
		if len(r.calls()) != before {
			t.Errorf("%s: a call went out", name)
		}
	}

	if _, reason := tools.DirectHomeControl(context.Background(), tools.HADeps{}, call, true, "kitchen light", ""); reason != "unavailable" {
		t.Fatalf("no home: %q", reason)
	}
}

// the spoken light word takes every light of the speaker's room together — its area by the setting, else
// the area named as the device's own room — and never asks which one; what it cannot settle stays the model's
func TestDirectRoomLights(t *testing.T) {
	house := baseHouse()
	// a switch the home classes as a light joins the room's lights
	house.states = append(house.states, haState("switch.corner", "off", obj{"friendly_name": "Corner", "device_class": "light"}))
	house.entities = append(house.entities, haReg("switch.corner", obj{"area_id": "bedroom"}))
	r := newHARig(t, house)
	r.policy = tools.HASensitivePolicy(r.settings)
	d := r.deps()
	call := agent.Call{DeviceID: "dev1", SessionID: "s1", Trigger: cadencev2.Trigger_TRIGGER_WAKE}

	t.Run("on, by the setting's area", func(t *testing.T) {
		r.setArea("bedroom")
		before := len(r.calls())
		if name, reason := tools.DirectHomeControl(context.Background(), d, call, true, "свет", ""); reason != "" || name != "свет" {
			t.Fatalf("on: %q %q", name, reason)
		}
		got := r.calls()[before:]
		slices.Sort(got)
		want := []string{
			"homeassistant.turn_on light.bedroom", "homeassistant.turn_on light.ceiling_bedroom",
			"homeassistant.turn_on light.wall_switch", "homeassistant.turn_on switch.corner",
		}
		if !slices.Equal(got, want) {
			t.Fatalf("calls %v, want %v", got, want)
		}
		if n := len(r.usage.count(t, 4)); n != 4 {
			t.Fatalf("usage %d", n)
		}
	})

	t.Run("off, by the room's name", func(t *testing.T) {
		r.setArea("")
		before := len(r.calls())
		if name, reason := tools.DirectHomeControl(context.Background(), d, call, false, "the light", "Bedroom"); reason != "" || name != "the light" {
			t.Fatalf("off: %q %q", name, reason)
		}
		got := r.calls()[before:]
		if len(got) != 4 {
			t.Fatalf("calls %v", got)
		}
		for _, c := range got {
			if !strings.HasPrefix(c, "homeassistant.turn_off ") {
				t.Fatalf("call %q", c)
			}
		}
	})

	t.Run("an area's alias names the room", func(t *testing.T) {
		r.setArea("")
		before := len(r.calls())
		if _, reason := tools.DirectHomeControl(context.Background(), d, call, true, "лампочки", "кухня"); reason != "" {
			t.Fatalf("alias: %q", reason)
		}
		got := r.calls()[before:]
		slices.Sort(got)
		want := []string{"homeassistant.turn_on light.ceiling_kitchen", "homeassistant.turn_on light.kitchen"}
		if !slices.Equal(got, want) {
			t.Fatalf("calls %v, want %v", got, want)
		}
	})

	t.Run("no room, no setting: the model's", func(t *testing.T) {
		r.setArea("")
		before := len(r.calls())
		if _, reason := tools.DirectHomeControl(context.Background(), d, call, true, "свет", ""); reason != "no_area" {
			t.Fatalf("no area: %q", reason)
		}
		if len(r.calls()) != before {
			t.Fatal("a call went out")
		}
	})

	t.Run("an area with no light is the model's", func(t *testing.T) {
		r.setArea("attic")
		if _, reason := tools.DirectHomeControl(context.Background(), d, call, true, "свет", ""); reason != "not_found" {
			t.Fatalf("empty: %q", reason)
		}
	})

	t.Run("an unavailable bulb is skipped, not asked about", func(t *testing.T) {
		r.setArea("bedroom")
		before := len(r.calls())
		s := haState("light.bedroom", "unavailable", named("Bedroom light"))
		r.fake.Emit("state_changed", obj{"entity_id": "light.bedroom", "old_state": nil, "new_state": s})
		waitUntil(t, "the state landed", func() bool {
			v, err := r.home.View()
			return err == nil && v.Fresh && stateOf(v, "light.bedroom") == "unavailable"
		})
		if _, reason := tools.DirectHomeControl(context.Background(), d, call, true, "свет", ""); reason != "" {
			t.Fatalf("skip: %q", reason)
		}
		got := r.calls()[before:]
		slices.Sort(got)
		want := []string{
			"homeassistant.turn_on light.ceiling_bedroom", "homeassistant.turn_on light.wall_switch",
			"homeassistant.turn_on switch.corner",
		}
		if !slices.Equal(got, want) {
			t.Fatalf("calls %v, want %v", got, want)
		}
	})
}

func stateOf(v ha.View, id string) string {
	for _, e := range v.Entities {
		if e.ID == id {
			return e.State
		}
	}
	return ""
}
