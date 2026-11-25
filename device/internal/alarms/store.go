package alarms

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/atomicfile"
)

// ten alarms, five timers and twenty reminders with their labels are well under this; anything longer is garbage
const maxStoreBytes = 65536

// StorePath is the book's file in the daemon's data dir.
func StorePath(dataDir string) string { return filepath.Join(dataDir, "state", "alarms") }

// storedAlarm carries the latest occurrence the alarm is done with: it rang for it, or it was already behind the
// alarm when it was set. the book never rings one twice, across restarts too
type storedAlarm struct {
	alarm  Alarm
	rungAt time.Time
	rung   bool
}

type fileAlarm struct {
	ID           string   `json:"id"`
	Time         string   `json:"time"`
	Days         []string `json:"days,omitempty"`
	Date         string   `json:"date,omitempty"`
	Level        int      `json:"level"`
	LastRungAt   *int64   `json:"lastRungAt,omitempty"`
	RampSeconds  *int     `json:"rampSeconds,omitempty"`
	RampFrom     string   `json:"rampFrom,omitempty"`
	LimitSeconds int      `json:"limitSeconds,omitempty"`
	SoundID      string   `json:"soundId,omitempty"`
}

type fileReminder struct {
	ID         string   `json:"id"`
	Time       string   `json:"time,omitempty"`
	Days       []string `json:"days,omitempty"`
	Date       string   `json:"date,omitempty"`
	At         int64    `json:"at,omitempty"`
	Label      string   `json:"label"`
	SoundID    string   `json:"soundId,omitempty"`
	Level      int      `json:"level,omitempty"`
	LastRungAt *int64   `json:"lastRungAt,omitempty"`
}

// fileRing is a ring kept for a snooze, or the one that last stopped
type fileRing struct {
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	Level        int    `json:"level,omitempty"`
	RampSeconds  *int   `json:"rampSeconds,omitempty"`
	RampFrom     string `json:"rampFrom,omitempty"`
	LimitSeconds int    `json:"limitSeconds,omitempty"`
	AutoSnoozes  int    `json:"autoSnoozes,omitempty"`
	SoundID      string `json:"soundId,omitempty"`
	// a snooze's
	Until int64 `json:"until,omitempty"`
	// the last ring's
	StoppedAt int64  `json:"stoppedAt,omitempty"`
	StoppedBy string `json:"stoppedBy,omitempty"`
}

// stored is everything the file keeps
type stored struct {
	alarms    []storedAlarm
	timers    []Timer
	reminders []storedReminder
	snoozed   []Snooze
	last      *LastRing
}

type fileTimer struct {
	ID      string `json:"id"`
	Seconds int    `json:"seconds"`
	EndsAt  int64  `json:"endsAt"`
}

type fileBook struct {
	Alarms    []fileAlarm    `json:"alarms"`
	Timers    []fileTimer    `json:"timers"`
	Reminders []fileReminder `json:"reminders,omitempty"`
	Snoozed   []fileRing     `json:"snoozed,omitempty"`
	Last      *fileRing      `json:"lastRing,omitempty"`
}

// load reads the book. a missing file is a fresh install: empty, nothing logged. a file that cannot be trusted
// (unreadable, too long, not json, an unknown key, a null, a value out of range or of the wrong type, more than the
// limits, a repeated id) is empty too, with one alarms_store_invalid line that carries none of its content
func load(path string, log *slog.Logger) stored {
	raw, err := readCapped(path)
	if errors.Is(err, fs.ErrNotExist) {
		return stored{}
	}
	if err != nil {
		log.Warn("alarms_store_invalid", "reason", "read")
		return stored{}
	}
	f, ok := parseBook(raw)
	if !ok {
		log.Warn("alarms_store_invalid", "reason", "format")
		return stored{}
	}
	return f
}

var errTooLong = errors.New("alarms: store too long")

func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // a fixed name in the daemon's data dir
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxStoreBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxStoreBytes {
		return nil, errTooLong
	}
	return raw, nil
}

