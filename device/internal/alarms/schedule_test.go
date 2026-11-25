package alarms

import (
	"testing"
	"time"

	// the zones the tests name, whatever the host has
	_ "time/tzdata"
)

// Asia/Bishkek is +06:00 all year; 2026-10-26 is a Monday. Europe/Berlin moves its clocks on 2026-03-29 (02:00 ->
// 03:00) and 2026-10-25 (03:00 -> 02:00).
var (
	bishkek = mustZone("Asia/Bishkek")
	berlin  = mustZone("Europe/Berlin")
)

const (
	mondays           Days = 0x01
	weekdays          Days = 0x1F
	sundays           Days = 0x40
	sundaysAndMondays Days = 0x41
	day                    = 24 * time.Hour
)

func mustZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func at(year int, month time.Month, d, hour, minute, second int) time.Time {
	return time.Date(year, month, d, hour, minute, second, 0, bishkek)
}

func utc(year int, month time.Month, d, hour, minute int) time.Time {
	return time.Date(year, month, d, hour, minute, 0, 0, time.UTC)
}

func repeating(hour, minute int, days Days) Alarm {
	return Alarm{ID: "a1", Hour: hour, Minute: minute, Days: days, Level: 50}
}

func once(hour, minute int, date Date) Alarm {
	return Alarm{ID: "a1", Hour: hour, Minute: minute, Date: date, Level: 50}
}

func sameMoment(got time.Time, ok bool, want time.Time, wantOK bool) bool {
	return ok == wantOK && (!ok || got.Equal(want))
}

func TestPlausibleFromTheLocalYear2026(t *testing.T) {
	for _, c := range []struct {
		now  time.Time
		loc  *time.Location
		want bool
	}{
		{at(2025, 12, 31, 23, 59, 59), bishkek, false},
		{at(2026, 1, 1, 0, 0, 0), bishkek, true},
		{at(2026, 10, 26, 12, 0, 0), bishkek, true},
		{time.Unix(0, 0), bishkek, false},
		// 2025-12-31 20:00 UTC is already 2026 in Bishkek
		{utc(2025, 12, 31, 20, 0), bishkek, true},
		{utc(2025, 12, 31, 20, 0), time.UTC, false},
	} {
		if got := plausible(c.now, c.loc); got != c.want {
			t.Errorf("plausible(%v in %v) = %v", c.now, c.loc, got)
		}
	}
}

func TestNextDateIsTheDayOfTheFirstTimeStrictlyAfter(t *testing.T) {
	for _, c := range []struct {
		name         string
		hour, minute int
		after        time.Time
		want         Date
	}{
		{"late evening rolls over to tomorrow", 7, 0, at(2026, 10, 26, 22, 30, 0), Date{2026, 10, 27}},
		{"a second before is still today", 7, 0, at(2026, 10, 26, 6, 59, 59), Date{2026, 10, 26}},
		{"the very second is not after", 7, 0, at(2026, 10, 26, 7, 0, 0), Date{2026, 10, 27}},
		{"small hours are still the local date", 7, 0, at(2026, 10, 26, 3, 0, 0), Date{2026, 10, 26}},
		{"a midnight alarm is tomorrow's", 0, 0, at(2026, 10, 26, 23, 59, 0), Date{2026, 10, 27}},
		{"the end of a month", 7, 0, at(2026, 10, 31, 22, 0, 0), Date{2026, 11, 1}},
		{"the end of a year", 7, 0, at(2026, 12, 31, 22, 0, 0), Date{2027, 1, 1}},
		{"a leap day", 7, 0, at(2028, 2, 28, 22, 0, 0), Date{2028, 2, 29}},
	} {
		if got, ok := nextDate(c.hour, c.minute, c.after, bishkek); !ok || got != c.want {
			t.Errorf("%s: nextDate = %v, %v; want %v", c.name, got, ok, c.want)
		}
	}
	// Monday 23:30 in New York is already Tuesday in UTC
	ny := mustZone("America/New_York")
	if got, _ := nextDate(7, 0, time.Date(2026, 10, 26, 23, 30, 0, 0, ny), ny); got != (Date{2026, 10, 27}) {
		t.Errorf("new york: %v", got)
	}
}

