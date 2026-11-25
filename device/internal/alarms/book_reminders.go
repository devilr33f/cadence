package alarms

import (
	"slices"
	"time"
)

// storedReminder carries the latest occurrence the reminder is done with, as storedAlarm does
type storedReminder struct {
	reminder Reminder
	rungAt   time.Time
	rung     bool
}

// reminderNext is the reminder's first occurrence strictly after `after`: its instant, or its calendar's
func reminderNext(r Reminder, after time.Time, loc *time.Location) (time.Time, bool) {
	if !r.At.IsZero() {
		return r.At, r.At.After(after)
	}
	return nextRing(r.schedule(), after, loc)
}

// reminderLast is its latest occurrence at or before now
func reminderLast(r Reminder, now time.Time, loc *time.Location) (time.Time, bool) {
	if !r.At.IsZero() {
		return r.At, !r.At.After(now)
	}
	return lastRing(r.schedule(), now, loc)
}

// SetReminder stores r in place of the reminder with its id. A one-time reminder at a time without a date gets the
// day of its next occurrence. Refused as SetAlarm is: ErrPast for one with no occurrence after now once the clock is
// plausible, ErrFull past MaxReminders, ErrStore, ErrInvalid, and ErrNoZone for one by the calendar before a zone is
// set (one at an instant needs none).
func (b *Book) SetReminder(r Reminder) (Reminder, error) {
	now := wall(b.clock.Now())
	b.mu.Lock()
	defer b.mu.Unlock()
	if !r.valid() {
		return Reminder{}, b.refuse(KindReminder, ErrInvalid, r.ID)
	}
	if r.At.IsZero() {
		if !b.zoneSet {
			return Reminder{}, b.refuse(KindReminder, ErrNoZone, r.ID)
		}
		if r.Days == 0 && r.Date.IsZero() {
			d, ok := nextDate(r.Hour, r.Minute, now, b.loc)
			if !ok {
				return Reminder{}, b.refuse(KindReminder, ErrPast, r.ID)
			}
			r.Date = d
		}
	}
	r.At = r.At.UTC()
	if _, ok := reminderNext(r, now, b.loc); !ok && r.Days == 0 && plausible(now, b.loc) {
		return Reminder{}, b.refuse(KindReminder, ErrPast, r.ID)
	}
	s := storedReminder{reminder: r}
	s.rungAt, s.rung = reminderLast(r, now, b.loc)
	if !r.At.IsZero() {
		// an instant ahead has rung for nothing yet
		s.rung = false
	}
	previous := slices.Clone(b.reminders)
	i := slices.IndexFunc(b.reminders, func(e storedReminder) bool { return e.reminder.ID == r.ID })
	switch {
	case i >= 0:
		b.reminders[i] = s
	case len(b.reminders) >= MaxReminders:
		return Reminder{}, b.refuse(KindReminder, ErrFull, r.ID)
	default:
		b.reminders = append(b.reminders, s)
	}
	if b.persist() != nil {
		b.reminders = previous
		return Reminder{}, b.refuse(KindReminder, ErrStore, r.ID)
	}
	b.counts.Sets++
	// the label is the user's text: never logged
	b.log.Info("reminder_set", "id", r.ID, "sound", r.SoundID != "")
	b.notify()
	return r, nil
}

// CancelReminder removes the reminder with id and its snooze; false when there is none.
func (b *Book) CancelReminder(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.removeReminders(func(r Reminder) bool { return r.ID == id }, func(s Snooze) bool { return s.ID == id })) > 0
}

func (b *Book) CancelAllReminders() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.removeReminders(func(Reminder) bool { return true }, func(Snooze) bool { return true })
}

// Reminders lists the reminders, in the order set.
func (b *Book) Reminders() []Reminder {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Reminder, 0, len(b.reminders))
	for _, e := range b.reminders {
		out = append(out, e.reminder)
	}
	return out
}

