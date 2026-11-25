package alarms

import "time"

// a week and the day a clock set forward may take out of it
const walkDays = 8

// plausible is a clock the network has set: until then no alarm rings or goes, timers run on any clock
func plausible(now time.Time, loc *time.Location) bool {
	return now.In(loc).Year() >= firstPlausibleYear
}

// nextDate is the day of the first hour:minute strictly after `after`
func nextDate(hour, minute int, after time.Time, loc *time.Location) (Date, bool) {
	d, _, ok := firstAfter(hour, minute, EveryDay, after, loc)
	return d, ok
}

// nextRing is the first occurrence strictly after `after`; a one-time alarm has the one, on its date
func nextRing(a Alarm, after time.Time, loc *time.Location) (time.Time, bool) {
	if a.Days == 0 {
		t, ok := pinned(a, loc)
		if !ok || !t.After(after) {
			return time.Time{}, false
		}
		return t, true
	}
	_, t, ok := firstAfter(a.Hour, a.Minute, a.Days, after, loc)
	return t, ok
}

// lastRing is the latest occurrence at or before now
func lastRing(a Alarm, now time.Time, loc *time.Location) (time.Time, bool) {
	if a.Days == 0 {
		t, ok := pinned(a, loc)
		if !ok || t.After(now) {
			return time.Time{}, false
		}
		return t, true
	}
	today := dateOf(now, loc)
	for behind := range walkDays + 1 {
		d := today.addDays(-behind)
		if !a.Days.keeps(d.weekday()) {
			continue
		}
		if t, ok := fromLocal(loc, d, a.Hour, a.Minute); ok && !t.After(now) {
			return t, true
		}
	}
	return time.Time{}, false
}

// firstAfter passes over a day that lacks the minute: it has no occurrence
func firstAfter(hour, minute int, days Days, after time.Time, loc *time.Location) (Date, time.Time, bool) {
	today := dateOf(after, loc)
	for ahead := range walkDays + 1 {
		d := today.addDays(ahead)
		if !days.keeps(d.weekday()) {
			continue
		}
		if t, ok := fromLocal(loc, d, hour, minute); ok && t.After(after) {
			return d, t, true
		}
	}
	return Date{}, time.Time{}, false
}

func pinned(a Alarm, loc *time.Location) (time.Time, bool) {
	if a.Date.IsZero() {
		return time.Time{}, false
	}
	return fromLocal(loc, a.Date, a.Hour, a.Minute)
}
