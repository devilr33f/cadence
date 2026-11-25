package tools

import (
	"context"
	"encoding/json"
	"slices"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
)

// plain types only (R7), as alarm's: all false counts as absent, null is no value; which fields go with which action
// is Run's check
const timerInput = `{
	"type": "object",
	"properties": {
		"action": {"type": "string", "enum": ["set", "cancel", "list", "snooze"]},
		"seconds": {"type": "integer", "minimum": 1, "maximum": 86400},
		"minutes": {"type": "integer", "minimum": 1, "maximum": 60},
		"all": {"type": "boolean"},
		` + deviceProp + `
	},
	"required": ["action"],
	"additionalProperties": false
}`

var timerFields = map[string][]string{
	"set": {"seconds", "device"}, "cancel": {"seconds", "all", "device"}, "list": {"device"}, "snooze": {"minutes", "device"},
}

// Timer sets, cancels and lists the device's countdown timers (M6a-D7). A timer raises no volume, so it works in
// any microphone session; the device still checks the session.
func Timer(d AlarmDeps) agent.Tool {
	return agent.Tool{
		Name: "timer",
		Description: "Set, cancel or list countdown timers on the speaker; several can run at once. set takes " +
			"seconds (1-86400; 10 minutes is 600). cancel takes seconds (the timer set for that length), all, or " +
			"neither (the one that ends first). list gives each timer's length and the seconds left. Call it for " +
			"every timer request; nothing changes without it. " + snoozeHint("timer") + deviceHint +
			"For a time of day use the alarm tool.",
		Input:   timerInput,
		Effects: agent.Write, Output: agent.Trusted,
		Timeout: alarmsTimeout,
		Run: d.logged("timer_tool", func(ctx context.Context, c agent.Call) (string, json.RawMessage, *agent.ToolError) {
			ctx, c, e := d.target(ctx, c)
			if e != nil {
				return "", nil, e
			}
			st, ok := d.state(ctx, c.DeviceID)
			if !ok {
				return "", nil, noAlarms()
			}
			a, e := parseTimer(c.Args)
			if e != nil {
				return a.action, nil, e
			}
			out, e := d.timer(ctx, c, st, a)
			return a.action, out, e
		}),
	}
}

type timerArgs struct {
	action  string
	minutes int
	seconds int
	all     bool
	device  string
}

// parseTimer reads v1's arguments (§6.2): set needs a length, a cancel takes a length or all, never both
func parseTimer(raw json.RawMessage) (timerArgs, *agent.ToolError) {
	m, ok := fields(raw)
	if !ok {
		return timerArgs{}, badArgs("The arguments are not a JSON object.")
	}
	var a timerArgs
	var e *agent.ToolError
	if a.action, e = action(m, timerFields); e != nil {
		return a, e
	}
	if raw, ok := m["seconds"]; ok {
		if json.Unmarshal(raw, &a.seconds) != nil || a.seconds < 1 || a.seconds > 86400 {
			return a, badArgs("seconds is 1-86400.")
		}
	}
	if raw, ok := m["minutes"]; ok {
		if a.minutes, ok = parseMinutes(raw); !ok {
			return a, badArgs("minutes is 1-60.")
		}
	}
	if raw, ok := m["all"]; ok && json.Unmarshal(raw, &a.all) != nil {
		return a, badArgs("all is true or false.")
	}
	if raw, ok := m["device"]; ok && json.Unmarshal(raw, &a.device) != nil {
		return a, badArgs("device is a device's name or room.")
	}
	switch {
	case a.action == "set" && a.seconds == 0:
		return a, badArgs("set needs seconds.")
	case a.all && a.seconds != 0:
		return a, badArgs("Give seconds or all, not both.")
	}
	return a, nil
}

type setTimerArgs struct {
	SessionID string `json:"sessionId"`
	ID        string `json:"id"`
	Seconds   int    `json:"seconds"`
}

type cancelTimerArgs struct {
	SessionID string `json:"sessionId"`
	Seconds   int    `json:"seconds,omitempty"`
	All       bool   `json:"all,omitempty"`
}

// timerView is a timer as the model sees it: never its id
type timerView struct {
	Seconds    int `json:"seconds,omitempty"`
	RemainingS int `json:"remainingS,omitempty"`
}

func (d AlarmDeps) timer(ctx context.Context, c agent.Call, st alarmsState, a timerArgs) (json.RawMessage, *agent.ToolError) {
	now := d.now()
	if a.action == "list" {
		list := make([]timerView, 0, len(st.Timers))
		for _, t := range st.Timers {
			left, ok := remaining(t.EndsAt, now)
			if !ok {
				return nil, noAlarms()
			}
			list = append(list, timerView{Seconds: t.Seconds, RemainingS: left})
		}
		return encode(map[string][]timerView{"timers": list})
	}
	if a.action == "snooze" {
		return d.snooze(ctx, c, "timer", a.minutes)
	}
	// nothing the device could check: it would refuse it
	if c.SessionID == "" {
		return nil, agent.WakeRequired()
	}
	if a.action == "set" {
		id := newID(func(id string) bool {
			return slices.ContainsFunc(st.Timers, func(t timerEntry) bool { return t.ID == id })
		})
		res, err := d.command(ctx, c, "set_timer", setTimerArgs{SessionID: c.SessionID, ID: id, Seconds: a.seconds})
		if err != nil {
			return nil, alarmsError(err, "timer_failed", "timer")
		}
		var out struct {
			Timer *timerEntry `json:"timer"`
		}
		if json.Unmarshal(res, &out) != nil || out.Timer == nil || out.Timer.Seconds < 1 {
			return nil, failedAnswer("timer_failed")
		}
		left, ok := remaining(out.Timer.EndsAt, now)
		if !ok {
			return nil, failedAnswer("timer_failed")
		}
		return encode(map[string]timerView{"timer": {Seconds: out.Timer.Seconds, RemainingS: left}})
	}
	// with neither a length nor all, the device takes the one that ends first
	res, err := d.command(ctx, c, "cancel_timer", cancelTimerArgs{SessionID: c.SessionID, Seconds: a.seconds, All: a.all})
	if err != nil {
		return nil, alarmsError(err, "timer_failed", "timer")
	}
	ids, ok := cancelled(res)
	if !ok {
		return nil, failedAnswer("timer_failed")
	}
	gone := make([]timerView, 0, len(ids))
	for _, id := range ids {
		// one the server's copy does not hold yet is still counted, with what the request named
		v := timerView{Seconds: a.seconds}
		if i := slices.IndexFunc(st.Timers, func(t timerEntry) bool { return t.ID == id }); i >= 0 {
			v.Seconds = st.Timers[i].Seconds
		}
		gone = append(gone, v)
	}
	return encode(map[string][]timerView{"cancelled": gone})
}
