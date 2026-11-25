package settings

import (
	"bytes"
	"encoding/json"
)

// AlarmsSetting is how the device's alarms, timers and reminders ring: each key
// unset is the device's default (a 30 s ramp from the level in force, 600 s
// for an alarm, 60 s for a timer and a reminder, 9 snooze minutes, no
// auto-snooze). The server pushes it with timeZone as the alarms config.
type AlarmsSetting struct {
	// RampSeconds is how long an alarm takes to climb to its level, 0-1800.
	RampSeconds *int `json:"rampSeconds,omitempty" minimum:"0" maximum:"1800"`
	// RampFrom is where the ramp starts: "current" (the level in force) or
	// "quiet" (level 5, or lower when the level in force is).
	RampFrom *string `json:"rampFrom,omitempty" enum:"current,quiet"`
	// AlarmLimitSeconds is how long an alarm rings unstopped, 60-3600.
	AlarmLimitSeconds *int `json:"alarmLimitSeconds,omitempty" minimum:"60" maximum:"3600"`
	// TimerLimitSeconds is how long a timer rings unstopped, 10-600.
	TimerLimitSeconds *int `json:"timerLimitSeconds,omitempty" minimum:"10" maximum:"600"`
	// ReminderLimitSeconds is how long a reminder rings unstopped, 10-300.
	ReminderLimitSeconds *int `json:"reminderLimitSeconds,omitempty" minimum:"10" maximum:"300"`
	// SnoozeMinutes is a snooze's length when none is asked for, 1-60.
	SnoozeMinutes *int `json:"snoozeMinutes,omitempty" minimum:"1" maximum:"60"`
	// AutoSnooze is how many times an alarm nobody stopped rings again
	// SnoozeMinutes later, 0-5.
	AutoSnooze *int `json:"autoSnooze,omitempty" minimum:"0" maximum:"5"`
}

// alarmBounds are each integer key's bounds, as the device's config schema
// has them.
var alarmBounds = map[string][2]int{
	"rampSeconds":          {0, 1800},
	"alarmLimitSeconds":    {60, 3600},
	"timerLimitSeconds":    {10, 600},
	"reminderLimitSeconds": {10, 300},
	"snoozeMinutes":        {1, 60},
	"autoSnooze":           {0, 5},
}

// validAlarms checks an alarms value: an object of known keys, none null,
// each within its bounds.
func validAlarms(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	for _, v := range fields {
		if isNull(v) {
			return false
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var a AlarmsSetting
	if dec.Decode(&a) != nil {
		return false
	}
	ints := map[string]*int{
		"rampSeconds": a.RampSeconds, "alarmLimitSeconds": a.AlarmLimitSeconds, "timerLimitSeconds": a.TimerLimitSeconds,
		"reminderLimitSeconds": a.ReminderLimitSeconds, "snoozeMinutes": a.SnoozeMinutes, "autoSnooze": a.AutoSnooze,
	}
	for k, v := range ints {
		if b := alarmBounds[k]; v != nil && (*v < b[0] || *v > b[1]) {
			return false
		}
	}
	return a.RampFrom == nil || *a.RampFrom == "current" || *a.RampFrom == "quiet"
}
