package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/ringsounds"
)

// plain types only (R7): a provider may refuse a whole tool list over one union, and this tool is offered every turn.
// false and [] count as absent, as models echo the empty values of fields they do not mean; null is no value here.
// which fields go with which action is Run's check, in words the model can act on
const alarmInput = `{
	"type": "object",
	"properties": {
		"action": {"type": "string", "enum": ["set", "cancel", "list", "snooze"]},
		"time": {"type": "string", "pattern": "^(?:[01]?\\d|2[0-3]):[0-5]\\d$"},
		"days": {"type": "array", "maxItems": 7, "items": {"type": "string",
			"enum": ["mon", "tue", "wed", "thu", "fri", "sat", "sun", "daily", "weekdays", "weekends"]}},
		"date": {"type": "string", "pattern": "^\\d{4}-\\d{2}-\\d{2}$"},
		"level": {"type": "integer", "minimum": 1, "maximum": 100},
		"rampSeconds": {"type": "integer", "minimum": 0, "maximum": 1800},
		"rampFrom": {"type": "string", "enum": ["current", "quiet"]},
		"minutes": {"type": "integer", "minimum": 1, "maximum": 60},
		"sound": {"type": "string", "minLength": 1, "maxLength": 200},
		"all": {"type": "boolean"},
		` + deviceProp + `
	},
	"required": ["action"],
	"additionalProperties": false
}`

var (
	alarmFields = map[string][]string{
		"set": {"time", "days", "date", "level", "rampSeconds", "rampFrom", "sound", "device"}, "cancel": {"time", "all", "device"},
		"list": {"device"}, "snooze": {"minutes", "device"},
	}
	clockTime  = regexp.MustCompile(`^(?:[01]?\d|2[0-3]):[0-5]\d$`)
	isoDate    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	weekdays   = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	dayPresets = map[string][]string{"daily": weekdays, "weekdays": weekdays[:5], "weekends": weekdays[5:]}
)

// Alarm sets, cancels, lists and snoozes the device's alarms (M6a-D7). Setting one needs a session the user began with
// the wake word or a press (rule 7: it raises the volume when it rings); cancel and list do not, and a snooze needs no
// session at all (owner 2026-11-16: it only puts off a ring set in a woken one). The device keeps the alarms and
// checks the session again itself.
func Alarm(d AlarmDeps) agent.Tool {
	return agent.Tool{
		Name: "alarm",
		Description: "Set, cancel or list the speaker's alarms: times of day it rings at, once or on repeat. " +
			"set takes time (H:MM, 24-hour clock), days for a repeating alarm (a list of mon..sun; daily, " +
			"weekdays and weekends stand for their days), or date (YYYY-MM-DD, worked out from the local date) for a " +
			"one-time alarm on another day than the time's next one, and level (1-100) only when the user names a volume. " +
			"rampSeconds (how long it takes to grow to its level) and rampFrom (quiet: from a whisper) only when the " +
			"user asks for a gentler or longer wake-up, e.g. «плавно, минут за пять» is rampSeconds 300. " +
			"sound only when the user wants to wake up to music: a music_search result id (r1) or a search query " +
			"(«разбуди под Queen» is sound \"Queen\"); the alarm then plays it, and its tone when the server cannot be " +
			"reached. If the result says music false, the alarm is set with its tone; tell the user. " +
			"An alarm at the same time and days replaces the old one. cancel takes time (every alarm at that time), " +
			"all, or neither (the alarms at the only time any is set; with several times, ask which). list reads " +
			"them. " + snoozeHint("alarm") + deviceHint + "Call it for every " +
			"alarm request; nothing changes without it. For a countdown use the timer tool. If it fails with " +
			"wake_required, tell the user to wake you first.",
		Input:   alarmInput,
		Effects: agent.Write, Output: agent.Trusted,
		Timeout: alarmsTimeout,
		Run: d.logged("alarm_tool", func(ctx context.Context, c agent.Call) (string, json.RawMessage, *agent.ToolError) {
			ctx, c, e := d.target(ctx, c)
			if e != nil {
				return "", nil, e
			}
			st, ok := d.state(ctx, c.DeviceID)
			if !ok {
				return "", nil, noAlarms()
			}
			a, e := parseAlarm(c.Args)
			if e != nil {
				return a.action, nil, e
			}
			out, e := d.alarm(ctx, c, st, a)
			return a.action, out, e
		}),
	}
}

