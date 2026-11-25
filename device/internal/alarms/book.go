package alarms

import (
	"errors"
	"log/slog"
	"slices"
	"sync"
	"syscall"
	"time"
)

// the ring loop looks again at least this often, so a clock the network sets late is seen within a minute
const maxTick = time.Minute

// Book holds the alarms and timers and tells what is due among them. Every change is saved before it returns, and a
// ring is marked as rung before TakeDue hands it out, so a restart never rings it twice while saves succeed; after
// alarms_store_failed the mark lives in memory only, and a restart within LateWindow may ring it again. Safe for
// concurrent use.
type Book struct {
	path  string
	clock Clock
	log   *slog.Logger

	mu        sync.Mutex
	loc       *time.Location
	zoneSet   bool
	alarms    []storedAlarm
	timers    []Timer
	reminders []storedReminder
	// rings put off, apart from the schedule; one per kind and id
	snoozed []Snooze
	// the ring that last stopped; nil for none
	last     *LastRing
	settings Settings
	counts   Counts
	subs     []chan struct{}
}

// NewBook loads the book at path. Until a SetZone succeeds it holds the alarms: none rings, goes or is set
// (ErrNoZone), since any calendar but the owner's would ring them at the wrong hour; timers run. It drops nothing
// late either: the first TakeDue does. A missing or untrusted file is an empty book.
func NewBook(path string, clock Clock, log *slog.Logger) (*Book, error) {
	if path == "" || clock == nil {
		return nil, errors.New("alarms: a book needs a path and a clock")
	}
	return openBook(path, clock, log), nil
}

// openBook is NewBook for a path and a clock it was given
func openBook(path string, clock Clock, log *slog.Logger) *Book {
	if log == nil {
		log = slog.Default()
	}
	b := &Book{path: path, clock: clock, log: log, loc: time.UTC, settings: DefaultSettings()}
	f := load(path, log)
	b.alarms, b.timers, b.reminders, b.snoozed, b.last = f.alarms, f.timers, f.reminders, f.snoozed, f.last
	return b
}

// SetZone sets the IANA zone whose calendar alarms ring by; "" is UTC. A name it refuses (ErrZone) leaves the zone in
// force, or none.
func (b *Book) SetZone(name string) error {
	loc, err := loadZone(name)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.zoneSet && loc.String() == b.loc.String() {
		return nil
	}
	b.loc, b.zoneSet = loc, true
	b.notify()
	return nil
}

// Zone is the zone the alarms ring by; false until a SetZone succeeded.
func (b *Book) Zone() (*time.Location, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.loc, b.zoneSet
}

// Changes is a new subscriber's channel: it has a value after any later change of what the book holds or of its zone,
// and changes that come before it is read fold into one. Call it once per reader.
func (b *Book) Changes() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan struct{}, 1)
	b.subs = append(b.subs, ch)
	return ch
}

// SetAlarm stores a, in place of the alarm with its id or of its twin (the same time, days and date under another
// id), and says which. A one-time alarm without a date gets the day of its next occurrence. ErrPast for a one-time
// alarm with no occurrence after now once the clock is plausible, ErrFull for a new alarm past MaxAlarms, ErrStore
// when it cannot be saved, ErrInvalid for what the book cannot hold, ErrNoZone before a zone is set; a refusal leaves
// the book as it was.
func (b *Book) SetAlarm(a Alarm) (stored Alarm, replaced bool, err error) {
	now := wall(b.clock.Now())
	b.mu.Lock()
	defer b.mu.Unlock()
	if !a.valid() {
		return Alarm{}, false, b.refuse(KindAlarm, ErrInvalid, a.ID)
	}
	if !b.zoneSet {
		return Alarm{}, false, b.refuse(KindAlarm, ErrNoZone, a.ID)
	}
	if a.Days == 0 {
		if a.Date.IsZero() {
			d, ok := nextDate(a.Hour, a.Minute, now, b.loc)
			if !ok {
				return Alarm{}, false, b.refuse(KindAlarm, ErrPast, a.ID)
			}
			a.Date = d
		}
		// the server has no clock: it confirms a date that is gone as readily as one ahead. until the clock is set no
		// date is told from a past one
		if _, ok := nextRing(a, now, b.loc); !ok && plausible(now, b.loc) {
			return Alarm{}, false, b.refuse(KindAlarm, ErrPast, a.ID)
		}
	}
	s := storedAlarm{alarm: a}
	// an occurrence up to now is behind a new alarm: one set at 07:01 for 07:00 is for tomorrow
	s.rungAt, s.rung = lastRing(a, now, b.loc)

	previous := slices.Clone(b.alarms)
	// the server tells alarms apart by the dates this book pins, so the one-time alarm it is asked for twice comes
	// under two ids: the new id takes the twin's place, unless it holds one of its own, and the twin goes
	twin := func(e storedAlarm) bool { return twins(e.alarm, a) }
	i := slices.IndexFunc(b.alarms, func(e storedAlarm) bool { return e.alarm.ID == a.ID })
	if i < 0 {
		i = slices.IndexFunc(b.alarms, twin)
	}
	switch {
	case i >= 0:
		b.alarms[i] = s
	case len(b.alarms) >= MaxAlarms:
		return Alarm{}, false, b.refuse(KindAlarm, ErrFull, a.ID)
	default:
		b.alarms = append(b.alarms, s)
	}
	b.alarms = slices.DeleteFunc(b.alarms, twin)
	if b.persist() != nil {
		b.alarms = previous
		return Alarm{}, false, b.refuse(KindAlarm, ErrStore, a.ID)
	}
	b.counts.Sets++
	b.log.Info("alarm_set", "id", a.ID)
	b.notify()
	return a, i >= 0, nil
}

