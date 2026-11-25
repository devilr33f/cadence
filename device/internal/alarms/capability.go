package alarms

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// CapabilityID is the device's alarms and timers: the server sets and cancels them by command, and the device rings
// them on its own.
const CapabilityID = "alarms"

// Sessions tells how a recent session began; an id it does not know is false.
type Sessions interface {
	Trigger(sessionID string) (cadencev2.Trigger, bool)
}

type stateAlarm struct {
	ID           string   `json:"id"`
	Time         string   `json:"time"`
	Days         []string `json:"days,omitempty"`
	Date         string   `json:"date,omitempty"`
	Level        int      `json:"level"`
	RampSeconds  *int     `json:"rampSeconds,omitempty"`
	RampFrom     string   `json:"rampFrom,omitempty"`
	LimitSeconds int      `json:"limitSeconds,omitempty"`
	// the server's ring sound it plays, absent for its tone
	SoundID string `json:"soundId,omitempty"`
	// when it next rings, a snooze of it included; absent while its calendar is unknown
	Next string `json:"next,omitempty"`
}

// stateReminder is a reminder with its short label (the user's own words, for screens: never logged)
type stateReminder struct {
	ID      string   `json:"id"`
	Time    string   `json:"time,omitempty"`
	Days    []string `json:"days,omitempty"`
	Date    string   `json:"date,omitempty"`
	At      string   `json:"at,omitempty"`
	Label   string   `json:"label"`
	SoundID string   `json:"soundId,omitempty"`
	Level   int      `json:"level,omitempty"`
	Next    string   `json:"next,omitempty"`
}

type stateTimer struct {
	ID      string `json:"id"`
	Seconds int    `json:"seconds"`
	EndsAt  string `json:"endsAt"`
	Next    string `json:"next"`
}

type stateSnooze struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Until string `json:"until"`
}

type stateLastRing struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	// when it stopped
	At        string `json:"at"`
	StoppedBy string `json:"stoppedBy"`
}

type stateRinging struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type state struct {
	Alarms    []stateAlarm    `json:"alarms"`
	Timers    []stateTimer    `json:"timers"`
	Reminders []stateReminder `json:"reminders"`
	Ringing   *stateRinging   `json:"ringing"`
	Snoozed   []stateSnooze   `json:"snoozed"`
	LastRing  *stateLastRing  `json:"lastRing"`
}

func alarmState(a Alarm) stateAlarm {
	s := stateAlarm{ID: a.ID, Time: a.Time(), Days: a.Days.Names(), Level: a.Level, RampFrom: string(a.Options.RampFrom),
		LimitSeconds: a.Options.LimitSeconds, SoundID: a.SoundID}
	if !a.Date.IsZero() {
		s.Date = a.Date.String()
	}
	if a.Options.HasRamp {
		seconds := a.Options.RampSeconds
		s.RampSeconds = &seconds
	}
	return s
}

// reminderState is r as the state shows it, with when it next rings
func (p *provider) reminderState(r Reminder) stateReminder {
	s := stateReminder{ID: r.ID, Time: r.Time(), Days: r.Days.Names(), Label: r.Label, SoundID: r.SoundID, Level: r.Level}
	if !r.Date.IsZero() {
		s.Date = r.Date.String()
	}
	if !r.At.IsZero() {
		s.At = rfc3339(r.At)
	}
	if next, ok := p.book.NextOfReminder(r); ok {
		s.Next = rfc3339(next)
	}
	return s
}

func timerState(t Timer) stateTimer {
	end := rfc3339(t.EndsAt)
	return stateTimer{ID: t.ID, Seconds: t.Seconds, EndsAt: end, Next: end}
}

