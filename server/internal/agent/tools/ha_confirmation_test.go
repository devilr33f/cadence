package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
)

func runIdentifiedHA(control agent.Tool, turnID, callID, args string) (json.RawMessage, *agent.ToolError) {
	return agent.Run(context.Background(), control, agent.Call{
		DeviceID: "d1", SessionID: "s1", TurnID: turnID, CallID: callID,
		Trigger: cadencev2.Trigger_TRIGGER_WAKE, Args: json.RawMessage(args),
	})
}

// the production regression: an infrared climate entity accepts turn_off now
// and reports off only six seconds later. The reply says pending at 2.5 s; the
// same operation confirms in the background and learns the entity's timing.
func TestHAControlSlowClimateReturnsPendingThenConfirms(t *testing.T) {
	r := newHARig(t, baseHouse())
	control := tools.HAControl(r.deps())
	args := `{"action":"turn_off","target":"living room"}`
	confirmed := time.AfterFunc(6*time.Second, func() {
		r.fake.Emit("state_changed", obj{"entity_id": "climate.living_room", "old_state": nil,
			"new_state": haState("climate.living_room", "off", obj{
				"friendly_name": "Living room", "temperature": 21, "current_temperature": 20.5, "hvac_action": "off",
			})})
	})
	defer confirmed.Stop()
	start := time.Now()
	raw, e := runIdentifiedHA(control, "turn-slow", "call-slow", args)
	if e != nil {
		t.Fatalf("slow but working command failed: %+v", e)
	}
	if d := time.Since(start); d < 2*time.Second || d > 4500*time.Millisecond {
		t.Fatalf("the foreground window was %v", d)
	}
	var out struct {
		Pending      bool   `json:"pending"`
		State        string `json:"state"`
		Expected     string `json:"expected"`
		Confirmation string `json:"confirmation"`
		Entity       struct {
			State string `json:"state"`
		} `json:"entity"`
	}
	if json.Unmarshal(raw, &out) != nil || !out.Pending || out.State != "heat" || out.Expected != "off" ||
		out.Entity.State != "heat" || out.Confirmation != "turn-slow:call-slow" {
		t.Fatalf("pending result %s", raw)
	}
	snap := r.confirmation(t, out.Confirmation, tools.HAConfirmationDone)
	if snap.State != "off" || snap.Expected != "off" {
		t.Fatalf("confirmed snapshot %+v", snap)
	}
	rec := r.lateRecord(t)
	var final struct {
		OK   bool `json:"ok"`
		Data struct {
			Pending bool   `json:"pending"`
			Late    bool   `json:"late"`
			State   string `json:"state"`
		} `json:"data"`
	}
	if json.Unmarshal(rec.Result, &final) != nil || !final.OK || final.Data.Pending || !final.Data.Late || final.Data.State != "off" {
		t.Fatalf("late success %s", rec.Result)
	}
	if rec.DeviceID != "d1" || rec.TurnID != "turn-slow" || rec.CallID != "call-slow" || string(rec.Args) != args {
		t.Fatalf("late record %+v", rec)
	}
	if got := r.calls(); len(got) != 1 || got[0] != "homeassistant.turn_off climate.living_room" {
		t.Fatalf("calls %v", got)
	}
	// six seconds plus the margin replaces the domain's 12 s starting point,
	// for this entity alone; the exact timing has a little scheduling slack
	if got := r.confirm.Budget("climate.living_room", "climate"); got < 7500*time.Millisecond || got >= 12*time.Second {
		t.Fatalf("adapted climate budget %v", got)
	}
	if got := r.confirm.Budget("climate.other", "climate"); got != 12*time.Second {
		t.Fatalf("another climate entity's budget %v", got)
	}
	if logs := r.logs.String(); strings.Contains(logs, "climate.living_room") || strings.Contains(logs, "Living room") {
		t.Fatalf("entity text reached the log: %s", logs)
	}
}

func TestHAControlFastLightAnswersConfirmed(t *testing.T) {
	r := newHARig(t, baseHouse())
	control := tools.HAControl(r.deps())
	start := time.Now()
	raw := r.raw(control, `{"action":"turn_on","target":"bedroom light"}`, haState("light.bedroom", "on", named("Bedroom light")))
	if d := time.Since(start); d > time.Second {
		t.Fatalf("fast light took %v", d)
	}
	if raw != `{"done":"turn_on","entity":{"id":"light.bedroom","name":"Bedroom light","state":"on"}}` {
		t.Fatalf("%s", raw)
	}
	select {
	case rec := <-r.late:
		t.Fatalf("a fast confirmation became a late record: %+v", rec)
	default:
	}
}

func TestHAConfirmationDomainBudgets(t *testing.T) {
	c := tools.NewHAConfirmations(nil, tools.HAConfirmationOptions{})
	defer c.Close()
	for _, domain := range []string{"light", "switch", "input_boolean"} {
		if got := c.Budget(domain+".x", domain); got != 2500*time.Millisecond {
			t.Fatalf("%s budget %v", domain, got)
		}
	}
	for _, domain := range []string{"climate", "water_heater", "cover", "fan", "media_player", "vacuum"} {
		if got := c.Budget(domain+".x", domain); got != 12*time.Second {
			t.Fatalf("%s budget %v", domain, got)
		}
	}
	if got := c.Budget("lock.front", "lock"); got != 5*time.Second {
		t.Fatalf("other domain budget %v", got)
	}
}

// an instant response must not teach a slow domain to use the old short
// failure window on a later command
func TestHAConfirmationSlowDomainKeepsItsLongFloor(t *testing.T) {
	r := newHARig(t, baseHouse())
	control := tools.HAControl(r.deps())
	r.raw(control, `{"action":"turn_off","target":"living room"}`, haState("climate.living_room", "off", named("Living room")))
	if got := r.confirm.Budget("climate.living_room", "climate"); got != 8*time.Second {
		t.Fatalf("learned fast climate budget %v", got)
	}
}

func TestHAConfirmationAdaptiveBudgetIsCapped(t *testing.T) {
	r := newHARigWithConfirm(t, baseHouse(), tools.HAConfirmationOptions{
		Short: 10 * time.Millisecond, Cap: 120 * time.Millisecond, Margin: time.Second,
	})
	control := tools.HAControl(r.deps())
	confirmed := time.AfterFunc(50*time.Millisecond, func() {
		r.fake.Emit("state_changed", obj{"entity_id": "climate.living_room", "old_state": nil,
			"new_state": haState("climate.living_room", "off", named("Living room"))})
	})
	defer confirmed.Stop()
	raw, e := runIdentifiedHA(control, "turn-cap", "call-cap", `{"action":"turn_off","target":"living room"}`)
	if e != nil {
		t.Fatalf("%+v", e)
	}
	var out struct {
		Pending      bool   `json:"pending"`
		Confirmation string `json:"confirmation"`
	}
	if json.Unmarshal(raw, &out) != nil || !out.Pending {
		t.Fatalf("%s", raw)
	}
	r.confirmation(t, out.Confirmation, tools.HAConfirmationDone)
	if got := r.confirm.Budget("climate.living_room", "climate"); got != 120*time.Millisecond {
		t.Fatalf("learned budget %v exceeds the cap", got)
	}
}