// SetTimer starts a timer that ends seconds from now, in place of the timer with its id; identical timers under
// other ids keep running. Refused as SetAlarm, without ErrPast.
func (b *Book) SetTimer(id string, seconds int) (Timer, error) {
	now := wall(b.clock.Now())
	b.mu.Lock()
	defer b.mu.Unlock()
	if !ValidID(id) || seconds < minTimerSeconds || seconds > MaxTimerSeconds {
		return Timer{}, b.refuse(KindTimer, ErrInvalid, id)
	}
	t := Timer{ID: id, Seconds: seconds, EndsAt: now.Add(time.Duration(seconds) * time.Second).UTC()}
	previous := slices.Clone(b.timers)
	i := slices.IndexFunc(b.timers, func(e Timer) bool { return e.ID == id })
	switch {
	case i >= 0:
		b.timers[i] = t
	case len(b.timers) >= MaxTimers:
		return Timer{}, b.refuse(KindTimer, ErrFull, id)
	default:
		b.timers = append(b.timers, t)
	}
	if b.persist() != nil {
		b.timers = previous
		return Timer{}, b.refuse(KindTimer, ErrStore, id)
	}
	b.counts.Sets++
	b.log.Info("timer_set", "id", id, "seconds", seconds)
	b.notify()
	return t, nil
}

// CancelAlarm removes the alarm with id; false when there is none. A cancel stands even if the file cannot be
// written, as every cancel does: the alarm would only come back at a restart.
func (b *Book) CancelAlarm(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.removeAlarms(func(a Alarm) bool { return a.ID == id }, func(s Snooze) bool { return s.ID == id })) > 0
}

// CancelAlarmsAt removes every alarm at hour:minute, whatever its days, and names them.
func (b *Book) CancelAlarmsAt(hour, minute int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.removeAlarms(func(a Alarm) bool { return a.Hour == hour && a.Minute == minute }, nil)
}

func (b *Book) CancelAllAlarms() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.removeAlarms(func(Alarm) bool { return true }, func(Snooze) bool { return true })
}

func (b *Book) CancelTimer(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.removeTimers(func(t Timer) bool { return t.ID == id }, func(s Snooze) bool { return s.ID == id })) > 0
}

// CancelTimerFor removes the timer set for seconds that ends first.
func (b *Book) CancelTimerFor(seconds int) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.removeFirstTimer(func(t Timer) bool { return t.Seconds == seconds })
}

// CancelNextTimer removes the timer that ends first.
func (b *Book) CancelNextTimer() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.removeFirstTimer(func(Timer) bool { return true })
}

func (b *Book) CancelAllTimers() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.removeTimers(func(Timer) bool { return true }, func(Snooze) bool { return true })
}

// List is what the book holds, in the order set.
func (b *Book) List() ([]Alarm, []Timer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	alarms := make([]Alarm, 0, len(b.alarms))
	for _, e := range b.alarms {
		alarms = append(alarms, e.alarm)
	}
	return alarms, append(make([]Timer, 0, len(b.timers)), b.timers...)
}

// Armed is whether any alarm is set.
func (b *Book) Armed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.alarms) > 0
}

func (b *Book) NearestTimerEnd() (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := firstToEnd(b.timers, func(Timer) bool { return true })
	return t.EndsAt, ok
}

// TakeDue hands out everything due at now, marked as rung and saved first: timers in the order they end, then
// alarms by occurrence, the first set among equals. An alarm is due from its occurrence for LateWindow if it has not
// rung for it, a timer from its end for as long; later than that it is dropped without a ring and logged as
// ring_missed. Alarms wait for a zone and a plausible clock, timers do not.
func (b *Book) TakeDue(now time.Time) []Ring {
	now = wall(now)
	b.mu.Lock()
	defer b.mu.Unlock()
	changed := b.dropLate(now)
	var rings []Ring
	for {
		r, ok := b.takeOne(now)
		if !ok {
			break
		}
		rings = append(rings, r)
	}
	if !changed && len(rings) == 0 {
		return nil
	}
	b.counts.Rings += len(rings)
	// a mark that cannot be saved still counts in memory; persist has logged it
	_ = b.persist()
	b.notify()
	return rings
}