func snoozeState(s Snooze) stateSnooze {
	return stateSnooze{Kind: string(s.Kind), ID: s.ID, Until: rfc3339(s.Until)}
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// provider is the alarms capability: the book's alarms and timers and the ring that sounds as its state, the four
// commands, and the zone the book rings by as its config
type provider struct {
	book     *Book
	sessions Sessions
	log      *slog.Logger
	// the ringer over the book, for a snooze or a stop of the ring that sounds
	ringer *Ringer
	state  chan json.RawMessage
	// the state's own subscription to the book, taken before any change can slip by
	changes <-chan struct{}

	mu      sync.Mutex
	ringing *stateRinging
}

func newProvider(book *Book, sessions Sessions, log *slog.Logger) *provider {
	p := &provider{book: book, sessions: sessions, log: log, state: make(chan json.RawMessage, 1), changes: book.Changes()}
	p.publish()
	return p
}

func (*provider) Descriptor() capability.Descriptor {
	return capability.Descriptor{
		ID: CapabilityID, Version: 1, ConfigSchema: configSchema, StateSchema: stateSchema, Commands: commandSchemas(),
	}
}

// Apply takes {timeZone, rampSeconds?, rampFrom?, alarmLimitSeconds?, timerLimitSeconds?, reminderLimitSeconds?,
// snoozeMinutes?, autoSnooze?}: the IANA zone of the device's timeZone setting, which the alarms ring by from now on,
// and its alarms setting, each key absent its default. A config it refuses leaves the one in force, or none.
func (p *provider) Apply(_ context.Context, _ int64, raw json.RawMessage) error {
	m, ok := object(raw, "timeZone", "rampSeconds", "rampFrom", "alarmLimitSeconds", "timerLimitSeconds",
		"reminderLimitSeconds", "snoozeMinutes", "autoSnooze")
	var zone string
	if !ok || !required(m, "timeZone", &zone) || zone == "" {
		return &capability.ApplyError{Code: "invalid"}
	}
	settings, ok := parseSettings(m)
	if !ok {
		return &capability.ApplyError{Code: "invalid"}
	}
	if p.book.SetZone(zone) != nil {
		return &capability.ApplyError{Code: "invalid"}
	}
	p.book.SetSettings(settings)
	return nil
}

// parseSettings reads the config's ramp, limits and snooze over the defaults, each within its bounds
func parseSettings(m map[string]json.RawMessage) (Settings, bool) {
	s := DefaultSettings()
	ok := true
	seconds := func(key string, lo, hi int, to *time.Duration) {
		var n int
		present, good := value(m, key, &n)
		if !present {
			return
		}
		if !good || n < lo || n > hi {
			ok = false
			return
		}
		*to = time.Duration(n) * time.Second
	}
	seconds("rampSeconds", 0, MaxRampSeconds, &s.Ramp)
	seconds("alarmLimitSeconds", MinAlarmLimitSeconds, MaxAlarmLimitSeconds, &s.AlarmLimit)
	seconds("timerLimitSeconds", MinTimerLimitSeconds, MaxTimerLimitSeconds, &s.TimerLimit)
	seconds("reminderLimitSeconds", MinReminderLimitSeconds, MaxReminderLimitSeconds, &s.ReminderLimit)
	var from string
	if present, good := value(m, "rampFrom", &from); present {
		s.RampFrom = RampFrom(from)
		ok = ok && good && s.RampFrom.valid()
	}
	if present, good := value(m, "snoozeMinutes", &s.SnoozeMinutes); present {
		ok = ok && good && s.SnoozeMinutes >= MinSnoozeMinutes && s.SnoozeMinutes <= MaxSnoozeMinutes
	}
	if present, good := value(m, "autoSnooze", &s.AutoSnooze); present {
		ok = ok && good && s.AutoSnooze >= 0 && s.AutoSnooze <= MaxAutoSnooze
	}
	return s, ok
}

// Command sets or cancels an alarm or a timer for the session the args name (rule 7: see admit) and answers what the
// book did; never logged here beyond what the book logs.
func (p *provider) Command(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	return p.command(ctx, name, args)
}

func (p *provider) State() <-chan json.RawMessage { return p.state }

// follow publishes the state on each change of the book until ctx ends
func (p *provider) follow(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.changes:
			p.publish()
		}
	}
}

// setRinging says which ring sounds, nil for none
func (p *provider) setRinging(r *Ring) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ringing = nil
	if r != nil {
		p.ringing = &stateRinging{Kind: string(r.Kind), ID: r.ID}
	}
	p.publishLocked()
}

func (p *provider) publish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.publishLocked()
}

// publishLocked offers the state, keeping only the latest; under mu, so states go out in the order they were made.
// callers hold mu
func (p *provider) publishLocked() {
	list, timers := p.book.List()
	snoozed := p.book.Snoozed()
	reminders := p.book.Reminders()
	s := state{Alarms: make([]stateAlarm, 0, len(list)), Timers: make([]stateTimer, 0, len(timers)), Ringing: p.ringing,
		Snoozed: make([]stateSnooze, 0, len(snoozed)), Reminders: make([]stateReminder, 0, len(reminders))}
	for _, r := range reminders {
		s.Reminders = append(s.Reminders, p.reminderState(r))
	}
	for _, a := range list {
		e := alarmState(a)
		if next, ok := p.book.NextOf(a); ok {
			e.Next = rfc3339(next)
		}
		s.Alarms = append(s.Alarms, e)
	}
	for _, t := range timers {
		s.Timers = append(s.Timers, timerState(t))
	}
	for _, z := range snoozed {
		s.Snoozed = append(s.Snoozed, snoozeState(z))
	}
	if l, ok := p.book.Last(); ok {
		s.LastRing = &stateLastRing{Kind: string(l.Kind), ID: l.ID, At: rfc3339(l.StoppedAt), StoppedBy: string(l.StoppedBy)}
	}
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	for {
		select {
		case p.state <- b:
			return
		default:
		}
		select {
		case <-p.state:
		default:
		}
	}
}