func TestNextRing(t *testing.T) {
	tuesday := once(7, 0, Date{2026, 10, 27})
	for _, c := range []struct {
		name   string
		alarm  Alarm
		after  time.Time
		want   time.Time
		wantOK bool
	}{
		{"weekdays from Friday after the time", repeating(7, 0, weekdays), at(2026, 10, 30, 8, 0, 0), at(2026, 11, 2, 7, 0, 0), true},
		{"weekdays from Friday before the time", repeating(7, 0, weekdays), at(2026, 10, 30, 6, 0, 0), at(2026, 10, 30, 7, 0, 0), true},
		{"weekdays from Sunday night", repeating(7, 0, weekdays), at(2026, 11, 1, 23, 59, 0), at(2026, 11, 2, 7, 0, 0), true},
		{"Sunday only on the very second", repeating(7, 0, sundays), at(2026, 11, 1, 7, 0, 0), at(2026, 11, 8, 7, 0, 0), true},
		{"Sunday only from Monday", repeating(7, 0, sundays), at(2026, 10, 26, 12, 0, 0), at(2026, 11, 1, 7, 0, 0), true},
		{"Monday only after the time", repeating(7, 0, mondays), at(2026, 10, 26, 8, 0, 0), at(2026, 11, 2, 7, 0, 0), true},
		{"Sunday rolls into Monday", repeating(7, 0, sundaysAndMondays), at(2026, 11, 1, 7, 30, 0), at(2026, 11, 2, 7, 0, 0), true},
		{"every day on the very second", repeating(7, 0, EveryDay), at(2026, 10, 26, 7, 0, 0), at(2026, 10, 27, 7, 0, 0), true},
		{"over the end of a year", repeating(7, 0, EveryDay), at(2026, 12, 31, 8, 0, 0), at(2027, 1, 1, 7, 0, 0), true},
		{"one-time the evening before", tuesday, at(2026, 10, 26, 22, 30, 0), at(2026, 10, 27, 7, 0, 0), true},
		{"one-time on the very second", tuesday, at(2026, 10, 27, 7, 0, 0), time.Time{}, false},
		{"one-time the day after", tuesday, at(2026, 10, 28, 7, 0, 0), time.Time{}, false},
		{"one-time months ahead", once(7, 0, Date{2027, 3, 1}), at(2026, 10, 26, 12, 0, 0), at(2027, 3, 1, 7, 0, 0), true},
		{"a date that is no day", once(7, 0, Date{2026, 2, 30}), at(2026, 1, 1, 0, 0, 0), time.Time{}, false},
		{"no date", once(7, 0, Date{}), at(2026, 10, 26, 22, 30, 0), time.Time{}, false},
	} {
		if got, ok := nextRing(c.alarm, c.after, bishkek); !sameMoment(got, ok, c.want, c.wantOK) {
			t.Errorf("%s: nextRing = %v, %v; want %v, %v", c.name, got, ok, c.want, c.wantOK)
		}
	}
}

func TestLastRingIsTheLatestOccurrenceNotAfterNow(t *testing.T) {
	tuesday := once(7, 0, Date{2026, 10, 27})
	for _, c := range []struct {
		name   string
		alarm  Alarm
		now    time.Time
		want   time.Time
		wantOK bool
	}{
		{"weekdays on Saturday", repeating(7, 0, weekdays), at(2026, 10, 31, 10, 0, 0), at(2026, 10, 30, 7, 0, 0), true},
		{"weekdays on Monday before the time", repeating(7, 0, weekdays), at(2026, 10, 26, 6, 0, 0), at(2026, 10, 23, 7, 0, 0), true},
		{"weekdays on Monday at the time", repeating(7, 0, weekdays), at(2026, 10, 26, 7, 0, 0), at(2026, 10, 26, 7, 0, 0), true},
		{"Sunday only a week back", repeating(7, 0, sundays), at(2026, 11, 1, 6, 0, 0), at(2026, 10, 25, 7, 0, 0), true},
		{"over the start of a year", repeating(7, 0, EveryDay), at(2027, 1, 1, 6, 0, 0), at(2026, 12, 31, 7, 0, 0), true},
		{"one-time a second before", tuesday, at(2026, 10, 27, 6, 59, 59), time.Time{}, false},
		{"one-time on the very second", tuesday, at(2026, 10, 27, 7, 0, 0), at(2026, 10, 27, 7, 0, 0), true},
		{"one-time a month after", tuesday, at(2026, 11, 27, 7, 0, 0), at(2026, 10, 27, 7, 0, 0), true},
	} {
		if got, ok := lastRing(c.alarm, c.now, bishkek); !sameMoment(got, ok, c.want, c.wantOK) {
			t.Errorf("%s: lastRing = %v, %v; want %v, %v", c.name, got, ok, c.want, c.wantOK)
		}
	}
}

func TestNextRingIsTheNextOccurrenceWheneverAsked(t *testing.T) {
	start := at(2026, 10, 26, 0, 0, 0)
	for _, a := range []Alarm{repeating(7, 0, weekdays), repeating(23, 59, sundays), repeating(0, 0, EveryDay),
		repeating(7, 30, sundaysAndMondays), repeating(2, 30, EveryDay)} {
		for _, loc := range []*time.Location{bishkek, berlin} {
			for after := start; after.Before(start.Add(21 * day)); after = after.Add(5*time.Hour + time.Second) {
				next, ok := nextRing(a, after, loc)
				if !ok || !next.After(after) || next.Sub(after) > walkDays*day {
					t.Fatalf("%v in %v after %v: next = %v, %v", a, loc, after, next, ok)
				}
				if last, ok := lastRing(a, next, loc); !ok || !last.Equal(next) {
					t.Fatalf("%v: lastRing(next) = %v, %v", a, last, ok)
				}
				// nothing came between
				before, ok := lastRing(a, next.Add(-time.Second), loc)
				if !ok || !before.Before(next) {
					t.Fatalf("%v: lastRing before next = %v, %v", a, before, ok)
				}
				if again, ok := nextRing(a, before, loc); !ok || !again.Equal(next) {
					t.Fatalf("%v: nextRing(before) = %v, want %v", a, again, next)
				}
			}
		}
	}
}