// NextTick is how long until the next due second, at most a minute; zero when something is due now.
func (b *Book) NextTick(now time.Time) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	due, ok := b.nextDue(wall(now))
	if !ok {
		return maxTick
	}
	return min(max(due.Sub(now), 0), maxTick)
}

func (b *Book) Counts() Counts {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.counts
}

func (b *Book) notify() {
	for _, ch := range b.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (b *Book) refuse(kind Kind, err *RefusedError, id string) error {
	b.counts.Refusals++
	attrs := []any{"kind", string(kind), "reason", err.Code}
	// an id the book would not hold may be anything
	if ValidID(id) {
		attrs = append(attrs, "id", id)
	}
	b.log.Warn("alarm_refused", attrs...)
	return err
}

func (b *Book) persist() error {
	err := save(b.path, stored{alarms: b.alarms, timers: b.timers, reminders: b.reminders, snoozed: b.snoozed, last: b.last})
	if err != nil {
		// the cause quotes the data dir
		var errno syscall.Errno
		if errors.As(err, &errno) {
			b.log.Error("alarms_store_failed", "code", int(errno))
		} else {
			b.log.Error("alarms_store_failed")
		}
	}
	return err
}

// removeAlarms removes the matching alarms and the snoozes of them, and the snoozes snoozed matches besides (one of a
// one-time alarm that has gone); nil matches none of those
func (b *Book) removeAlarms(match func(Alarm) bool, snoozed func(Snooze) bool) []string {
	var gone []string
	b.alarms = slices.DeleteFunc(b.alarms, func(e storedAlarm) bool {
		if match(e.alarm) {
			gone = append(gone, e.alarm.ID)
			return true
		}
		return false
	})
	for _, id := range gone {
		b.log.Info("alarm_cancelled", "id", id)
	}
	return b.removed(KindAlarm, gone, snoozed)
}

func (b *Book) removeTimers(match func(Timer) bool, snoozed func(Snooze) bool) []string {
	var gone []string
	b.timers = slices.DeleteFunc(b.timers, func(t Timer) bool {
		if match(t) {
			gone = append(gone, t.ID)
			return true
		}
		return false
	})
	for _, id := range gone {
		b.log.Info("timer_cancelled", "id", id)
	}
	return b.removed(KindTimer, gone, snoozed)
}

// removed drops the snoozes of kind whose id went, or that snoozed matches, and names every id cancelled once
func (b *Book) removed(kind Kind, gone []string, snoozed func(Snooze) bool) []string {
	b.snoozed = slices.DeleteFunc(b.snoozed, func(s Snooze) bool {
		if s.Kind != kind || !slices.Contains(gone, s.ID) && (snoozed == nil || !snoozed(s)) {
			return false
		}
		b.log.Info("snooze_cancelled", "kind", string(kind), "id", s.ID)
		if !slices.Contains(gone, s.ID) {
			gone = append(gone, s.ID)
		}
		return true
	})
	b.cancelled(len(gone))
	return gone
}

func (b *Book) removeFirstTimer(match func(Timer) bool) (string, bool) {
	first, ok := firstToEnd(b.timers, match)
	if !ok {
		return "", false
	}
	b.removeTimers(func(t Timer) bool { return t.ID == first.ID }, nil)
	return first.ID, true
}

func (b *Book) cancelled(n int) {
	if n == 0 {
		return
	}
	b.counts.Cancels += n
	_ = b.persist()
	b.notify()
}

// alarmsLive is a zone set and a plausible clock: until a clock is set no date is told from a past one, and until a
// zone is set no occurrence is known. timers run on any clock in any zone
func (b *Book) alarmsLive(now time.Time) bool { return b.zoneSet && plausible(now, b.loc) }

// dropLate drops a timer that ended more than LateWindow ago, and a spent one-time alarm while alarms are live
func (b *Book) dropLate(now time.Time) bool {
	before := b.counts.Missed
	b.timers = slices.DeleteFunc(b.timers, func(t Timer) bool {
		if now.Sub(t.EndsAt) <= LateWindow {
			return false
		}
		b.missed(KindTimer, t.ID)
		return true
	})
	b.snoozed = slices.DeleteFunc(b.snoozed, func(s Snooze) bool {
		if now.Sub(s.Until) <= LateWindow {
			return false
		}
		b.missed(s.Kind, s.ID)
		return true
	})
	if b.alarmsLive(now) {
		b.alarms = slices.DeleteFunc(b.alarms, func(e storedAlarm) bool {
			if !b.spent(e, now) {
				return false
			}
			b.missed(KindAlarm, e.alarm.ID)
			return true
		})
	}
	b.dropLateReminders(now)
	return b.counts.Missed != before
}

func (b *Book) missed(kind Kind, id string) {
	b.counts.Missed++
	b.log.Warn("ring_missed", "kind", string(kind), "id", id)
}

// spent is a one-time alarm that will not ring again: it has, its late window is over, or its day lacks the minute
// and is over too. a repeating alarm is never spent: it has a next day, or waits for one
func (b *Book) spent(e storedAlarm, now time.Time) bool {
	a := e.alarm
	if a.Days != 0 {
		return false
	}
	if last, ok := lastRing(a, now, b.loc); ok {
		return (e.rung && !last.After(e.rungAt)) || now.Sub(last) > LateWindow
	}
	if _, ok := nextRing(a, now, b.loc); ok {
		return false
	}
	return a.Date.before(dateOf(now, b.loc))
}

// dueOccurrence is the occurrence an alarm is owed a ring for: it has come, it has not rung, and it is not that late
func (b *Book) dueOccurrence(e storedAlarm, now time.Time) (time.Time, bool) {
	last, ok := lastRing(e.alarm, now, b.loc)
	if !ok || (e.rung && !last.After(e.rungAt)) || now.Sub(last) > LateWindow {
		return time.Time{}, false
	}
	return last, true
}

func (b *Book) takeOne(now time.Time) (Ring, bool) {
	if t, ok := firstToEnd(b.timers, func(t Timer) bool { return !t.EndsAt.After(now) }); ok {
		b.timers = slices.DeleteFunc(b.timers, func(e Timer) bool { return e.ID == t.ID })
		return Ring{Kind: KindTimer, ID: t.ID, At: t.EndsAt}, true
	}
	// a snooze is a moment, not a calendar's: it rings without a zone too
	if i := firstSnooze(b.snoozed, now); i >= 0 {
		s := b.snoozed[i]
		b.snoozed = slices.Delete(b.snoozed, i, i+1)
		r := s.Ring
		r.At = s.Until
		return r, true
	}
	// a reminder at an instant rings without a zone, as a snooze does
	if r, ok := b.takeReminder(now); ok {
		return r, true
	}
	if !b.alarmsLive(now) {
		return Ring{}, false
	}
	first := -1
	var at time.Time
	for i, e := range b.alarms {
		if due, ok := b.dueOccurrence(e, now); ok && (first < 0 || due.Before(at)) {
			first, at = i, due
		}
	}
	if first < 0 {
		return Ring{}, false
	}
	a := b.alarms[first].alarm
	if a.Days == 0 {
		b.alarms = slices.Delete(b.alarms, first, first+1)
	} else {
		b.alarms[first].rungAt, b.alarms[first].rung = at, true
	}
	return Ring{Kind: KindAlarm, ID: a.ID, Level: a.Level, At: at, Options: a.Options, SoundID: a.SoundID}, true
}

// nextDue is when TakeDue next has something: at or before now when it has something now
func (b *Book) nextDue(now time.Time) (time.Time, bool) {
	var soonest time.Time
	found := false
	consider := func(t time.Time) {
		if !found || t.Before(soonest) {
			soonest, found = t, true
		}
	}
	if t, ok := firstToEnd(b.timers, func(Timer) bool { return true }); ok {
		consider(t.EndsAt)
	}
	for _, s := range b.snoozed {
		consider(s.Until)
	}
	b.nextReminder(now, consider)
	if !b.alarmsLive(now) {
		return soonest, found
	}
	for _, e := range b.alarms {
		if t, ok := b.dueOccurrence(e, now); ok {
			consider(t)
		} else if t, ok := nextRing(e.alarm, now, b.loc); ok {
			consider(t)
		}
	}
	return soonest, found
}

// firstToEnd is the matching timer that ends first, the first set among equals
func firstToEnd(timers []Timer, match func(Timer) bool) (Timer, bool) {
	var first Timer
	found := false
	for _, t := range timers {
		if match(t) && (!found || t.EndsAt.Before(first.EndsAt)) {
			first, found = t, true
		}
	}
	return first, found
}

// firstSnooze is the index of the snooze due at now that comes first, -1 for none
func firstSnooze(snoozed []Snooze, now time.Time) int {
	first := -1
	for i, s := range snoozed {
		if !s.Until.After(now) && (first < 0 || s.Until.Before(snoozed[first].Until)) {
			first = i
		}
	}
	return first
}

// twins are one alarm under two ids: the same time, days and date
func twins(x, y Alarm) bool {
	return x.ID != y.ID && x.Hour == y.Hour && x.Minute == y.Minute && x.Days == y.Days && x.Date == y.Date
}
