// Package alarms keeps the device's alarms and timers: what is set, when each rings and what is due now. The device
// owns them, so they ring with the server or the network down.
package alarms

import (
	"errors"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxAlarms    = 10
	MaxTimers    = 5
	MaxReminders = 20
	// a reminder's label for screens, in runes; its full text stays on the server
	MaxLabelRunes   = 80
	MaxTimerSeconds = 86400
	MaxIDLength     = 16
	MinLevel        = 1
	MaxLevel        = 100

	// LateWindow is how late an occurrence or a timer's end may be and still ring, once: a device that was off or
	// restarting rings it when it comes back
	LateWindow = 300 * time.Second

	// a clock the network has not set yet reads before this year
	firstPlausibleYear = 2026
	maxYear            = 9999
	minTimerSeconds    = 1
)

// Days is a set of weekdays, bit 0 Monday to bit 6 Sunday; none is a one-time alarm.
type Days uint8

const EveryDay Days = 0x7F

// weekday names on disk and on the wire, in Days' bit order
var weekdayNames = [7]string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// ParseDays reads weekday names, in any order and none twice.
func ParseDays(names []string) (Days, bool) {
	var d Days
	for _, name := range names {
		bit := -1
		for i, n := range weekdayNames {
			if n == name {
				bit = i
			}
		}
		if bit < 0 || d&(1<<bit) != 0 {
			return 0, false
		}
		d |= 1 << bit
	}
	return d, true
}

// Names lists the days from mon to sun.
func (d Days) Names() []string {
	var names []string
	for i, n := range weekdayNames {
		if d&(1<<i) != 0 {
			names = append(names, n)
		}
	}
	return names
}

func (d Days) keeps(w time.Weekday) bool {
	// time.Weekday counts from Sunday
	return d&(1<<((int(w)+6)%7)) != 0
}

// Date is a calendar day; the zero Date is none.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

func (d Date) IsZero() bool { return d == Date{} }

// ParseDate reads exactly YYYY-MM-DD in ascii digits, a day the calendar has.
func ParseDate(s string) (Date, bool) {
	if len(s) != len("2006-01-02") || s[4] != '-' || s[7] != '-' {
		return Date{}, false
	}
	year, okY := digits(s[0:4])
	month, okM := digits(s[5:7])
	day, okD := digits(s[8:10])
	d := Date{year, time.Month(month), day}
	if !okY || !okM || !okD || !d.valid() {
		return Date{}, false
	}
	return d, true
}

func (d Date) String() string { return fmt.Sprintf("%04d-%02d-%02d", d.Year, int(d.Month), d.Day) }

