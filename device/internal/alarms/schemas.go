package alarms

// the capability's JSON Schemas (draft 2020-12); each command checks its args itself as strictly
const (
	sessionIDSchema = `{"type":"string","minLength":1,"maxLength":63}`
	idSchema        = `{"type":"string","pattern":"^[a-z0-9]{1,16}$"}`
	timeSchema      = `{"type":"string","pattern":"^([01][0-9]|2[0-3]):[0-5][0-9]$"}`
	daysSchema      = `{"type":"array","items":{"enum":["mon","tue","wed","thu","fri","sat","sun"]},"minItems":1,"maxItems":7,` +
		`"uniqueItems":true}`
	dateSchema    = `{"type":"string","pattern":"^[0-9]{4}-[0-9]{2}-[0-9]{2}$"}`
	levelSchema   = `{"type":"integer","minimum":1,"maximum":100,"default":50}`
	secondsSchema = `{"type":"integer","minimum":1,"maximum":86400}`

	rampSecondsSchema = `{"type":"integer","minimum":0,"maximum":1800}`
	rampFromSchema    = `{"enum":["current","quiet"]}`
	limitSchema       = `{"type":"integer","minimum":60,"maximum":3600}`
	kindSchema        = `{"enum":["alarm","timer","reminder"]}`
	labelSchema       = `{"type":"string","minLength":1,"maxLength":80}`
	optionalLevel     = `{"type":"integer","minimum":1,"maximum":100}`
	instantSchema     = `{"type":"string","format":"date-time"}`

	configSchema = `{"type":"object","properties":{"timeZone":{"type":"string","minLength":1,"maxLength":64},` +
		`"rampSeconds":` + rampSecondsSchema + `,"rampFrom":` + rampFromSchema + `,"alarmLimitSeconds":` + limitSchema +
		`,"timerLimitSeconds":{"type":"integer","minimum":10,"maximum":600}` +
		`,"reminderLimitSeconds":{"type":"integer","minimum":10,"maximum":300}` +
		`,"snoozeMinutes":{"type":"integer","minimum":1,"maximum":60},"autoSnooze":{"type":"integer","minimum":0,"maximum":5}},` +
		`"required":["timeZone"],"additionalProperties":false}`
	optionsProps = `,"rampSeconds":` + rampSecondsSchema + `,"rampFrom":` + rampFromSchema + `,"limitSeconds":` + limitSchema

	alarmSchema = `{"type":"object","properties":{"id":` + idSchema + `,"time":` + timeSchema + `,"days":` + daysSchema +
		`,"date":` + dateSchema + `,"level":` + optionalLevel + optionsProps + `,"soundId":` + idSchema + `,"next":` +
		instantSchema + `},"required":["id","time","level"],"additionalProperties":false}`
	reminderSchema = `{"type":"object","properties":{"id":` + idSchema + `,"time":` + timeSchema + `,"days":` + daysSchema +
		`,"date":` + dateSchema + `,"at":` + instantSchema + `,"label":` + labelSchema + `,"soundId":` + idSchema +
		`,"level":` + optionalLevel + `,"next":` + instantSchema + `},"required":["id","label"],"additionalProperties":false}`
	timerSchema = `{"type":"object","properties":{"id":` + idSchema + `,"seconds":` + secondsSchema +
		`,"endsAt":` + instantSchema + `,"next":` + instantSchema + `},"required":["id","seconds","endsAt"],` +
		`"additionalProperties":false}`
	snoozeSchema = `{"type":"object","properties":{"kind":` + kindSchema + `,"id":` + idSchema + `,"until":` + instantSchema +
		`},"required":["kind","id","until"],"additionalProperties":false}`
	lastRingSchema = `{"oneOf":[{"type":"null"},{"type":"object","properties":{"kind":` + kindSchema + `,"id":` + idSchema +
		`,"at":` + instantSchema + `,"stoppedBy":{"enum":["button","volume","wake","word","timeout","snooze","remote"]}},` +
		`"required":["kind","id","at","stoppedBy"],"additionalProperties":false}]}`
	stateSchema = `{"type":"object","properties":{"alarms":{"type":"array","maxItems":10,"items":` + alarmSchema + `},` +
		`"timers":{"type":"array","maxItems":5,"items":` + timerSchema + `},` +
		`"reminders":{"type":"array","maxItems":20,"items":` + reminderSchema + `},` +
		`"ringing":{"oneOf":[{"type":"null"},{"type":"object","properties":{"kind":` + kindSchema + `,"id":` + idSchema +
		`},"required":["kind","id"],"additionalProperties":false}]},` +
		`"snoozed":{"type":"array","maxItems":35,"items":` + snoozeSchema + `},"lastRing":` + lastRingSchema + `},` +
		`"required":["alarms","timers","ringing"],"additionalProperties":false}`

	setAlarmSchema = `{"type":"object","properties":{"sessionId":` + sessionIDSchema + `,"id":` + idSchema + `,"time":` +
		timeSchema + `,"days":` + daysSchema + `,"date":` + dateSchema + `,"level":` + levelSchema + optionsProps +
		`,"soundId":` + idSchema + `},` +
		`"required":["sessionId","id","time"],"not":{"required":["days","date"]},"additionalProperties":false}`
	// a reminder without a level needs no session (owner, 2026-11-16); with one it is admitted as an alarm
	setReminderSchema = `{"type":"object","properties":{"sessionId":` + sessionIDSchema + `,"id":` + idSchema +
		`,"time":` + timeSchema + `,"days":` + daysSchema + `,"date":` + dateSchema +
		`,"inSeconds":{"type":"integer","minimum":1,"maximum":604800},"label":` + labelSchema + `,"soundId":` + idSchema +
		`,"level":` + optionalLevel + `},"required":["id","label"],` +
		`"oneOf":[{"required":["time"],"not":{"required":["days","date"]}},{"required":["inSeconds"],` +
		`"not":{"anyOf":[{"required":["time"]},{"required":["days"]},{"required":["date"]}]}}],"additionalProperties":false}`
	cancelReminderSchema = `{"type":"object","properties":{"sessionId":` + sessionIDSchema + `,"id":` + idSchema +
		`,"all":{"const":true}},"oneOf":[{"required":["id"]},{"required":["all"]}],"additionalProperties":false}`
	cancelAlarmSchema = `{"type":"object","properties":{"sessionId":` + sessionIDSchema + `,"id":` + idSchema + `,"time":` +
		timeSchema + `,"all":{"const":true}},"required":["sessionId"],` +
		`"oneOf":[{"required":["id"]},{"required":["time"]},{"required":["all"]}],"additionalProperties":false}`
	// a timer may be set and cancelled without a session: by the server's authenticated API (owner, 2026-11-16)
	setTimerSchema = `{"type":"object","properties":{"sessionId":` + sessionIDSchema + `,"id":` + idSchema + `,"seconds":` +
		secondsSchema + `},"required":["id","seconds"],"additionalProperties":false}`
	cancelTimerSchema = `{"type":"object","properties":{"sessionId":` + sessionIDSchema + `,"id":` + idSchema +
		`,"seconds":` + secondsSchema + `,"all":{"const":true}},` +
		`"not":{"anyOf":[{"required":["id","seconds"]},{"required":["id","all"]},{"required":["seconds","all"]}]},` +
		`"additionalProperties":false}`
	// snooze and stop_ring end or put off a ring set in a woken session: no session needed (owner, 2026-11-16)
	snoozeCommandSchema = `{"type":"object","properties":{"sessionId":` + sessionIDSchema + `,"kind":` + kindSchema +
		`,"id":` + idSchema + `,"minutes":{"type":"integer","minimum":1,"maximum":60}},"additionalProperties":false}`
	stopRingSchema = `{"type":"object","properties":{"sessionId":` + sessionIDSchema + `,"kind":` + kindSchema +
		`,"id":` + idSchema + `},"additionalProperties":false}`
)

func commandSchemas() map[string]string {
	return map[string]string{
		"set_alarm": setAlarmSchema, "cancel_alarm": cancelAlarmSchema,
		"set_timer": setTimerSchema, "cancel_timer": cancelTimerSchema,
		"snooze": snoozeCommandSchema, "stop_ring": stopRingSchema,
		"set_reminder": setReminderSchema, "cancel_reminder": cancelReminderSchema,
	}
}
