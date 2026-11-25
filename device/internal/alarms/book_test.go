package alarms_test

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/alarms"
)

func wantErr(t *testing.T, what string, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Errorf("%s: err = %v, want %v", what, got, want)
	}
}

// blockStore puts a file where the state directory was, so every save fails (a read-only directory would not stop root)
func blockStore(t *testing.T, f *fixture) {
	t.Helper()
	dir := filepath.Dir(f.path)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSettingAnIDAgainReplacesItsAlarmInPlaceAndOnlyANewIDMeetsTheLimit(t *testing.T) {
	f := newFixture(t)
	now := at(26, 6, 0, 0)
	b := f.open(now)
	f.mustSetAlarm(b, now, alarm("a0", 7, 0, weekdays))
	f.mustSetAlarm(b, now, alarm("a1", 8, 0, weekdays))

	again := alarm("a0", 9, 30, mondays)
	again.Level = 80
	f.clock.set(now)
	stored, replaced, err := b.SetAlarm(again)
	if err != nil || !replaced || stored.Hour != 9 || stored.Level != 80 {
		t.Fatalf("replace = %+v, %v, %v", stored, replaced, err)
	}
	wantIDs(t, "alarms", alarmIDs(b), "a0", "a1")

	for i := 2; i < alarms.MaxAlarms; i++ {
		f.mustSetAlarm(b, now, alarm("a"+string(rune('0'+i)), 7, i, weekdays))
	}
	wantErr(t, "an 11th", f.setAlarm(b, now, alarm("late", 6, 0, weekdays)), alarms.ErrFull)
	f.mustSetAlarm(b, now, alarm("a5", 6, 0, weekdays))
	if list, _ := f.stored(); len(list) != alarms.MaxAlarms {
		t.Errorf("stored %d alarms", len(list))
	}
}

// the server tells alarms apart by the date the device pins, and the device pins one only when it is set, so the
// same one-time alarm asked for twice reaches it under two ids
func TestTheSameAlarmUnderAnotherIDTakesItsPlaceAndATwinNeverCountsTowardTheLimit(t *testing.T) {
	setup := func(t *testing.T) (*fixture, *alarms.Book, time.Time) {
		f := newFixture(t)
		now := at(26, 22, 30, 0)
		b := f.open(now)
		f.mustSetAlarm(b, now, alarm("before", 6, 0, weekdays))
		f.mustSetAlarm(b, now, alarm("first", 7, 0, 0))
		f.mustSetAlarm(b, now, alarm("after", 8, 0, weekdays))

		// pinned to tomorrow like the first, and so is a date the owner named for that day
		second := alarm("second", 7, 0, 0)
		second.Level = 70
		f.clock.set(now)
		if _, replaced, err := b.SetAlarm(second); err != nil || !replaced {
			t.Fatalf("second: replaced %v, %v", replaced, err)
		}
		wantIDs(t, "after second", alarmIDs(b), "before", "second", "after")
		f.mustSetAlarm(b, now, dated("third", 7, 0, date(2026, 10, 27)))
		wantIDs(t, "after third", alarmIDs(b), "before", "third", "after")
		stored, _ := f.stored()
		wantIDs(t, "stored", ids(stored, func(a alarms.Alarm) string { return a.ID }), "before", "third", "after")

		// another day, other days or another minute is another alarm
		f.mustSetAlarm(b, now, dated("later", 7, 0, date(2026, 10, 28)))
		f.mustSetAlarm(b, now, alarm("mondays", 6, 0, mondays))
		f.mustSetAlarm(b, now, alarm("minute", 7, 1, 0))
		wantIDs(t, "others", alarmIDs(b), "before", "third", "after", "later", "mondays", "minute")
		return f, b, now
	}

	t.Run("a repeating twin is replaced the same way", func(t *testing.T) {
		f, b, now := setup(t)
		f.mustSetAlarm(b, now, alarm("again", 8, 0, weekdays))
		wantIDs(t, "alarms", alarmIDs(b), "before", "third", "again", "later", "mondays", "minute")
	})
	t.Run("an id the book holds moved onto a twin keeps its own place and the twin goes", func(t *testing.T) {
		f, b, now := setup(t)
		f.mustSetAlarm(b, now, alarm("minute", 6, 0, weekdays))
		wantIDs(t, "alarms", alarmIDs(b), "third", "after", "later", "mondays", "minute")
	})
	t.Run("a full book still takes a twin, in its place", func(t *testing.T) {
		f, b, now := setup(t)
		for i := range alarms.MaxAlarms - 6 {
			f.mustSetAlarm(b, now, alarm("x"+string(rune('0'+i)), 9, i, weekdays))
		}
		wantErr(t, "new", f.setAlarm(b, now, alarm("new", 10, 0, weekdays)), alarms.ErrFull)
		f.mustSetAlarm(b, now, alarm("twin", 7, 0, 0))
		got := alarmIDs(b)
		if len(got) != alarms.MaxAlarms || got[1] != "twin" {
			t.Errorf("alarms = %v", got)
		}
	})
	t.Run("a set the store refuses leaves the twin where it was", func(t *testing.T) {
		f, b, now := setup(t)
		blockStore(t, f)
		wantErr(t, "fourth", f.setAlarm(b, now, alarm("fourth", 7, 0, 0)), alarms.ErrStore)
		wantIDs(t, "alarms", alarmIDs(b), "before", "third", "after", "later", "mondays", "minute")
	})
}

func TestSettingATimerAgainReplacesItAndTheSixthIsFull(t *testing.T) {
	f := newFixture(t)
	now := at(26, 6, 0, 0)
	b := f.open(now)
	f.mustSetTimer(b, now, "t0", 60)
	f.mustSetTimer(b, now, "t1", 120)
	f.mustSetTimer(b, now.Add(10*time.Second), "t0", 600)

	_, timers := b.List()
	wantIDs(t, "timers", timerIDs(b), "t0", "t1")
	if timers[0].Seconds != 600 || timers[0].Remaining(now.Add(10*time.Second)) != 600 {
		t.Errorf("t0 = %+v", timers[0])
	}
	for _, id := range []string{"t2", "t3", "t4"} {
		f.mustSetTimer(b, now, id, 60)
	}
	wantErr(t, "a 6th", f.setTimer(b, now, "t9", 60), alarms.ErrFull)
	f.mustSetTimer(b, now, "t3", 90)
	if n := len(timerIDs(b)); n != alarms.MaxTimers {
		t.Errorf("timers = %d", n)
	}
}

func TestAOneTimeAlarmIsStoredWithTheDayItWillRingOn(t *testing.T) {
	setup := func(t *testing.T) (*fixture, *alarms.Book) {
		f := newFixture(t)
		b := f.open(at(26, 22, 30, 0))
		f.clock.set(at(26, 22, 30, 0))
		stored, replaced, err := b.SetAlarm(alarm("once", 7, 0, 0))
		if err != nil || replaced || stored.Date != date(2026, 10, 27) {
			t.Fatalf("set = %+v, %v, %v", stored, replaced, err)
		}
		if list, _ := f.stored(); len(list) != 1 || list[0].Date != date(2026, 10, 27) {
			t.Fatalf("stored %+v", list)
		}
		return f, b
	}

	t.Run("it rings tomorrow, not tonight, and is gone after", func(t *testing.T) {
		f, b := setup(t)
		if rings := b.TakeDue(at(26, 22, 31, 0)); len(rings) != 0 {
			t.Errorf("tonight: %v", rings)
		}
		if rings := b.TakeDue(at(27, 6, 59, 59)); len(rings) != 0 {
			t.Errorf("a second early: %v", rings)
		}
		rings := b.TakeDue(at(27, 7, 0, 0))
		if len(rings) != 1 {
			t.Fatalf("rings = %v", rings)
		}
		r := rings[0]
		if r.Kind != alarms.KindAlarm || r.ID != "once" || r.Level != 50 || !r.At.Equal(at(27, 7, 0, 0)) {
			t.Errorf("ring = %+v", r)
		}
		wantIDs(t, "alarms", alarmIDs(b))
		if b.Armed() {
			t.Error("armed after the last alarm rang")
		}
		if list, _ := f.stored(); len(list) != 0 {
			t.Errorf("stored %v", list)
		}
	})
	t.Run("a date the owner named is kept", func(t *testing.T) {
		f, b := setup(t)
		f.mustSetAlarm(b, at(26, 22, 30, 0), dated("dated", 7, 0, date(2026, 11, 5)))
		if list, _ := b.List(); list[1].Date != date(2026, 11, 5) {
			t.Errorf("date = %v", list[1].Date)
		}
	})
}

// the server confirms what it sends, so a one-time alarm that could never ring is refused here, never dropped later
func TestAOneTimeAlarmWhoseTimeIsNotAfterNowIsRefusedAsPast(t *testing.T) {
	f := newFixture(t)
	now := at(26, 12, 0, 0)
	b := f.open(now)
	f.mustSetAlarm(b, now, dated("keep", 7, 0, date(2026, 10, 27)))
	changes := b.Changes()

	for _, a := range []alarms.Alarm{
		dated("morning", 7, 0, date(2026, 10, 26)),
		dated("yesterday", 13, 0, date(2026, 10, 25)),
		dated("now", 12, 0, date(2026, 10, 26)),
		// an id the book holds keeps its alarm
		dated("keep", 8, 0, date(2026, 10, 1)),
	} {
		wantErr(t, a.ID, f.setAlarm(b, now, a), alarms.ErrPast)
	}
	if changed(changes) {
		t.Error("a refusal signalled a change")
	}
	list, _ := b.List()
	if len(list) != 1 || list[0].Hour != 7 || list[0].Date != date(2026, 10, 27) {
		t.Errorf("alarms = %+v", list)
	}
	if got := len(f.logs.events("alarm_refused")); got != 4 {
		t.Errorf("alarm_refused lines = %d", got)
	}
	if e := f.logs.events("alarm_refused")[0]; e["reason"] != "past" || e["id"] != "morning" || e["kind"] != "alarm" {
		t.Errorf("refusal = %v", e)
	}

	// the next minute is still ahead, a time without a date is pinned to its next day, and a repeating alarm has one
	f.mustSetAlarm(b, now, dated("soon", 12, 1, date(2026, 10, 26)))
	f.mustSetAlarm(b, now, alarm("undated", 9, 0, 0))
	f.mustSetAlarm(b, now, alarm("daily", 7, 0, alarms.EveryDay))

	// a clock that is not set yet tells no date from a past one, so the alarm waits for it
	early := time.Date(2025, 6, 1, 12, 0, 0, 0, bishkek)
	unset := f.open(early)
	if err := f.setAlarm(unset, early, dated("spring", 7, 0, date(2025, 5, 1))); err != nil {
		t.Errorf("set on an unset clock: %v", err)
	}
}

func TestAWeekdayAlarmRingsOncePerOccurrenceAcrossRestarts(t *testing.T) {
	setup := func(t *testing.T) *fixture {
		f := newFixture(t)
		b := f.open(at(26, 6, 0, 0))
		wake := alarm("wake", 7, 0, weekdays)
		wake.Level = 70
		f.mustSetAlarm(b, at(26, 6, 0, 0), wake)
		if rings := b.TakeDue(at(26, 6, 59, 59)); len(rings) != 0 {
			t.Fatalf("early: %v", rings)
		}
		rings := b.TakeDue(at(26, 7, 0, 0))
		if len(rings) != 1 || rings[0].ID != "wake" || rings[0].Level != 70 || !rings[0].At.Equal(at(26, 7, 0, 0)) {
			t.Fatalf("rings = %+v", rings)
		}
		for _, now := range []time.Time{at(26, 7, 0, 0), at(26, 7, 0, 30)} {
			if rings := b.TakeDue(now); len(rings) != 0 {
				t.Fatalf("again at %v: %v", now, rings)
			}
		}
		return f
	}

	t.Run("a restart two minutes later does not ring it again", func(t *testing.T) {
		f := setup(t)
		b := f.open(at(26, 7, 2, 0))
		if rings := b.TakeDue(at(26, 7, 2, 0)); len(rings) != 0 {
			t.Errorf("rings = %v", rings)
		}
		if !b.Armed() {
			t.Error("not armed")
		}
	})
	t.Run("it rings again the next weekday", func(t *testing.T) {
		f := setup(t)
		b := f.open(at(26, 7, 2, 0))
		if rings := b.TakeDue(at(27, 7, 0, 0)); len(rings) != 1 || !rings[0].At.Equal(at(27, 7, 0, 0)) {
			t.Errorf("rings = %+v", rings)
		}
		if rings := b.TakeDue(at(27, 7, 0, 10)); len(rings) != 0 {
			t.Errorf("again: %v", rings)
		}
	})
	t.Run("friday's ring is the last of the week", func(t *testing.T) {
		f := setup(t)
		b := f.open(at(30, 6, 0, 0))
		if rings := b.TakeDue(at(30, 7, 0, 0)); len(rings) != 1 {
			t.Errorf("friday: %v", rings)
		}
		if rings := b.TakeDue(at(31, 7, 0, 0)); len(rings) != 0 {
			t.Errorf("saturday: %v", rings)
		}
	})
}

func TestARestartAfterAnOccurrenceRingsItLateOnceForUpToFiveMinutes(t *testing.T) {
	setup := func(t *testing.T) *fixture {
		f := newFixture(t)
		b := f.open(at(26, 6, 0, 0))
		f.mustSetAlarm(b, at(26, 6, 0, 0), alarm("rep", 7, 0, weekdays))
		f.mustSetAlarm(b, at(26, 6, 0, 0), dated("once", 7, 0, date(2026, 10, 26)))
		return f
	}

	t.Run("at 07:04 both ring late, one after the other, and a restart does not repeat them", func(t *testing.T) {
		f := setup(t)
		b := f.open(at(26, 7, 4, 0))
		rings := b.TakeDue(at(26, 7, 4, 0))
		wantIDs(t, "rings", ringIDs(rings), "rep", "once")
		if !rings[0].At.Equal(at(26, 7, 0, 0)) {
			t.Errorf("at = %v", rings[0].At)
		}
		b = f.open(at(26, 7, 4, 30))
		if rings := b.TakeDue(at(26, 7, 4, 30)); len(rings) != 0 {
			t.Errorf("after a restart: %v", rings)
		}
		wantIDs(t, "alarms", alarmIDs(b), "rep")
	})
	t.Run("the last second of the window still rings", func(t *testing.T) {
		f := setup(t)
		b := f.open(at(26, 7, 5, 0))
		wantIDs(t, "rings", ringIDs(b.TakeDue(at(26, 7, 5, 0))), "rep", "once")
		if len(f.logs.events("ring_missed")) != 0 {
			t.Error("a ring in the window was missed")
		}
	})
	t.Run("a second later the one-time alarm is gone for good and the repeating one waits for its next day", func(t *testing.T) {
		f := setup(t)
		b := f.open(at(26, 7, 5, 1))
		if rings := b.TakeDue(at(26, 7, 5, 1)); len(rings) != 0 {
			t.Errorf("rings = %v", rings)
		}
		wantIDs(t, "alarms", alarmIDs(b), "rep")
		stored, _ := f.stored()
		wantIDs(t, "stored", ids(stored, func(a alarms.Alarm) string { return a.ID }), "rep")
		missed := f.logs.events("ring_missed")
		if len(missed) != 1 || missed[0]["kind"] != "alarm" || missed[0]["id"] != "once" {
			t.Errorf("ring_missed = %v", missed)
		}
		if b.Counts().Missed != 1 {
			t.Errorf("counts = %+v", b.Counts())
		}
	})
}

func TestAnAlarmSetJustAfterItsTimeWaitsForTheNextOne(t *testing.T) {
	f := newFixture(t)
	b := f.open(at(26, 7, 1, 0))
	f.mustSetAlarm(b, at(26, 7, 1, 0), alarm("rep", 7, 0, weekdays))
	if rings := b.TakeDue(at(26, 7, 1, 0)); len(rings) != 0 {
		t.Errorf("rings = %v", rings)
	}
	if rings := b.TakeDue(at(27, 7, 0, 0)); len(rings) != 1 {
		t.Errorf("tomorrow: %v", rings)
	}

	// so does a replacement
	louder := alarm("rep", 7, 0, weekdays)
	louder.Level = 90
	f.mustSetAlarm(b, at(27, 7, 0, 20), louder)
	if rings := b.TakeDue(at(27, 7, 0, 30)); len(rings) != 0 {
		t.Errorf("replacement rang: %v", rings)
	}
	if rings := b.TakeDue(at(28, 7, 0, 0)); len(rings) != 1 || rings[0].Level != 90 {
		t.Errorf("next day: %+v", rings)
	}
}

// the ringer merges two rings of one occurrence time; the book hands out each
func TestTwoAlarmsDueTogetherAreBothHandedOutInListOrder(t *testing.T) {
	f := newFixture(t)
	b := f.open(at(26, 6, 0, 0))
	f.mustSetAlarm(b, at(26, 6, 0, 0), alarm("b", 7, 0, weekdays))
	f.mustSetAlarm(b, at(26, 6, 0, 0), alarm("a", 7, 0, mondays))
	wantIDs(t, "rings", ringIDs(b.TakeDue(at(26, 7, 0, 0))), "b", "a")
	if rings := b.TakeDue(at(26, 7, 0, 0)); len(rings) != 0 {
		t.Errorf("again: %v", rings)
	}
}

func TestTimersRingInTheOrderTheyEndAndAheadOfAnAlarm(t *testing.T) {
	f := newFixture(t)
	start := at(26, 6, 0, 0)
	b := f.open(start)
	f.mustSetTimer(b, start, "slow", 180)
	f.mustSetTimer(b, start, "fast", 60)
	f.mustSetTimer(b, start, "mid", 120)
	f.mustSetAlarm(b, start, alarm("wake", 6, 1, weekdays))

	if rings := b.TakeDue(start.Add(59 * time.Second)); len(rings) != 0 {
		t.Errorf("early: %v", rings)
	}
	if end, ok := b.NearestTimerEnd(); !ok || !end.Equal(start.Add(60*time.Second)) {
		t.Errorf("nearest = %v, %v", end, ok)
	}
	now := start.Add(240 * time.Second)
	rings := b.TakeDue(now)
	wantIDs(t, "rings", ringIDs(rings), "fast", "mid", "slow", "wake")
	if r := rings[0]; r.Kind != alarms.KindTimer || r.Level != 0 || !r.At.Equal(start.Add(60*time.Second)) {
		t.Errorf("first = %+v", r)
	}
	if rings[3].Kind != alarms.KindAlarm {
		t.Errorf("last = %+v", rings[3])
	}
	wantIDs(t, "timers", timerIDs(b))
	if _, ok := b.NearestTimerEnd(); ok {
		t.Error("a nearest end with no timers")
	}
	if _, stored := f.stored(); len(stored) != 0 {
		t.Errorf("stored %v", stored)
	}
	if c := b.Counts(); c.Rings != 4 || c.Sets != 4 {
		t.Errorf("counts = %+v", c)
	}
}

func TestTimersSurviveARestartAndOneTooLateIsDroppedWithoutRinging(t *testing.T) {
	start := at(26, 6, 0, 0)
	setup := func(t *testing.T) *fixture {
		f := newFixture(t)
		b := f.open(start)
		f.mustSetTimer(b, start, "tea", 600)
		f.mustSetTimer(b, start, "egg", 60)
		return f
	}
	sec := func(n int) time.Time { return start.Add(time.Duration(n) * time.Second) }

	t.Run("a reload while they run keeps the time they have left", func(t *testing.T) {
		f := setup(t)
		b := f.open(sec(100))
		_, timers := b.List()
		if len(timers) != 2 || timers[0].ID != "tea" || timers[0].Seconds != 600 ||
			timers[0].Remaining(sec(100)) != 500 || timers[1].Remaining(sec(100)) != 0 {
			t.Errorf("timers = %+v", timers)
		}
	})
	t.Run("a reload ten minutes after the end drops both and saves that", func(t *testing.T) {
		f := setup(t)
		b := f.open(sec(1200))
		if rings := b.TakeDue(sec(1200)); len(rings) != 0 {
			t.Errorf("rings = %v", rings)
		}
		wantIDs(t, "timers", timerIDs(b))
		if _, stored := f.stored(); len(stored) != 0 {
			t.Errorf("stored %v", stored)
		}
		missed := f.logs.events("ring_missed")
		if len(missed) != 2 || missed[0]["kind"] != "timer" || missed[0]["id"] != "tea" || missed[1]["id"] != "egg" {
			t.Errorf("ring_missed = %v", missed)
		}
	})
	t.Run("a reload two minutes after the end rings it late, once", func(t *testing.T) {
		f := setup(t)
		b := f.open(sec(120))
		rings := b.TakeDue(sec(120))
		if len(rings) != 1 || rings[0].ID != "egg" || !rings[0].At.Equal(sec(60)) {
			t.Errorf("rings = %+v", rings)
		}
		if rings := b.TakeDue(sec(120)); len(rings) != 0 {
			t.Errorf("again: %v", rings)
		}
		wantIDs(t, "timers", timerIDs(b), "tea")
	})
	t.Run("301 seconds late is dropped", func(t *testing.T) {
		f := setup(t)
		b := f.open(sec(30))
		if rings := b.TakeDue(sec(60 + 301)); len(rings) != 0 {
			t.Errorf("rings = %v", rings)
		}
		wantIDs(t, "timers", timerIDs(b), "tea")
		if len(f.logs.events("ring_missed")) != 1 {
			t.Errorf("logs = %v", f.logs.lines())
		}
	})
	t.Run("300 seconds late still rings", func(t *testing.T) {
		f := setup(t)
		b := f.open(sec(30))
		wantIDs(t, "rings", ringIDs(b.TakeDue(sec(60+300))), "egg")
	})
}

func TestWhatATimerHasLeftNeverExceedsTheTimeItWasSetFor(t *testing.T) {
	f := newFixture(t)
	start := at(26, 6, 0, 0)
	b := f.open(start)
	f.mustSetTimer(b, start, "tea", 300)
	_, timers := b.List()
	for _, c := range []struct {
		now  time.Time
		want int
	}{
		{start.Add(100 * time.Second), 200},
		{start.Add(100*time.Second + 900*time.Millisecond), 200},
		// a clock set back
		{start.Add(-1000 * time.Second), 300},
		{start.Add(1000 * time.Second), 0},
	} {
		if got := timers[0].Remaining(c.now); got != c.want {
			t.Errorf("remaining at %v = %d, want %d", c.now, got, c.want)
		}
	}
}

func TestAStoreThatCannotBeWrittenLeavesTheListsAsTheyWere(t *testing.T) {
	setup := func(t *testing.T) (*fixture, *alarms.Book, time.Time, <-chan struct{}) {
		f := newFixture(t)
		now := at(26, 6, 0, 0)
		b := f.open(now)
		f.mustSetAlarm(b, now, alarm("a0", 7, 0, weekdays))
		f.mustSetTimer(b, now, "t0", 60)
		blockStore(t, f)
		changes := b.Changes()

		wantErr(t, "new alarm", f.setAlarm(b, now, alarm("a1", 8, 0, weekdays)), alarms.ErrStore)
		wantErr(t, "replaced alarm", f.setAlarm(b, now, alarm("a0", 9, 0, weekdays)), alarms.ErrStore)
		wantErr(t, "new timer", f.setTimer(b, now, "t1", 90), alarms.ErrStore)
		wantErr(t, "replaced timer", f.setTimer(b, now, "t0", 90), alarms.ErrStore)
		if changed(changes) {
			t.Error("a refused set signalled a change")
		}
		list, timers := b.List()
		if len(list) != 1 || list[0].Hour != 7 || len(timers) != 1 || timers[0].Seconds != 60 {
			t.Errorf("lists = %+v %+v", list, timers)
		}
		if n := len(f.logs.events("alarms_store_failed")); n != 4 {
			t.Errorf("alarms_store_failed lines = %d", n)
		}
		refused := f.logs.events("alarm_refused")
		if len(refused) != 4 || refused[0]["reason"] != "store" || refused[2]["kind"] != "timer" {
			t.Errorf("alarm_refused = %v", refused)
		}
		return f, b, now, changes
	}

	t.Run("a ring still counts, so it cannot ring twice in this run", func(t *testing.T) {
		f, b, now, changes := setup(t)
		failed := func() int { return len(f.logs.events("alarms_store_failed")) }
		before := failed()
		wantIDs(t, "rings", ringIDs(b.TakeDue(now.Add(70*time.Second))), "t0")
		if failed() != before+1 {
			t.Errorf("the timer's mark logged %d alarms_store_failed", failed()-before)
		}
		if rings := b.TakeDue(now.Add(70 * time.Second)); len(rings) != 0 {
			t.Errorf("again: %v", rings)
		}
		if !changed(changes) {
			t.Error("a ring did not signal a change")
		}

		// a repeating alarm keeps its lastRungAt in memory: the occurrence never rings again in this run
		before = failed()
		wantIDs(t, "alarm", ringIDs(b.TakeDue(at(26, 7, 0, 0))), "a0")
		if failed() != before+1 {
			t.Errorf("the alarm's mark logged %d alarms_store_failed", failed()-before)
		}
		for _, later := range []time.Time{at(26, 7, 0, 0), at(26, 7, 4, 0)} {
			if rings := b.TakeDue(later); len(rings) != 0 {
				t.Errorf("again at %v: %v", later, rings)
			}
		}
		wantIDs(t, "alarms", alarmIDs(b), "a0")
		wantIDs(t, "tomorrow", ringIDs(b.TakeDue(at(27, 7, 0, 0))), "a0")
	})
	t.Run("a cancel still takes effect", func(t *testing.T) {
		_, b, _, _ := setup(t)
		if !b.CancelAlarm("a0") || !b.CancelTimer("t0") {
			t.Error("cancel refused")
		}
		wantIDs(t, "alarms", alarmIDs(b))
		wantIDs(t, "timers", timerIDs(b))
	})
}

func TestCancellingRemovesForGoodAndSaysWhatWent(t *testing.T) {
	now := at(26, 6, 0, 0)

	t.Run("by id", func(t *testing.T) {
		f := newFixture(t)
		b := f.open(now)
		f.mustSetAlarm(b, now, alarm("a0", 7, 0, weekdays))
		f.mustSetAlarm(b, now, alarm("a1", 8, 0, weekdays))
		f.mustSetTimer(b, now, "t0", 60)
		changes := b.Changes()

		for _, c := range []struct {
			got  bool
			want bool
			what string
		}{
			{b.CancelAlarm("nope"), false, "an unknown alarm"},
			{b.CancelAlarm("t0"), false, "a timer's id as an alarm"},
			{b.CancelTimer("a0"), false, "an alarm's id as a timer"},
		} {
			if c.got != c.want {
				t.Errorf("%s: %v", c.what, c.got)
			}
		}
		if changed(changes) || b.Counts().Cancels != 0 || len(f.logs.events("alarm_cancelled")) != 0 {
			t.Error("a cancel of nothing counted")
		}
		if !b.CancelAlarm("a0") || b.CancelAlarm("a0") || !b.CancelTimer("t0") || b.CancelTimer("t0") {
			t.Error("cancel by id")
		}
		if !changed(changes) || b.Counts().Cancels != 2 {
			t.Errorf("counts = %+v", b.Counts())
		}
		if e := f.logs.events("alarm_cancelled"); len(e) != 1 || e[0]["id"] != "a0" {
			t.Errorf("alarm_cancelled = %v", e)
		}
		if e := f.logs.events("timer_cancelled"); len(e) != 1 || e[0]["id"] != "t0" {
			t.Errorf("timer_cancelled = %v", e)
		}
		reopened := f.open(now)
		wantIDs(t, "alarms after a restart", alarmIDs(reopened), "a1")
		wantIDs(t, "timers after a restart", timerIDs(reopened))
		if !reopened.Armed() {
			t.Error("not armed")
		}
	})
	t.Run("alarms by time go whatever their days, and all go together", func(t *testing.T) {
		f := newFixture(t)
		b := f.open(now)
		f.mustSetAlarm(b, now, alarm("wk", 7, 0, weekdays))
		f.mustSetAlarm(b, now, alarm("mo", 7, 0, mondays))
		f.mustSetAlarm(b, now, alarm("once", 7, 0, 0))
		f.mustSetAlarm(b, now, alarm("late", 7, 30, weekdays))

		wantIDs(t, "at 08:00", b.CancelAlarmsAt(8, 0))
		wantIDs(t, "at 07:00", b.CancelAlarmsAt(7, 0), "wk", "mo", "once")
		wantIDs(t, "left", alarmIDs(b), "late")
		f.mustSetAlarm(b, now, alarm("more", 9, 0, weekdays))
		wantIDs(t, "all", b.CancelAllAlarms(), "late", "more")
		wantIDs(t, "all of none", b.CancelAllAlarms())
		if b.Armed() {
			t.Error("armed")
		}
		if b.Counts().Cancels != 5 {
			t.Errorf("counts = %+v", b.Counts())
		}
		if stored, _ := f.stored(); len(stored) != 0 {
			t.Errorf("stored %v", stored)
		}
	})
	t.Run("timers by length, the first to end, and all", func(t *testing.T) {
		f := newFixture(t)
		b := f.open(now)
		f.mustSetTimer(b, now, "ten", 600)
		f.mustSetTimer(b, now.Add(-30*time.Second), "tenearly", 600)
		f.mustSetTimer(b, now, "five", 300)
		f.mustSetTimer(b, now, "hour", 3600)

		if id, ok := b.CancelTimerFor(120); ok {
			t.Errorf("a length none has: %s", id)
		}
		if id, ok := b.CancelTimerFor(600); !ok || id != "tenearly" {
			t.Errorf("for 600 = %s, %v", id, ok)
		}
		if id, ok := b.CancelNextTimer(); !ok || id != "five" {
			t.Errorf("next = %s, %v", id, ok)
		}
		wantIDs(t, "left", timerIDs(b), "ten", "hour")
		wantIDs(t, "all", b.CancelAllTimers(), "ten", "hour")
		if id, ok := b.CancelNextTimer(); ok {
			t.Errorf("next of none = %s", id)
		}
		if _, stored := f.stored(); len(stored) != 0 {
			t.Errorf("stored %v", stored)
		}
		if b.Counts().Cancels != 4 || len(f.logs.events("timer_cancelled")) != 4 {
			t.Errorf("counts = %+v", b.Counts())
		}
	})
}

func TestTheNextTickIsTheNextDueSecondAndNeverMoreThanAMinute(t *testing.T) {
	f := newFixture(t)
	now := at(26, 6, 0, 0)
	b := f.open(now)
	if got := b.NextTick(now); got != time.Minute {
		t.Errorf("empty book: %v", got)
	}
	f.mustSetAlarm(b, now, alarm("wake", 7, 0, weekdays))
	f.mustSetAlarm(b, now, alarm("once", 6, 45, 0))
	for _, c := range []struct {
		now  time.Time
		want time.Duration
	}{
		{at(26, 6, 0, 0), time.Minute},
		{at(26, 6, 44, 30), 30 * time.Second},
		{at(26, 6, 44, 59).Add(300 * time.Millisecond), 700 * time.Millisecond},
		{at(26, 6, 59, 15), 45 * time.Second},
	} {
		if got := b.NextTick(c.now); got != c.want {
			t.Errorf("at %v: %v, want %v", c.now, got, c.want)
		}
	}

	f.mustSetTimer(b, at(26, 6, 44, 0), "near", 10)
	if got := b.NextTick(at(26, 6, 44, 0)); got != 10*time.Second {
		t.Errorf("with a timer: %v", got)
	}
	// due now, also late within the window
	if got := b.NextTick(at(26, 6, 44, 10)); got != 0 {
		t.Errorf("due: %v", got)
	}
	if got := b.NextTick(at(26, 6, 45, 30)); got != 0 {
		t.Errorf("late: %v", got)
	}
	b.TakeDue(at(26, 6, 45, 30))
	if got := b.NextTick(at(26, 6, 45, 30)); got != time.Minute {
		t.Errorf("after taking: %v", got)
	}
	// a weekday alarm after its ring waits for tomorrow
	b.TakeDue(at(26, 7, 0, 0))
	if got := b.NextTick(at(26, 7, 0, 0)); got != time.Minute {
		t.Errorf("after the ring: %v", got)
	}
}

func TestAClockThatIsNotSetYetRingsNoAlarmAndPurgesNoneButATimerStillRuns(t *testing.T) {
	early := time.Date(1970, 1, 1, 6, 0, 0, 0, bishkek)
	setup := func(t *testing.T) (*fixture, *alarms.Book) {
		f := newFixture(t)
		b := f.open(early)
		f.mustSetAlarm(b, early, alarm("once", 7, 0, 0))
		f.mustSetAlarm(b, early, alarm("rep", 7, 0, alarms.EveryDay))
		if list, _ := b.List(); list[0].Date.IsZero() {
			t.Fatal("the one-time alarm has no date")
		}
		if got := b.NextTick(time.Date(1970, 1, 1, 6, 59, 50, 0, bishkek)); got != time.Minute {
			t.Errorf("next tick = %v", got)
		}
		// the last is past the late window: a stale one-time alarm, kept for the clock to be set
		for _, now := range []time.Time{early.Add(time.Hour), early.Add(time.Hour + 2*time.Minute), early.Add(time.Hour + 6*time.Minute)} {
			if rings := b.TakeDue(now); len(rings) != 0 {
				t.Errorf("at %v: %v", now, rings)
			}
		}
		wantIDs(t, "alarms", alarmIDs(b), "once", "rep")

		f.mustSetTimer(b, early, "tea", 60)
		if got := b.NextTick(early); got != time.Minute {
			t.Errorf("next tick = %v", got)
		}
		if got := b.NextTick(early.Add(20 * time.Second)); got != 40*time.Second {
			t.Errorf("next tick to the timer = %v", got)
		}
		rings := b.TakeDue(early.Add(time.Minute))
		if len(rings) != 1 || rings[0].Kind != alarms.KindTimer {
			t.Errorf("rings = %+v", rings)
		}
		wantIDs(t, "alarms", alarmIDs(b), "once", "rep")
		if len(f.logs.events("ring_missed")) != 0 {
			t.Error("missed on an unset clock")
		}
		return f, b
	}

	t.Run("once the clock is right, the one-time alarm that went stale is purged", func(t *testing.T) {
		f, b := setup(t)
		if rings := b.TakeDue(at(26, 12, 0, 0)); len(rings) != 0 {
			t.Errorf("rings = %v", rings)
		}
		wantIDs(t, "alarms", alarmIDs(b), "rep")
		if stored, _ := f.stored(); len(stored) != 1 {
			t.Errorf("stored %v", stored)
		}
		if e := f.logs.events("ring_missed"); len(e) != 1 || e[0]["id"] != "once" {
			t.Errorf("ring_missed = %v", e)
		}
	})
	t.Run("a book loaded on such a clock keeps a stale alarm until the clock is right", func(t *testing.T) {
		f, _ := setup(t)
		reloaded := f.open(early.Add(100 * time.Second))
		reloaded.TakeDue(early.Add(100 * time.Second))
		wantIDs(t, "alarms", alarmIDs(reloaded), "once", "rep")
	})
}

func TestClocksThatMoveSkipAMissingMinuteAndRingARepeatedOneOnce(t *testing.T) {
	berlin := mustZone("Europe/Berlin")
	local := func(month time.Month, d, hour, minute int) time.Time {
		return time.Date(2026, month, d, hour, minute, 0, 0, berlin)
	}
	utc := func(month time.Month, d, hour, minute int) time.Time {
		return time.Date(2026, month, d, hour, minute, 0, 0, time.UTC)
	}

	t.Run("spring forward: no 02:30 on the 29th of March", func(t *testing.T) {
		f := newFixture(t)
		now := local(time.March, 28, 22, 0)
		b := f.openIn(now, "Europe/Berlin")
		f.mustSetAlarm(b, now, alarm("night", 2, 30, alarms.EveryDay))
		wantErr(t, "a one-time alarm on the missing minute",
			f.setAlarm(b, now, dated("gap", 2, 30, date(2026, 3, 29))), alarms.ErrPast)
		f.clock.set(now)
		pinned, _, err := b.SetAlarm(alarm("undated", 2, 30, 0))
		if err != nil || pinned.Date != date(2026, 3, 30) {
			t.Errorf("undated = %+v, %v", pinned, err)
		}

		// 01:00 UTC is 03:00 CEST: the minute never came
		if rings := b.TakeDue(utc(time.March, 29, 1, 0)); len(rings) != 0 {
			t.Errorf("rings = %v", rings)
		}
		if got := b.NextTick(local(time.March, 30, 2, 29)); got != time.Minute {
			t.Errorf("next tick = %v", got)
		}
		wantIDs(t, "monday", ringIDs(b.TakeDue(local(time.March, 30, 2, 30))), "night", "undated")
	})
	t.Run("fall back: 02:30 on the 25th of October rings at the first, not again at the second", func(t *testing.T) {
		f := newFixture(t)
		now := local(time.October, 24, 22, 0)
		b := f.openIn(now, "Europe/Berlin")
		f.mustSetAlarm(b, now, alarm("night", 2, 30, alarms.EveryDay))

		rings := b.TakeDue(utc(time.October, 25, 0, 30))
		if len(rings) != 1 || !rings[0].At.Equal(utc(time.October, 25, 0, 30)) {
			t.Fatalf("first 02:30: %+v", rings)
		}
		if rings := b.TakeDue(utc(time.October, 25, 1, 30)); len(rings) != 0 {
			t.Errorf("second 02:30: %v", rings)
		}
		b = f.openIn(utc(time.October, 25, 1, 31), "Europe/Berlin")
		if rings := b.TakeDue(utc(time.October, 25, 1, 31)); len(rings) != 0 {
			t.Errorf("after a restart: %v", rings)
		}
		rings = b.TakeDue(local(time.October, 26, 2, 30))
		if len(rings) != 1 || !rings[0].At.Equal(utc(time.October, 26, 1, 30)) {
			t.Errorf("next day: %+v", rings)
		}
	})
}

func TestTheZoneDecidesWhenAnAlarmRingsAndARefusedOneLeavesItInForce(t *testing.T) {
	f := newFixture(t)
	now := at(26, 6, 0, 0)
	b := f.openIn(now, "UTC")
	changes := b.Changes()
	f.mustSetAlarm(b, now, alarm("wake", 7, 0, weekdays))
	if rings := b.TakeDue(at(26, 7, 0, 0)); len(rings) != 0 {
		t.Errorf("07:00 in Bishkek rang in UTC: %v", rings)
	}
	_ = changed(changes)
	if err := b.SetZone("Asia/Bishkek"); err != nil {
		t.Fatal(err)
	}
	if !changed(changes) {
		t.Error("a new zone did not signal a change")
	}
	if err := b.SetZone("Asia/Bishkek"); err != nil || changed(changes) {
		t.Errorf("the same zone again: %v, changed", err)
	}
	for _, bad := range []string{"Mars/Olympus", "Local", "../etc/passwd"} {
		wantErr(t, bad, b.SetZone(bad), alarms.ErrZone)
	}
	if changed(changes) {
		t.Error("a refused zone signalled a change")
	}
	// still by Bishkek's calendar
	wantIDs(t, "rings", ringIDs(b.TakeDue(at(26, 7, 0, 0))), "wake")
}

// ruling M6a-R2: any calendar but the owner's would ring an alarm at the wrong hour or drop it as missed
func TestUntilAZoneIsSetTheBookHoldsItsAlarmsWhileTimersRun(t *testing.T) {
	f := newFixture(t)
	b := f.open(at(25, 22, 0, 0))
	f.mustSetAlarm(b, at(25, 22, 0, 0), dated("late", 23, 0, date(2026, 10, 25)))
	// 01:00 UTC is 07:00 in Bishkek
	f.mustSetAlarm(b, at(25, 22, 0, 0), alarm("night", 1, 0, alarms.EveryDay))
	f.mustSetAlarm(b, at(25, 22, 0, 0), alarm("wake", 7, 0, weekdays))

	// a start before the config with the zone is applied: "late" went stale overnight and "wake" is due
	b = f.openUnzoned(at(26, 6, 59, 50))
	changes := b.Changes()
	f.mustSetTimer(b, at(26, 6, 59, 50), "tea", 40)
	wantErr(t, "a set", f.setAlarm(b, at(26, 6, 59, 50), alarm("new", 8, 0, weekdays)), alarms.ErrNoZone)
	if got := b.NextTick(at(26, 6, 59, 50)); got != 40*time.Second {
		t.Errorf("next tick = %v, want the timer's", got)
	}
	wantIDs(t, "rings", ringIDs(b.TakeDue(at(26, 7, 0, 30))), "tea")
	wantIDs(t, "alarms", alarmIDs(b), "late", "night", "wake")
	if len(f.logs.events("ring_missed")) != 0 {
		t.Error("an alarm went as missed before a zone was set")
	}

	wantErr(t, "a refused zone", b.SetZone("Mars/Olympus"), alarms.ErrZone)
	_ = changed(changes)
	wantErr(t, "a set after it", f.setAlarm(b, at(26, 7, 0, 40), alarm("new", 8, 0, weekdays)), alarms.ErrNoZone)
	if rings := b.TakeDue(at(26, 7, 0, 40)); len(rings) != 0 {
		t.Errorf("rings after a refused zone: %v", rings)
	}
	wantIDs(t, "alarms after a refused zone", alarmIDs(b), "late", "night", "wake")
	// what it cannot hold is invalid whatever the zone
	wantErr(t, "an invalid set", f.setAlarm(b, at(26, 7, 0, 40), alarm("new", 24, 0, weekdays)), alarms.ErrInvalid)
	if e := f.logs.events("alarm_refused"); len(e) != 3 || e[0]["reason"] != "no_zone" || e[2]["reason"] != "invalid" {
		t.Errorf("alarm_refused = %v", e)
	}

	if err := b.SetZone("Asia/Bishkek"); err != nil {
		t.Fatal(err)
	}
	if !changed(changes) {
		t.Error("the first zone did not signal a change")
	}
	wantIDs(t, "rings by Bishkek", ringIDs(b.TakeDue(at(26, 7, 0, 45))), "wake")
	wantIDs(t, "alarms", alarmIDs(b), "night", "wake")
	if e := f.logs.events("ring_missed"); len(e) != 1 || e[0]["id"] != "late" {
		t.Errorf("ring_missed = %v", e)
	}
}

func TestAnEmptyZoneIsUTCSetOnPurpose(t *testing.T) {
	f := newFixture(t)
	now := at(26, 6, 0, 0)
	b := f.openUnzoned(now)
	changes := b.Changes()
	if _, ok := b.Zone(); ok {
		t.Fatal("a zone before any was set")
	}
	if err := b.SetZone(""); err != nil {
		t.Fatal(err)
	}
	if !changed(changes) {
		t.Error("the zone did not signal a change")
	}
	if loc, ok := b.Zone(); !ok || loc != time.UTC {
		t.Errorf("zone %v, %t", loc, ok)
	}
	f.mustSetAlarm(b, now, alarm("wake", 7, 0, weekdays))
	wantIDs(t, "07:00 UTC", ringIDs(b.TakeDue(time.Date(2026, 10, 26, 7, 0, 0, 0, time.UTC))), "wake")
}

func TestWhatTheBookCannotHoldIsRefusedAsInvalid(t *testing.T) {
	f := newFixture(t)
	now := at(26, 6, 0, 0)
	b := f.open(now)
	for _, a := range []alarms.Alarm{
		alarm("", 7, 0, weekdays),
		alarm("Upper", 7, 0, weekdays),
		alarm("abcdefghijklmnopq", 7, 0, weekdays),
		alarm("ok", 24, 0, weekdays),
		alarm("ok", 7, 60, weekdays),
		alarm("ok", -1, 0, weekdays),
		alarm("ok", 7, 0, 0x80),
		{ID: "ok", Hour: 7, Days: weekdays, Level: 0},
		{ID: "ok", Hour: 7, Days: weekdays, Level: 101},
		{ID: "ok", Hour: 7, Days: weekdays, Date: date(2026, 11, 3), Level: 50},
		{ID: "ok", Hour: 7, Date: date(2026, 2, 30), Level: 50},
		{ID: "ok", Hour: 7, Date: date(10000, 1, 1), Level: 50},
	} {
		wantErr(t, "alarm "+a.ID, f.setAlarm(b, now, a), alarms.ErrInvalid)
	}
	for _, c := range []struct {
		id      string
		seconds int
	}{{"ok", 0}, {"ok", alarms.MaxTimerSeconds + 1}, {"Bad id", 60}} {
		wantErr(t, "timer", f.setTimer(b, now, c.id, c.seconds), alarms.ErrInvalid)
	}
	f.mustSetAlarm(b, now, alarms.Alarm{ID: "abcdefghijklmnop", Hour: 23, Minute: 59, Days: alarms.EveryDay, Level: 1})
	f.mustSetTimer(b, now, "z9", alarms.MaxTimerSeconds)

	if c := b.Counts(); c.Refusals != 15 || c.Sets != 2 {
		t.Errorf("counts = %+v", c)
	}
	// an id the book would not hold is no id to log
	if f.logs.contains("Upper") || f.logs.contains("abcdefghijklmnopq") || f.logs.contains("Bad id") {
		t.Error("an invalid id reached the log")
	}
	if e := f.logs.events("alarm_refused"); e[len(e)-1]["reason"] != "invalid" {
		t.Errorf("refusal = %v", e[len(e)-1])
	}
	if e := f.logs.events("timer_set"); len(e) != 1 || e[0]["id"] != "z9" || e[0]["seconds"] != float64(alarms.MaxTimerSeconds) {
		t.Errorf("timer_set = %v", e)
	}
}

func TestEveryChangeOfWhatTheBookHoldsIsSignalledAndNothingElse(t *testing.T) {
	f := newFixture(t)
	now := at(26, 6, 0, 0)
	b := f.openUnzoned(now)
	lights, state := b.Changes(), b.Changes()
	both := func() bool {
		l, s := changed(lights), changed(state)
		if l != s {
			t.Errorf("subscribers disagree: %v, %v", l, s)
		}
		return l
	}
	if both() {
		t.Error("a fresh book signalled")
	}
	if err := b.SetZone("Asia/Bishkek"); err != nil {
		t.Fatal(err)
	}
	f.mustSetAlarm(b, now, alarm("wake", 7, 0, weekdays))
	f.mustSetTimer(b, now, "tea", 60)
	// three changes before a look fold into one
	if !both() || both() {
		t.Error("the zone and sets are not one change")
	}
	b.List()
	b.NextTick(now)
	b.TakeDue(now)
	b.CancelAlarm("nope")
	_ = f.setAlarm(b, now, alarm("Bad", 7, 0, weekdays))
	if both() {
		t.Error("reads, nothing due, a cancel of nothing and a refusal signalled")
	}
	late := b.Changes()
	b.TakeDue(now.Add(time.Minute))
	if !both() || !changed(late) {
		t.Error("a ring did not signal every subscriber")
	}
	b.CancelAlarm("wake")
	if !both() {
		t.Error("a cancel did not signal")
	}
}

func TestTheBookIsSafeToShareBetweenGoroutines(t *testing.T) {
	f := newFixture(t)
	now := at(26, 6, 0, 0)
	b := f.open(now)
	var wg sync.WaitGroup
	for g := range 4 {
		wg.Go(func() {
			id := string(rune('a' + g))
			for range 20 {
				_, _, _ = b.SetAlarm(alarm(id, 7, g, weekdays))
				_, _ = b.SetTimer(id, 1)
				b.List()
				b.TakeDue(now.Add(time.Second))
				b.NextTick(now)
				b.CancelAlarm(id)
				_, _ = b.CancelNextTimer()
			}
		})
	}
	wg.Go(func() {
		for range 20 {
			_ = b.SetZone("Europe/Berlin")
			_ = b.SetZone("Asia/Bishkek")
			_ = changed(b.Changes())
			_ = b.Counts()
		}
	})
	wg.Wait()
}

func TestABookNeedsAPathAndAClock(t *testing.T) {
	if _, err := alarms.NewBook("", &fakeClock{}, nil); err == nil {
		t.Error("no path")
	}
	if _, err := alarms.NewBook(alarms.StorePath(t.TempDir()), nil, nil); err == nil {
		t.Error("no clock")
	}
}