// NextOfReminder is when the reminder next rings, a snooze of it included; false while its calendar is unknown and it
// has no snooze.
func (b *Book) NextOfReminder(r Reminder) (time.Time, bool) {
	now := wall(b.clock.Now())
	b.mu.Lock()
	defer b.mu.Unlock()
	var next time.Time
	found := false
	if !r.At.IsZero() || b.alarmsLive(now) {
		next, found = reminderNext(r, now, b.loc)
		if i := slices.IndexFunc(b.reminders, func(e storedReminder) bool { return e.reminder.ID == r.ID }); i >= 0 {
			if due, ok := b.dueReminder(b.reminders[i], now); ok {
				next, found = due, true
			}
		}
	}
	for _, s := range b.snoozed {
		if s.Kind == KindReminder && s.ID == r.ID && (!found || s.Until.Before(next)) {
			next, found = s.Until, true
		}
	}
	return next, found
}

// removeReminders removes the matching reminders and their snoozes. callers hold mu
func (b *Book) removeReminders(match func(Reminder) bool, snoozed func(Snooze) bool) []string {
	var gone []string
	b.reminders = slices.DeleteFunc(b.reminders, func(e storedReminder) bool {
		if match(e.reminder) {
			gone = append(gone, e.reminder.ID)
			return true
		}
		return false
	})
	for _, id := range gone {
		b.log.Info("reminder_cancelled", "id", id)
	}
	return b.removed(KindReminder, gone, snoozed)
}

// reminderLive is whether the reminder's occurrences are known now: an instant always, the calendar's once alarms are
// live. callers hold mu
func (b *Book) reminderLive(r Reminder, now time.Time) bool {
	return !r.At.IsZero() || b.alarmsLive(now)
}

// dueReminder is the occurrence a reminder is owed a ring for, as dueOccurrence is an alarm's. callers hold mu
func (b *Book) dueReminder(e storedReminder, now time.Time) (time.Time, bool) {
	if !b.reminderLive(e.reminder, now) {
		return time.Time{}, false
	}
	last, ok := reminderLast(e.reminder, now, b.loc)
	if !ok || (e.rung && !last.After(e.rungAt)) || now.Sub(last) > LateWindow {
		return time.Time{}, false
	}
	return last, true
}

// spentReminder is a one-time reminder that will not ring again. callers hold mu
func (b *Book) spentReminder(e storedReminder, now time.Time) bool {
	r := e.reminder
	if r.Days != 0 || !b.reminderLive(r, now) {
		return false
	}
	if last, ok := reminderLast(r, now, b.loc); ok {
		return (e.rung && !last.After(e.rungAt)) || now.Sub(last) > LateWindow
	}
	if _, ok := reminderNext(r, now, b.loc); ok {
		return false
	}
	return r.At.IsZero() && r.Date.before(dateOf(now, b.loc))
}

// dropLateReminders drops the spent one-time reminders as ring_missed. callers hold mu
func (b *Book) dropLateReminders(now time.Time) {
	b.reminders = slices.DeleteFunc(b.reminders, func(e storedReminder) bool {
		if !b.spentReminder(e, now) {
			return false
		}
		b.missed(KindReminder, e.reminder.ID)
		return true
	})
}

// takeReminder hands out the reminder due first, marked as rung, a one-time one removed. callers hold mu
func (b *Book) takeReminder(now time.Time) (Ring, bool) {
	first := -1
	var at time.Time
	for i, e := range b.reminders {
		if due, ok := b.dueReminder(e, now); ok && (first < 0 || due.Before(at)) {
			first, at = i, due
		}
	}
	if first < 0 {
		return Ring{}, false
	}
	r := b.reminders[first].reminder
	if r.Days == 0 {
		b.reminders = slices.Delete(b.reminders, first, first+1)
	} else {
		b.reminders[first].rungAt, b.reminders[first].rung = at, true
	}
	return Ring{Kind: KindReminder, ID: r.ID, Level: r.Level, At: at, SoundID: r.SoundID}, true
}

// nextReminder is when the reminders next have something for TakeDue. callers hold mu
func (b *Book) nextReminder(now time.Time, consider func(time.Time)) {
	for _, e := range b.reminders {
		if !b.reminderLive(e.reminder, now) {
			continue
		}
		if t, ok := b.dueReminder(e, now); ok {
			consider(t)
		} else if t, ok := reminderNext(e.reminder, now, b.loc); ok {
			consider(t)
		}
	}
}