type alarmArgs struct {
	action string
	// a set's ramp, nil when absent
	rampSeconds *int
	rampFrom    string
	// a snooze's minutes, 0 for the device's default
	minutes int
	// HH:MM, "" when absent
	time  string
	days  []string
	date  string
	level *int
	all   bool
	// a set's music: a result id or a query, "" for the tone
	sound string
	// device is the device argument, "" for here
	device string
}

// parseAlarm reads v1's arguments (§6.1): which fields go with each action, the presets, a time as HH:MM
func parseAlarm(raw json.RawMessage) (alarmArgs, *agent.ToolError) {
	m, ok := fields(raw)
	if !ok {
		return alarmArgs{}, badArgs("The arguments are not a JSON object.")
	}
	var a alarmArgs
	var e *agent.ToolError
	if a.action, e = action(m, alarmFields); e != nil {
		return a, e
	}
	if raw, ok := m["time"]; ok {
		if a.time, ok = parseClock(raw); !ok {
			return a, badArgs("time is H:MM on a 24-hour clock.")
		}
	}
	if raw, ok := m["days"]; ok {
		if a.days, ok = parseDays(raw); !ok {
			return a, badArgs("days is a list of mon..sun, or daily, weekdays or weekends.")
		}
	}
	if raw, ok := m["date"]; ok {
		if json.Unmarshal(raw, &a.date) != nil || !isoDate.MatchString(a.date) {
			return a, badArgs("date is YYYY-MM-DD.")
		}
		if _, err := time.Parse(time.DateOnly, a.date); err != nil {
			return a, badArgs("date is not a day of the calendar.")
		}
	}
	if raw, ok := m["level"]; ok {
		var level int
		if json.Unmarshal(raw, &level) != nil || level < 1 || level > 100 {
			return a, badArgs("level is 1-100.")
		}
		a.level = &level
	}
	if raw, ok := m["rampSeconds"]; ok {
		var seconds int
		if json.Unmarshal(raw, &seconds) != nil || seconds < 0 || seconds > 1800 {
			return a, badArgs("rampSeconds is 0-1800.")
		}
		a.rampSeconds = &seconds
	}
	if raw, ok := m["rampFrom"]; ok {
		if json.Unmarshal(raw, &a.rampFrom) != nil || a.rampFrom != "current" && a.rampFrom != "quiet" {
			return a, badArgs("rampFrom is current or quiet.")
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
	if raw, ok := m["sound"]; ok {
		if json.Unmarshal(raw, &a.sound) != nil || strings.TrimSpace(a.sound) == "" || len(a.sound) > 200 {
			return a, badArgs("sound is a music_search result id or a query.")
		}
		a.sound = strings.TrimSpace(a.sound)
	}
	if raw, ok := m["device"]; ok && json.Unmarshal(raw, &a.device) != nil {
		return a, badArgs("device is a device's name or room.")
	}
	switch {
	case a.action == "set" && a.time == "":
		return a, badArgs("set needs time.")
	case a.date != "" && len(a.days) > 0:
		return a, badArgs("Give days for a repeating alarm or date for a one-time one, not both.")
	case a.all && a.time != "":
		return a, badArgs("Give time or all, not both.")
	}
	return a, nil
}

// parseClock reads H:MM or HH:MM: the minutes are the last two digits, the hour what comes before the colon
func parseClock(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) != nil || !clockTime.MatchString(s) {
		return "", false
	}
	h, _ := strconv.Atoi(s[:len(s)-3])
	return fmt.Sprintf("%02d:%s", h, s[len(s)-2:]), true
}

// parseDays resolves a list of weekdays and presets, each preset its days, sorted mon..sun and each once
func parseDays(raw json.RawMessage) ([]string, bool) {
	var list []string
	if json.Unmarshal(raw, &list) != nil || len(list) > len(weekdays) {
		return nil, false
	}
	var named []string
	for _, day := range list {
		switch preset, ok := dayPresets[day]; {
		case ok:
			named = append(named, preset...)
		case slices.Contains(weekdays, day):
			named = append(named, day)
		default:
			return nil, false
		}
	}
	var out []string
	for _, w := range weekdays {
		if slices.Contains(named, w) {
			out = append(out, w)
		}
	}
	return out, true
}

type setAlarmArgs struct {
	SessionID string   `json:"sessionId"`
	ID        string   `json:"id"`
	Time      string   `json:"time"`
	Days      []string `json:"days,omitempty"`
	Date      string   `json:"date,omitempty"`
	Level     *int     `json:"level,omitempty"`
	// the alarm's own ramp, absent for the setting's
	RampSeconds *int   `json:"rampSeconds,omitempty"`
	RampFrom    string `json:"rampFrom,omitempty"`
	// the ring sound of its music
	SoundID string `json:"soundId,omitempty"`
}

type cancelAlarmArgs struct {
	SessionID string `json:"sessionId"`
	ID        string `json:"id,omitempty"`
	Time      string `json:"time,omitempty"`
	All       bool   `json:"all,omitempty"`
}

// alarmView is an alarm as the model sees it: never its id. sound is its music's title
type alarmView struct {
	Time  string   `json:"time,omitempty"`
	Days  []string `json:"days,omitempty"`
	Date  string   `json:"date,omitempty"`
	Level int      `json:"level,omitempty"`
	Sound string   `json:"sound,omitempty"`
}

func viewOf(a alarmEntry) alarmView {
	return alarmView{Time: a.Time, Days: a.Days, Date: a.Date, Level: a.Level}
}

// soundTitles are the titles of the alarms' music, by sound id
func (d AlarmDeps) soundTitles(ctx context.Context, deviceID string, list []alarmEntry) map[string]string {
	out := map[string]string{}
	var ids []string
	for _, a := range list {
		if a.SoundID != "" {
			ids = append(ids, a.SoundID)
		}
	}
	if d.Sounds == nil || len(ids) == 0 {
		return out
	}
	for id, info := range d.Sounds.Info(ctx, deviceID, ids) {
		out[id] = info.Title
	}
	return out
}

func (d AlarmDeps) alarm(ctx context.Context, c agent.Call, st alarmsState, a alarmArgs) (json.RawMessage, *agent.ToolError) {
	if a.action == "list" {
		titles := d.soundTitles(ctx, c.DeviceID, st.Alarms)
		list := make([]alarmView, 0, len(st.Alarms))
		for _, al := range st.Alarms {
			v := viewOf(al)
			v.Sound = titles[al.SoundID]
			list = append(list, v)
		}
		return encode(map[string][]alarmView{"alarms": list})
	}
	if a.action == "snooze" {
		return d.snooze(ctx, c, "alarm", a.minutes)
	}
	// rule 7: nothing is sent outside a woken session; the device says no to the rest itself
	if (a.action == "set" && !agent.Woken(c.Trigger)) || c.SessionID == "" {
		return nil, agent.WakeRequired()
	}
	if a.action == "set" {
		return d.setAlarm(ctx, c, st, a)
	}
	return d.cancelAlarm(ctx, c, st, a)
}

func (d AlarmDeps) setAlarm(ctx context.Context, c agent.Call, st alarmsState, a alarmArgs) (json.RawMessage, *agent.ToolError) {
	id := newID(func(id string) bool {
		return slices.ContainsFunc(st.Alarms, func(al alarmEntry) bool { return al.ID == id })
	})
	// the music first: an alarm whose music could not be made still rings, with its tone
	var music *ringsounds.Sound
	if a.sound != "" && d.Music != nil {
		if s, err := d.Music(ctx, c.DeviceID, a.sound); err == nil {
			music = &s
		} else if ctx.Err() != nil {
			return nil, &agent.ToolError{Code: "timeout", Message: "Setting the alarm took too long."}
		}
	}
	args := setAlarmArgs{
		SessionID: c.SessionID, ID: id, Time: a.time, Days: a.days, Date: a.date, Level: a.level,
		RampSeconds: a.rampSeconds, RampFrom: a.rampFrom,
	}
	if music != nil {
		args.SoundID = music.ID
	}
	// the same time, days and date replaces the alarm that has them: the device's twin rule says so in replaced
	res, err := d.command(ctx, c, "set_alarm", args)
	if err != nil {
		return nil, alarmsError(err, "alarm_failed", "alarm")
	}
	var out struct {
		Alarm    *alarmEntry `json:"alarm"`
		Replaced bool        `json:"replaced"`
	}
	if json.Unmarshal(res, &out) != nil || out.Alarm == nil || out.Alarm.Time == "" {
		return nil, failedAnswer("alarm_failed")
	}
	view := viewOf(*out.Alarm)
	if a.sound == "" {
		return encode(struct {
			Alarm    alarmView `json:"alarm"`
			Replaced bool      `json:"replaced"`
		}{view, out.Replaced})
	}
	if music != nil {
		view.Sound = music.Title
	}
	return encode(struct {
		Alarm    alarmView `json:"alarm"`
		Replaced bool      `json:"replaced"`
		Music    bool      `json:"music"`
	}{view, out.Replaced, music != nil})
}

// cancelAlarm removes every alarm at a time or all of them; with neither, the only one, else it asks which
func (d AlarmDeps) cancelAlarm(ctx context.Context, c agent.Call, st alarmsState, a alarmArgs) (json.RawMessage, *agent.ToolError) {
	args := cancelAlarmArgs{SessionID: c.SessionID, Time: a.time, All: a.all}
	if a.time == "" && !a.all {
		switch len(st.Alarms) {
		case 0:
			return nil, &agent.ToolError{Code: "not_found", Message: "No alarm is set."}
		case 1:
			args.ID = st.Alarms[0].ID
		default:
			var times []string
			for _, al := range st.Alarms {
				if !slices.Contains(times, al.Time) {
					times = append(times, al.Time)
				}
			}
			// all at one time leave nothing to ask: that time is what the user means
			if len(times) == 1 {
				args.Time, a.time = times[0], times[0]
				break
			}
			return nil, &agent.ToolError{
				Code: "ambiguous", Message: "Several alarms are set: " + strings.Join(times, ", ") + ".",
				Hint: "Ask the user which time to cancel, then call again with that time.", Candidates: times,
			}
		}
	}
	res, err := d.command(ctx, c, "cancel_alarm", args)
	if err != nil {
		return nil, alarmsError(err, "alarm_failed", "alarm")
	}
	ids, ok := cancelled(res)
	if !ok {
		return nil, failedAnswer("alarm_failed")
	}
	gone := make([]alarmView, 0, len(ids))
	for _, id := range ids {
		// one the server's copy does not hold yet is still counted, with what the request named
		v := alarmView{Time: a.time}
		if i := slices.IndexFunc(st.Alarms, func(al alarmEntry) bool { return al.ID == id }); i >= 0 {
			v = viewOf(st.Alarms[i])
		}
		v.Level = 0
		gone = append(gone, v)
	}
	return encode(map[string][]alarmView{"cancelled": gone})
}