func TestFromLocalSkipsAMinuteTheClocksSkipAndTakesTheEarlierOfARepeatedOne(t *testing.T) {
	if got, ok := fromLocal(berlin, Date{2026, 3, 29}, 2, 30); ok {
		t.Errorf("02:30 on the spring day = %v", got)
	}
	if got, _ := fromLocal(berlin, Date{2026, 3, 29}, 1, 59); !got.Equal(utc(2026, 3, 29, 0, 59)) {
		t.Errorf("01:59 = %v", got)
	}
	if got, _ := fromLocal(berlin, Date{2026, 3, 29}, 3, 0); !got.Equal(utc(2026, 3, 29, 1, 0)) {
		t.Errorf("03:00 = %v", got)
	}
	// 02:30 summer time is 00:30 UTC; 02:30 standard time, an hour later, is not the answer
	if got, _ := fromLocal(berlin, Date{2026, 10, 25}, 2, 30); !got.Equal(utc(2026, 10, 25, 0, 30)) {
		t.Errorf("repeated 02:30 = %v", got)
	}
	if got, _ := fromLocal(bishkek, Date{2026, 10, 26}, 18, 30); !got.Equal(utc(2026, 10, 26, 12, 30)) {
		t.Errorf("bishkek 18:30 = %v", got)
	}
	for _, d := range []Date{{2026, 2, 30}, {2026, 4, 31}, {2026, 13, 1}, {2026, 0, 10}, {2026, 5, 0}, {2026, 2, 29}} {
		if got, ok := fromLocal(bishkek, d, 7, 0); ok {
			t.Errorf("%v is no day, got %v", d, got)
		}
	}
}

func TestADayWithoutTheMinuteIsPassedOver(t *testing.T) {
	saturdayNight := time.Date(2026, 3, 28, 23, 0, 0, 0, berlin)
	daily := repeating(2, 30, EveryDay)

	if got, _ := nextDate(2, 30, saturdayNight, berlin); got != (Date{2026, 3, 30}) {
		t.Errorf("nextDate = %v", got)
	}
	if got, _ := nextRing(daily, saturdayNight, berlin); !got.Equal(time.Date(2026, 3, 30, 2, 30, 0, 0, berlin)) {
		t.Errorf("nextRing = %v", got)
	}
	if got, _ := lastRing(daily, time.Date(2026, 3, 29, 10, 0, 0, 0, berlin), berlin); !got.Equal(time.Date(2026, 3, 28, 2, 30, 0, 0, berlin)) {
		t.Errorf("lastRing = %v", got)
	}
	// Saturday to the Sunday after next: eight days ahead
	if got, _ := nextRing(repeating(2, 30, sundays), time.Date(2026, 3, 28, 12, 0, 0, 0, berlin), berlin); !got.Equal(time.Date(2026, 4, 5, 2, 30, 0, 0, berlin)) {
		t.Errorf("weekly = %v", got)
	}
	gap := once(2, 30, Date{2026, 3, 29})
	if got, ok := nextRing(gap, saturdayNight, berlin); ok {
		t.Errorf("one-time on the skipped minute rings at %v", got)
	}
	if got, ok := lastRing(gap, time.Date(2026, 3, 30, 12, 0, 0, 0, berlin), berlin); ok {
		t.Errorf("one-time on the skipped minute rang at %v", got)
	}
}

func TestARepeatedMinuteRingsOnceADay(t *testing.T) {
	daily := repeating(2, 30, EveryDay)
	first, ok := nextRing(daily, time.Date(2026, 10, 24, 12, 0, 0, 0, berlin), berlin)
	if !ok || !first.Equal(utc(2026, 10, 25, 0, 30)) {
		t.Fatalf("first = %v, %v", first, ok)
	}
	if got, _ := nextRing(daily, first, berlin); !got.Equal(time.Date(2026, 10, 26, 2, 30, 0, 0, berlin)) {
		t.Errorf("after the first = %v", got)
	}
	// the second 02:30 is no occurrence of its own
	if got, _ := lastRing(daily, utc(2026, 10, 25, 1, 45), berlin); !got.Equal(first) {
		t.Errorf("lastRing in the second 02:30 hour = %v", got)
	}
}
