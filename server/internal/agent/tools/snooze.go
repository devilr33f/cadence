package tools

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
)

// snoozeHint tells the model what a request for more time after a ring is: a snooze, never a new or cancelled one
func snoozeHint(noun string) string {
	return "snooze puts off the " + noun + " that rings now, or the one stopped in the last 10 minutes, by minutes " +
		"(1-60; none is the speaker's own default) and rings it again then. If the user asks for more time after a " +
		"ring («ещё 5 минут», «snooze», «разбуди через 10 минут»), call this tool with action snooze and minutes. " +
		"Never cancel or set a " + noun + " again to do this: a repeating alarm's schedule stays. "
}

type snoozeArgs struct {
	SessionID string `json:"sessionId,omitempty"`
	Kind      string `json:"kind"`
	Minutes   int    `json:"minutes,omitempty"`
}

// snoozeView is a snooze as the model sees it: never its id or a wall time it would read in the wrong zone
type snoozeView struct {
	Kind    string `json:"kind"`
	Minutes int    `json:"minutes"`
}

func parseMinutes(raw json.RawMessage) (int, bool) {
	var minutes int
	if json.Unmarshal(raw, &minutes) != nil || minutes < 1 || minutes > 60 {
		return 0, false
	}
	return minutes, true
}

// snooze puts off the ring of kind that sounds, or the one stopped in the last 10 minutes: the device finds it. No
// woken session is needed (owner 2026-11-16): it only puts off a ring set in one
func (d AlarmDeps) snooze(ctx context.Context, c agent.Call, kind string, minutes int) (json.RawMessage, *agent.ToolError) {
	res, err := d.command(ctx, c, "snooze", snoozeArgs{SessionID: c.SessionID, Kind: kind, Minutes: minutes})
	if err != nil {
		e := alarmsError(err, kind+"_failed", kind)
		if e.Code == "not_found" {
			e.Message = "No " + kind + " is ringing or stopped ringing in the last 10 minutes."
		}
		return nil, e
	}
	var out struct {
		Snoozed *struct {
			Until string `json:"until"`
		} `json:"snoozed"`
	}
	if json.Unmarshal(res, &out) != nil || out.Snoozed == nil {
		return nil, failedAnswer(kind + "_failed")
	}
	until, err := time.Parse(time.RFC3339, out.Snoozed.Until)
	if err != nil {
		return nil, failedAnswer(kind + "_failed")
	}
	left := int(max(1, math.Round(until.Sub(d.now()).Minutes())))
	return encode(map[string]snoozeView{"snoozed": {Kind: kind, Minutes: left}})
}