func (d Date) valid() bool {
	if d.Year < 0 || d.Year > maxYear || d.Month < time.January || d.Month > time.December || d.Day < 1 {
		return false
	}
	return d.Day <= time.Date(d.Year, d.Month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// Alarm is what the server asked for: it never says when the alarm next rings, the book works that out.
type Alarm struct {
	ID     string
	Hour   int
	Minute int
	// none is a one-time alarm
	Days Days
	// a one-time alarm's day, never with Days; the book pins one when it is set without
	Date  Date
	Level int
	// its own ramp and limit, the config's where unset
	Options RingOptions
	// the server's ring sound it plays (its music), "" for the alarm tone
	SoundID string
}

// RampFrom is where an alarm's ramp starts: the level in force, or quiet (QuietLevel, or the level in force when that
// is lower) and up to the alarm's level, never above it.
type RampFrom string

const (
	RampCurrent RampFrom = "current"
	RampQuiet   RampFrom = "quiet"
	// where a quiet ramp starts
	QuietLevel = 5
)

func (f RampFrom) valid() bool { return f == RampCurrent || f == RampQuiet }

// RingOptions are an alarm's own ramp and ring limit; each unset one is the config's.
type RingOptions struct {
	// the ramp's length in seconds, 0 to MaxRampSeconds, when HasRamp
	RampSeconds int
	HasRamp     bool
	// "" is the config's
	RampFrom RampFrom
	// how long it rings when nothing stops it, MinAlarmLimitSeconds to MaxAlarmLimitSeconds; 0 is the config's
	LimitSeconds int
}

func (o RingOptions) valid() bool {
	return (!o.HasRamp || o.RampSeconds >= 0 && o.RampSeconds <= MaxRampSeconds) &&
		(o.RampFrom == "" || o.RampFrom.valid()) &&
		(o.LimitSeconds == 0 || o.LimitSeconds >= MinAlarmLimitSeconds && o.LimitSeconds <= MaxAlarmLimitSeconds)
}

// the config's bounds
const (
	MaxRampSeconds          = 1800
	MinAlarmLimitSeconds    = 60
	MaxAlarmLimitSeconds    = 3600
	MinTimerLimitSeconds    = 10
	MaxTimerLimitSeconds    = 600
	MinReminderLimitSeconds = 10
	MaxReminderLimitSeconds = 300
	MinSnoozeMinutes        = 1
	MaxSnoozeMinutes        = 60
	MaxAutoSnooze           = 5
	// how long after a ring stopped a snooze without an id still finds it
	SnoozeWindow = 10 * time.Minute
)

// Settings are the config's ramp, ring limits and snooze; DefaultSettings is v1's behaviour.
type Settings struct {
	Ramp          time.Duration
	RampFrom      RampFrom
	AlarmLimit    time.Duration
	TimerLimit    time.Duration
	ReminderLimit time.Duration
	SnoozeMinutes int
	// how many times an alarm nobody stopped rings again SnoozeMinutes later
	AutoSnooze int
}

func DefaultSettings() Settings {
	return Settings{
		Ramp: RampDuration, RampFrom: RampCurrent, AlarmLimit: AlarmLimit, TimerLimit: TimerLimit,
		ReminderLimit: ReminderLimit, SnoozeMinutes: DefaultSnoozeMinutes,
	}
}

const (
	ReminderLimit        = 60 * time.Second
	DefaultSnoozeMinutes = 9
)

// Time is the alarm's HH:MM.
func (a Alarm) Time() string { return fmt.Sprintf("%02d:%02d", a.Hour, a.Minute) }

// ParseTime reads exactly HH:MM, 00:00 to 23:59.
func ParseTime(s string) (hour, minute int, ok bool) {
	if len(s) != len("15:04") || s[2] != ':' {
		return 0, 0, false
	}
	hour, okH := digits(s[0:2])
	minute, okM := digits(s[3:5])
	if !okH || !okM || hour > 23 || minute > 59 {
		return 0, 0, false
	}
	return hour, minute, true
}

func (a Alarm) valid() bool {
	if !ValidID(a.ID) || a.SoundID != "" && !ValidID(a.SoundID) || a.Hour < 0 || a.Hour > 23 || a.Minute < 0 || a.Minute > 59 ||
		a.Level < MinLevel || a.Level > MaxLevel || a.Days&^EveryDay != 0 || !a.Options.valid() {
		return false
	}
	if a.Date.IsZero() {
		return true
	}
	return a.Days == 0 && a.Date.valid()
}

// Reminder rings its chime and its spoken text at a time of day, once or on repeat as an alarm does, or once at an
// instant (At: "in 20 minutes"). The device keeps a short label of it for screens; the server keeps its text and the
// speech of it (SoundID). Without a level it rings at the level in force, as a timer does; with one it ramps as an
// alarm.
type Reminder struct {
	ID     string
	Hour   int
	Minute int
	Days   Days
	Date   Date
	// a one-time reminder at an instant, never with a time, days or date
	At      time.Time
	Label   string
	SoundID string
	// 0 for none
	Level int
}

// Time is the reminder's HH:MM, "" for one at an instant.
func (r Reminder) Time() string {
	if !r.At.IsZero() {
		return ""
	}
	return fmt.Sprintf("%02d:%02d", r.Hour, r.Minute)
}

func (r Reminder) valid() bool {
	if !ValidID(r.ID) || !ValidLabel(r.Label) || r.SoundID != "" && !ValidID(r.SoundID) ||
		r.Level != 0 && (r.Level < MinLevel || r.Level > MaxLevel) {
		return false
	}
	if !r.At.IsZero() {
		return r.Days == 0 && r.Date.IsZero() && r.Hour == 0 && r.Minute == 0
	}
	return r.schedule().valid()
}

// schedule is the reminder's time, days and date as an alarm's, for the calendar code
func (r Reminder) schedule() Alarm {
	return Alarm{ID: r.ID, Hour: r.Hour, Minute: r.Minute, Days: r.Days, Date: r.Date, Level: MinLevel}
}

// ValidLabel is valid utf-8 of 1 to MaxLabelRunes runes with no control character.
func ValidLabel(s string) bool {
	n := utf8.RuneCountInString(s)
	if n == 0 || n > MaxLabelRunes || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// Timer ends Seconds after it was set; EndsAt is wall time, so it outlives a restart.
type Timer struct {
	ID      string
	Seconds int
	EndsAt  time.Time
}

// Remaining is what is left at now in whole seconds, never more than the timer was set for, whatever the clock did.
func (t Timer) Remaining(now time.Time) int {
	return int(min(max(t.EndsAt.Unix()-now.Unix(), 0), int64(t.Seconds)))
}

type Kind string

const (
	KindAlarm Kind = "alarm"
	KindTimer Kind = "timer"
	// a reminder rings its chime and the server's speech of its text
	KindReminder Kind = "reminder"
)

func (k Kind) valid() bool { return k == KindAlarm || k == KindTimer || k == KindReminder }

// Ring is one thing to ring now: At is an alarm's or a reminder's occurrence or a timer's end. A timer has no level,
// nor a reminder set without one: they never touch the volume.
type Ring struct {
	Kind  Kind
	ID    string
	Level int
	At    time.Time
	// an alarm's own ramp and limit
	Options RingOptions
	// the server's ring sound: an alarm's music, a reminder's speech; "" for none
	SoundID string
	// how many times it already rang again on its own, nobody having stopped it
	AutoSnoozes int
}

// Snooze is a ring put off until Until: the ring itself, kept apart from the schedule, so a repeating alarm's days
// and a one-time alarm's end stay as they were.
type Snooze struct {
	Ring
	Until time.Time
}

// LastRing is the ring that last stopped, and what stopped it.
type LastRing struct {
	Ring
	StoppedAt time.Time
	StoppedBy StopReason
}

// RefusedError is why the book did not take a set; Code is what the server may see.
type RefusedError struct{ Code string }

func (e *RefusedError) Error() string { return "alarms: refused: " + e.Code }

var (
	ErrFull    = &RefusedError{Code: "full"}
	ErrPast    = &RefusedError{Code: "past"}
	ErrStore   = &RefusedError{Code: "store"}
	ErrInvalid = &RefusedError{Code: "invalid"}
	// a set alarm before a zone was set: its calendar is not known yet
	ErrNoZone = &RefusedError{Code: "no_zone"}
	// a snooze that names no ring that sounds or stopped within SnoozeWindow
	ErrNothing = &RefusedError{Code: "not_found"}

	ErrZone = errors.New("alarms: unknown time zone")
)

// ValidID is /^[a-z0-9]{1,16}$/.
func ValidID(s string) bool {
	if len(s) == 0 || len(s) > MaxIDLength {
		return false
	}
	for _, c := range []byte(s) {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// the counts of each kind the book keeps, for its limits
const maxRings = MaxAlarms + MaxTimers + MaxReminders

// Counts are what the book did since it opened, for status and metrics.
type Counts struct {
	Sets     int
	Cancels  int
	Refusals int
	Rings    int
	Missed   int
}

func digits(s string) (int, bool) {
	n := 0
	for _, c := range []byte(s) {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}
