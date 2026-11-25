package alarms

import (
	"context"
	"encoding/json"
	"errors"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

const (
	defaultLevel = 50
	maxSessionID = 63
)

var (
	errInvalid      = &capability.CommandError{Code: "invalid"}
	errNotFound     = &capability.CommandError{Code: "not_found"}
	errFollowUp     = &capability.CommandError{Code: "follow_up"}
	errWakeRequired = &capability.CommandError{Code: "wake_required"}
	errUnavailable  = &capability.CommandError{Code: "unavailable"}
)

// sessionless are the commands that may come without a session: snooze and stop_ring only end or put off a ring set
// in a woken session, so a follow-up, a screen's press, the dashboard and Home Assistant may send them; timers and
// reminders without a level never touch the volume, so the server's authenticated API (Home Assistant, a screen's
// press) may set and cancel them too (owner, 2026-11-16). one without a session is the server's own caller; one with
// a session is checked as before. a reminder with a level is an alarm's case: it needs a woken session
var sessionless = map[string]bool{
	"snooze": true, "stop_ring": true, "set_timer": true, "cancel_timer": true, "set_reminder": true, "cancel_reminder": true,
}

// unchecked are the sessionless commands whose session, when one is named, is not checked either
var unchecked = map[string]bool{"snooze": true, "stop_ring": true}

// admit is rule 7 for a command of session: setting an alarm raises the volume when it rings, so it needs a session the
// owner began with the wake word or a press, and a follow-up, which hears whoever talks after a reply, is told so; the
// other commands go in any session the device began. one it does not know, or the server's own, never acts
func admit(name string, trigger cadencev2.Trigger, known bool) error {
	woken := trigger == cadencev2.Trigger_TRIGGER_WAKE || trigger == cadencev2.Trigger_TRIGGER_BUTTON
	switch {
	case known && woken:
		return nil
	case known && trigger == cadencev2.Trigger_TRIGGER_FOLLOW_UP:
		if name == "set_alarm" {
			return errFollowUp
		}
		return nil
	default:
		return errWakeRequired
	}
}

// command runs one of the four on the book once its args and its session pass
func (p *provider) command(ctx context.Context, name string, raw json.RawMessage) (json.RawMessage, error) {
	keys := map[string][]string{
		"set_alarm": {"sessionId", "id", "time", "days", "date", "level", "rampSeconds", "rampFrom", "limitSeconds",
			"soundId"},
		"cancel_alarm":    {"sessionId", "id", "time", "all"},
		"set_timer":       {"sessionId", "id", "seconds"},
		"cancel_timer":    {"sessionId", "id", "seconds", "all"},
		"snooze":          {"sessionId", "kind", "id", "minutes"},
		"stop_ring":       {"sessionId", "kind", "id"},
		"set_reminder":    {"sessionId", "id", "time", "days", "date", "inSeconds", "label", "soundId", "level"},
		"cancel_reminder": {"sessionId", "id", "all"},
	}[name]
	if keys == nil {
		return nil, capability.ErrUnsupported
	}
	m, ok := object(raw, keys...)
	if !ok {
		return nil, errInvalid
	}
	var session string
	hasSession, okSession := value(m, "sessionId", &session)
	if !okSession || (hasSession || !sessionless[name]) && (session == "" || len(session) > maxSessionID) {
		return nil, errInvalid
	}
	parse := map[string]func(map[string]json.RawMessage) (action, error){
		"set_alarm": p.setAlarm, "cancel_alarm": p.cancelAlarm, "set_timer": p.setTimer, "cancel_timer": p.cancelTimer,
		"snooze": p.snooze, "stop_ring": p.stopRing, "set_reminder": p.setReminder, "cancel_reminder": p.cancelReminder,
	}[name]
	// the args first: a command that cannot be read is invalid whoever sent it
	do, err := parse(m)
	if err != nil {
		return nil, err
	}
	// a reminder with a level raises the volume when it rings: it goes as an alarm does
	_, leveled := m["level"]
	asAlarm := name == "set_reminder" && leveled
	if asAlarm && !hasSession {
		return nil, errWakeRequired
	}
	if hasSession && !unchecked[name] {
		admitAs := name
		if asAlarm {
			admitAs = "set_alarm"
		}
		// this device's session, or the one the server vouched for on another device in this command
		trigger, known := capability.SessionTrigger(ctx, p.sessions, session)
		if err := admit(admitAs, trigger, known); err != nil {
			return nil, err
		}
	}
	res, err := do()
	if err != nil {
		return nil, err
	}
	p.publish()
	return json.Marshal(res)
}

// action is a command whose args passed, run once its session does
type action func() (any, error)

type setAlarmResult struct {
	Alarm    stateAlarm `json:"alarm"`
	Replaced bool       `json:"replaced"`
}

type setTimerResult struct {
	Timer stateTimer `json:"timer"`
}

type cancelResult struct {
	Cancelled []string `json:"cancelled"`
}

// setAlarm reads set_alarm's args into what it does
func (p *provider) setAlarm(m map[string]json.RawMessage) (action, error) {
	a := Alarm{Level: defaultLevel}
	var clock, date string
	var days []string
	if !required(m, "id", &a.ID) || !required(m, "time", &clock) {
		return nil, errInvalid
	}
	hasDays, okDays := value(m, "days", &days)
	hasDate, okDate := value(m, "date", &date)
	_, okLevel := value(m, "level", &a.Level)
	opts, okOpts := parseOptions(m)
	a.Options = opts
	_, okSound := value(m, "soundId", &a.SoundID)
	var okClock bool
	a.Hour, a.Minute, okClock = ParseTime(clock)
	if !okDays || !okDate || !okLevel || !okOpts || !okSound || !okClock || hasDays && hasDate {
		return nil, errInvalid
	}
	if hasDays {
		var ok bool
		if a.Days, ok = ParseDays(days); !ok || a.Days == 0 {
			return nil, errInvalid
		}
	}
	if hasDate {
		var ok bool
		if a.Date, ok = ParseDate(date); !ok {
			return nil, errInvalid
		}
	}
	if !a.valid() {
		return nil, errInvalid
	}
	return func() (any, error) {
		stored, replaced, err := p.book.SetAlarm(a)
		if err != nil {
			return nil, refusal(err)
		}
		return setAlarmResult{Alarm: alarmState(stored), Replaced: replaced}, nil
	}, nil
}

func (p *provider) cancelAlarm(m map[string]json.RawMessage) (action, error) {
	var id, clock string
	var all bool
	hasID, okID := value(m, "id", &id)
	hasTime, okTime := value(m, "time", &clock)
	hasAll, okAll := value(m, "all", &all)
	if !okID || !okTime || !okAll || count(hasID, hasTime, hasAll) != 1 || hasID && !ValidID(id) || hasAll && !all {
		return nil, errInvalid
	}
	hour, minute, okClock := ParseTime(clock)
	if hasTime && !okClock {
		return nil, errInvalid
	}
	return func() (any, error) {
		var gone []string
		switch {
		case hasID:
			if p.book.CancelAlarm(id) {
				gone = []string{id}
			}
		case hasTime:
			gone = p.book.CancelAlarmsAt(hour, minute)
		default:
			gone = p.book.CancelAllAlarms()
		}
		return cancelled(gone)
	}, nil
}

func (p *provider) setTimer(m map[string]json.RawMessage) (action, error) {
	var id string
	var seconds int
	if !required(m, "id", &id) || !required(m, "seconds", &seconds) || !ValidID(id) ||
		seconds < minTimerSeconds || seconds > MaxTimerSeconds {
		return nil, errInvalid
	}
	return func() (any, error) {
		t, err := p.book.SetTimer(id, seconds)
		if err != nil {
			return nil, refusal(err)
		}
		return setTimerResult{Timer: timerState(t)}, nil
	}, nil
}

// cancelTimer takes the timer with an id, the one set for a length that ends first, all, or with none of them the
// one that ends first
func (p *provider) cancelTimer(m map[string]json.RawMessage) (action, error) {
	var id string
	var seconds int
	var all bool
	hasID, okID := value(m, "id", &id)
	hasSeconds, okSeconds := value(m, "seconds", &seconds)
	hasAll, okAll := value(m, "all", &all)
	if !okID || !okSeconds || !okAll || count(hasID, hasSeconds, hasAll) > 1 || hasID && !ValidID(id) || hasAll && !all ||
		hasSeconds && (seconds < minTimerSeconds || seconds > MaxTimerSeconds) {
		return nil, errInvalid
	}
	return func() (any, error) {
		var gone []string
		switch {
		case hasID:
			if p.book.CancelTimer(id) {
				gone = []string{id}
			}
		case hasAll:
			gone = p.book.CancelAllTimers()
		default:
			var first string
			var ok bool
			if hasSeconds {
				first, ok = p.book.CancelTimerFor(seconds)
			} else {
				first, ok = p.book.CancelNextTimer()
			}
			if ok {
				gone = []string{first}
			}
		}
		return cancelled(gone)
	}, nil
}

type snoozeResult struct {
	Snoozed stateSnooze `json:"snoozed"`
}

type stopResult struct {
	Stopped stateRinging `json:"stopped"`
}

// ringTarget reads an optional kind and id: none is the ring that sounds, or for a snooze the one that just stopped
func ringTarget(m map[string]json.RawMessage) (Kind, string, bool) {
	var kind, id string
	hasKind, okKind := value(m, "kind", &kind)
	hasID, okID := value(m, "id", &id)
	if !okKind || !okID || hasKind && !Kind(kind).valid() || hasID && !ValidID(id) {
		return "", "", false
	}
	return Kind(kind), id, true
}

// snooze puts off the ring that sounds, or the one that stopped within SnoozeWindow, by minutes or the config's
func (p *provider) snooze(m map[string]json.RawMessage) (action, error) {
	kind, id, ok := ringTarget(m)
	var minutes int
	hasMinutes, okMinutes := value(m, "minutes", &minutes)
	if !ok || !okMinutes || hasMinutes && (minutes < MinSnoozeMinutes || minutes > MaxSnoozeMinutes) {
		return nil, errInvalid
	}
	return func() (any, error) {
		if p.ringer == nil {
			return nil, errUnavailable
		}
		s, err := p.ringer.Snooze(kind, id, minutes)
		if err != nil {
			return nil, refusal(err)
		}
		return snoozeResult{Snoozed: snoozeState(s)}, nil
	}, nil
}

// stopRing stops the ring that sounds, or one that waits that kind and id name
func (p *provider) stopRing(m map[string]json.RawMessage) (action, error) {
	kind, id, ok := ringTarget(m)
	if !ok {
		return nil, errInvalid
	}
	return func() (any, error) {
		if p.ringer == nil {
			return nil, errUnavailable
		}
		r, ok := p.ringer.StopFor(kind, id)
		if !ok {
			return nil, errNotFound
		}
		return stopResult{Stopped: stateRinging{Kind: string(r.Kind), ID: r.ID}}, nil
	}, nil
}

func cancelled(gone []string) (any, error) {
	if len(gone) == 0 {
		return nil, errNotFound
	}
	return cancelResult{Cancelled: gone}, nil
}

// refusal is the book's refusal as the code the server sees: a set before the zone is known is unavailable, as the
// device cannot tell when it would ring
func refusal(err error) error {
	var re *RefusedError
	switch {
	case errors.Is(err, ErrNoZone):
		return errUnavailable
	case errors.As(err, &re):
		return &capability.CommandError{Code: re.Code}
	default:
		return err
	}
}

func count(flags ...bool) int {
	n := 0
	for _, f := range flags {
		if f {
			n++
		}
	}
	return n
}