// save writes the whole book, mode 600, creating the state dir on a fresh install
func save(path string, st stored) error {
	f := fileBook{Alarms: []fileAlarm{}, Timers: []fileTimer{}}
	for _, s := range st.alarms {
		a := s.alarm
		e := fileAlarm{ID: a.ID, Time: a.Time(), Days: a.Days.Names(), Level: a.Level, RampFrom: string(a.Options.RampFrom),
			LimitSeconds: a.Options.LimitSeconds, SoundID: a.SoundID}
		if a.Options.HasRamp {
			e.RampSeconds = &a.Options.RampSeconds
		}
		if !a.Date.IsZero() {
			e.Date = a.Date.String()
		}
		if s.rung {
			at := s.rungAt.Unix()
			e.LastRungAt = &at
		}
		f.Alarms = append(f.Alarms, e)
	}
	for _, t := range st.timers {
		f.Timers = append(f.Timers, fileTimer{ID: t.ID, Seconds: t.Seconds, EndsAt: t.EndsAt.Unix()})
	}
	for _, s := range st.reminders {
		f.Reminders = append(f.Reminders, reminderFile(s))
	}
	for _, s := range st.snoozed {
		e := ringFile(s.Ring)
		e.Until = s.Until.Unix()
		f.Snoozed = append(f.Snoozed, e)
	}
	if l := st.last; l != nil {
		e := ringFile(l.Ring)
		e.StoppedAt, e.StoppedBy = l.StoppedAt.Unix(), string(l.StoppedBy)
		f.Last = &e
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(path, append(raw, '\n'))
}

// encoding/json matches keys without case, keeps the last of a key given twice and takes a null as absent, so every
// object is read key by key
func parseBook(raw []byte) (stored, bool) {
	top, ok := object(raw, "alarms", "timers", "reminders", "snoozed", "lastRing")
	if !ok {
		return stored{}, false
	}
	var st stored
	ok = parseList(top, "alarms", MaxAlarms, func(raw json.RawMessage) (string, bool) {
		s, ok := parseAlarm(raw)
		st.alarms = append(st.alarms, s)
		return s.alarm.ID, ok
	}) && parseList(top, "timers", MaxTimers, func(raw json.RawMessage) (string, bool) {
		t, ok := parseTimer(raw)
		st.timers = append(st.timers, t)
		return t.ID, ok
	}) && parseList(top, "reminders", MaxReminders, func(raw json.RawMessage) (string, bool) {
		r, ok := parseReminder(raw)
		st.reminders = append(st.reminders, r)
		return r.reminder.ID, ok
	}) && parseList(top, "snoozed", maxSnoozed, func(raw json.RawMessage) (string, bool) {
		s, ok := parseSnooze(raw)
		st.snoozed = append(st.snoozed, s)
		// one snooze per kind and id
		return string(s.Kind) + ":" + s.ID, ok
	})
	if !ok {
		return stored{}, false
	}
	if raw, present := top["lastRing"]; present {
		l, ok := parseLast(raw)
		if !ok {
			return stored{}, false
		}
		st.last = &l
	}
	return st, true
}

func ringFile(r Ring) fileRing {
	e := fileRing{Kind: string(r.Kind), ID: r.ID, Level: r.Level, RampFrom: string(r.Options.RampFrom),
		LimitSeconds: r.Options.LimitSeconds, AutoSnoozes: r.AutoSnoozes, SoundID: r.SoundID}
	if r.Options.HasRamp {
		seconds := r.Options.RampSeconds
		e.RampSeconds = &seconds
	}
	return e
}

var ringKeys = []string{"kind", "id", "level", "rampSeconds", "rampFrom", "limitSeconds", "autoSnoozes", "soundId"}

// parseRing reads a ring's own keys from m: an alarm's has a level, a timer's none, a reminder's either
func parseRing(m map[string]json.RawMessage) (Ring, bool) {
	var r Ring
	var kind string
	if !required(m, "kind", &kind) || !required(m, "id", &r.ID) {
		return Ring{}, false
	}
	r.Kind = Kind(kind)
	hasLevel, okLevel := value(m, "level", &r.Level)
	opts, okOpts := parseOptions(m)
	_, okAuto := value(m, "autoSnoozes", &r.AutoSnoozes)
	hasSound, okSound := value(m, "soundId", &r.SoundID)
	r.Options = opts
	levelFits := hasLevel == (r.Kind == KindAlarm) || r.Kind == KindReminder
	if !okLevel || !okOpts || !okAuto || !okSound || hasSound && !ValidID(r.SoundID) || !r.Kind.valid() ||
		!ValidID(r.ID) || r.AutoSnoozes < 0 || r.AutoSnoozes > MaxAutoSnooze || !levelFits ||
		hasLevel && (r.Level < MinLevel || r.Level > MaxLevel) {
		return Ring{}, false
	}
	return r, true
}

// parseOptions reads an alarm's own ramp and limit, each optional
func parseOptions(m map[string]json.RawMessage) (RingOptions, bool) {
	var o RingOptions
	var from string
	hasRamp, okRamp := value(m, "rampSeconds", &o.RampSeconds)
	_, okFrom := value(m, "rampFrom", &from)
	hasLimit, okLimit := value(m, "limitSeconds", &o.LimitSeconds)
	o.HasRamp, o.RampFrom = hasRamp, RampFrom(from)
	if !okRamp || !okFrom || !okLimit || hasLimit && o.LimitSeconds == 0 || !o.valid() {
		return RingOptions{}, false
	}
	return o, true
}

func parseSnooze(raw json.RawMessage) (Snooze, bool) {
	m, ok := object(raw, append(slices.Clone(ringKeys), "until")...)
	if !ok {
		return Snooze{}, false
	}
	r, ok := parseRing(m)
	var until int64
	if !ok || !required(m, "until", &until) {
		return Snooze{}, false
	}
	return Snooze{Ring: r, Until: time.Unix(until, 0).UTC()}, true
}

func parseLast(raw json.RawMessage) (LastRing, bool) {
	m, ok := object(raw, append(slices.Clone(ringKeys), "stoppedAt", "stoppedBy")...)
	if !ok {
		return LastRing{}, false
	}
	r, ok := parseRing(m)
	var at int64
	var by string
	if !ok || !required(m, "stoppedAt", &at) || !required(m, "stoppedBy", &by) || !StopReason(by).valid() {
		return LastRing{}, false
	}
	return LastRing{Ring: r, StoppedAt: time.Unix(at, 0).UTC(), StoppedBy: StopReason(by)}, true
}

// parseList reads the entries under key, none when it is absent; no more than limit and no id twice
func parseList(top map[string]json.RawMessage, key string, limit int, parse func(json.RawMessage) (string, bool)) bool {
	var list []json.RawMessage
	present, ok := value(top, key, &list)
	if !present {
		return true
	}
	if !ok || len(list) > limit {
		return false
	}
	seen := make(map[string]bool, len(list))
	for _, raw := range list {
		id, ok := parse(raw)
		if !ok || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func parseAlarm(raw json.RawMessage) (storedAlarm, bool) {
	m, ok := object(raw, "id", "time", "days", "date", "level", "lastRungAt", "rampSeconds", "rampFrom", "limitSeconds",
		"soundId")
	if !ok {
		return storedAlarm{}, false
	}
	var s storedAlarm
	var clock, date string
	var days []string
	var rungAt int64
	okID := required(m, "id", &s.alarm.ID)
	okTime := required(m, "time", &clock)
	okLevel := required(m, "level", &s.alarm.Level)
	hasDays, okDays := value(m, "days", &days)
	hasDate, okDate := value(m, "date", &date)
	hasRung, okRung := value(m, "lastRungAt", &rungAt)
	opts, okOpts := parseOptions(m)
	_, okSound := value(m, "soundId", &s.alarm.SoundID)
	if !okID || !okTime || !okLevel || !okDays || !okDate || !okRung || !okOpts || !okSound {
		return storedAlarm{}, false
	}
	s.alarm.Options = opts
	// a stored one-time alarm always has its pinned day
	if hasDays == hasDate {
		return storedAlarm{}, false
	}
	var okClock bool
	if s.alarm.Hour, s.alarm.Minute, okClock = ParseTime(clock); !okClock {
		return storedAlarm{}, false
	}
	if hasDays {
		if s.alarm.Days, ok = ParseDays(days); !ok || s.alarm.Days == 0 {
			return storedAlarm{}, false
		}
	}
	if hasDate {
		if s.alarm.Date, ok = ParseDate(date); !ok {
			return storedAlarm{}, false
		}
	}
	if hasRung {
		// an alarm set on a clock that reads 1970 has its occurrence before the epoch
		s.rungAt, s.rung = time.Unix(rungAt, 0).UTC(), true
	}
	return s, s.alarm.valid()
}

func reminderFile(s storedReminder) fileReminder {
	r := s.reminder
	e := fileReminder{ID: r.ID, Time: r.Time(), Days: r.Days.Names(), Label: r.Label, SoundID: r.SoundID, Level: r.Level}
	if !r.At.IsZero() {
		e.At = r.At.Unix()
	}
	if !r.Date.IsZero() {
		e.Date = r.Date.String()
	}
	if s.rung {
		at := s.rungAt.Unix()
		e.LastRungAt = &at
	}
	return e
}

// parseReminder reads a reminder: at an instant, or at a time with days or its pinned date
func parseReminder(raw json.RawMessage) (storedReminder, bool) {
	m, ok := object(raw, "id", "time", "days", "date", "at", "label", "soundId", "level", "lastRungAt")
	if !ok {
		return storedReminder{}, false
	}
	var s storedReminder
	r := &s.reminder
	var clock, date string
	var days []string
	var at, rungAt int64
	okID := required(m, "id", &r.ID)
	okLabel := required(m, "label", &r.Label)
	hasTime, okTime := value(m, "time", &clock)
	hasDays, okDays := value(m, "days", &days)
	hasDate, okDate := value(m, "date", &date)
	hasAt, okAt := value(m, "at", &at)
	_, okSound := value(m, "soundId", &r.SoundID)
	_, okLevel := value(m, "level", &r.Level)
	hasRung, okRung := value(m, "lastRungAt", &rungAt)
	if !okID || !okLabel || !okTime || !okDays || !okDate || !okAt || !okSound || !okLevel || !okRung ||
		hasAt == hasTime || hasAt && (hasDays || hasDate) || hasTime && hasDays == hasDate {
		return storedReminder{}, false
	}
	if hasAt {
		r.At = time.Unix(at, 0).UTC()
	} else {
		var okClock bool
		if r.Hour, r.Minute, okClock = ParseTime(clock); !okClock {
			return storedReminder{}, false
		}
	}
	if hasDays {
		if r.Days, ok = ParseDays(days); !ok || r.Days == 0 {
			return storedReminder{}, false
		}
	}
	if hasDate {
		if r.Date, ok = ParseDate(date); !ok {
			return storedReminder{}, false
		}
	}
	if hasRung {
		s.rungAt, s.rung = time.Unix(rungAt, 0).UTC(), true
	}
	return s, r.valid()
}

func parseTimer(raw json.RawMessage) (Timer, bool) {
	m, ok := object(raw, "id", "seconds", "endsAt")
	if !ok {
		return Timer{}, false
	}
	var t Timer
	var endsAt int64
	okID := required(m, "id", &t.ID)
	okSeconds := required(m, "seconds", &t.Seconds)
	okEnds := required(m, "endsAt", &endsAt)
	if !okID || !okSeconds || !okEnds || !ValidID(t.ID) || t.Seconds < minTimerSeconds || t.Seconds > MaxTimerSeconds {
		return Timer{}, false
	}
	t.EndsAt = time.Unix(endsAt, 0).UTC()
	return t, true
}

// object is one JSON object whose keys are all among keys, exactly as written and none twice
func object(raw []byte, keys ...string) (map[string]json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	m := make(map[string]json.RawMessage, len(keys))
	for dec.More() {
		t, err := dec.Token()
		key, isKey := t.(string)
		if err != nil || !isKey || !slices.Contains(keys, key) {
			return nil, false
		}
		if _, twice := m[key]; twice {
			return nil, false
		}
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return nil, false
		}
		m[key] = v
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return m, true
}

// value decodes m[key] into to: absent is fine, null is not
func value(m map[string]json.RawMessage, key string, to any) (present, ok bool) {
	raw, present := m[key]
	if !present {
		return false, true
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true, false
	}
	return true, json.Unmarshal(raw, to) == nil
}

func required(m map[string]json.RawMessage, key string, to any) bool {
	present, ok := value(m, key, to)
	return present && ok
}
