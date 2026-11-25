package alarms

import (
	"time"
)

// Clock reads the wall time; alarms are set and ring by it.
type Clock interface{ Now() time.Time }

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

// loadZone is an IANA zone; "" is UTC. Local is refused: it is whatever the host was set to, and the zone is the
// owner's setting, not the device's.
func loadZone(name string) (*time.Location, error) {
	if name == "Local" {
		return nil, ErrZone
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		// the cause quotes the name
		return nil, ErrZone
	}
	return loc, nil
}

// wall is t in whole seconds without its monotonic reading: the book counts wall seconds, as the clock reads them
// after it was set, and a timer that outlives a restart cannot be timed otherwise
func wall(t time.Time) time.Time { return t.Truncate(time.Second) }

func dateOf(t time.Time, loc *time.Location) Date {
	y, m, d := t.In(loc).Date()
	return Date{y, m, d}
}

// noon keeps date arithmetic clear of any zone's midnight
func (d Date) noon() time.Time { return time.Date(d.Year, d.Month, d.Day, 12, 0, 0, 0, time.UTC) }

func (d Date) addDays(n int) Date {
	y, m, day := d.noon().AddDate(0, 0, n).Date()
	return Date{y, m, day}
}

func (d Date) weekday() time.Weekday { return d.noon().Weekday() }

func (d Date) before(o Date) bool {
	if d.Year != o.Year {
		return d.Year < o.Year
	}
	if d.Month != o.Month {
		return d.Month < o.Month
	}
	return d.Day < o.Day
}

// fromLocal is the moment hour:minute on the date reads in loc. false for a date that is no day or a minute the zone
// skips (clocks set forward over it); of a minute it has twice (clocks set back), the earlier, so it rings once. a
// zone moves its clocks at most once around any minute, so the offsets a day before and after are the two it can
// have
func fromLocal(loc *time.Location, d Date, hour, minute int) (time.Time, bool) {
	if !d.valid() {
		return time.Time{}, false
	}
	asUTC := time.Date(d.Year, d.Month, d.Day, hour, minute, 0, 0, time.UTC)
	var best time.Time
	found := false
	for _, probe := range [...]time.Duration{-24 * time.Hour, 0, 24 * time.Hour} {
		_, offset := asUTC.Add(probe).In(loc).Zone()
		t := asUTC.Add(-time.Duration(offset) * time.Second)
		if readsAs(t.In(loc), asUTC) && (!found || t.Before(best)) {
			best, found = t, true
		}
	}
	return best, found
}

func readsAs(local, want time.Time) bool {
	ly, lm, ld := local.Date()
	wy, wm, wd := want.Date()
	return ly == wy && lm == wm && ld == wd && local.Hour() == want.Hour() && local.Minute() == want.Minute() &&
		local.Second() == 0
}
