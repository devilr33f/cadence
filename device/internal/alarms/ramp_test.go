package alarms_test

import (
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/alarms"
)

// v1's ramp: 5-point steps, each once 5 points are earned, the alarm's own level at 30 s, never below where it began
func TestRampLevel(t *testing.T) {
	for _, c := range []struct {
		start, level int
		elapsed      time.Duration
		want         int
	}{
		{30, 80, 0, 30},
		{30, 80, -time.Second, 30},
		{30, 80, 2999 * time.Millisecond, 30},
		{30, 80, 3 * time.Second, 35},
		{30, 80, 15 * time.Second, 55},
		{30, 80, 29999 * time.Millisecond, 75},
		{30, 80, 30 * time.Second, 80},
		{30, 80, time.Hour, 80},
		// a span that is no multiple of 5: the last step is the level itself, at 30 s
		{30, 52, 29999 * time.Millisecond, 50},
		{30, 52, 30 * time.Second, 52},
		// 3 points earn no step: the level comes at 30 s
		{30, 33, 29 * time.Second, 30},
		{30, 33, 30 * time.Second, 33},
		// never lower: an alarm below the level in force stays at it
		{60, 40, 0, 60},
		{60, 40, time.Minute, 60},
		{50, 50, time.Minute, 50},
		// clamped to 0-100
		{-10, 150, 30 * time.Second, 100},
		{-10, 150, 0, 0},
	} {
		if got := alarms.RampLevel(c.start, c.level, c.elapsed, alarms.RampDuration); got != c.want {
			t.Errorf("RampLevel(%d, %d, %v) = %d, want %d", c.start, c.level, c.elapsed, got, c.want)
		}
	}
}

// the ramp moves at the first moment its level changes, never between, and stops once at the alarm's level
func TestRampNextStep(t *testing.T) {
	for _, c := range []struct {
		start, level int
		elapsed      time.Duration
		next         time.Duration
		ok           bool
	}{
		{30, 80, 0, 3 * time.Second, true},
		{30, 80, 3 * time.Second, 3 * time.Second, true},
		{30, 80, 4 * time.Second, 2 * time.Second, true},
		{30, 80, 27 * time.Second, 3 * time.Second, true},
		{30, 80, 30 * time.Second, 0, false},
		// 7 points over 30 s: one step at 21.429 s, then the level at 30 s
		{30, 37, 0, 21429 * time.Millisecond, true},
		{30, 37, 22 * time.Second, 8 * time.Second, true},
		{30, 33, 0, 30 * time.Second, true},
		{60, 40, 0, 0, false},
	} {
		next, ok := alarms.RampNextStep(c.start, c.level, c.elapsed, alarms.RampDuration)
		if next != c.next || ok != c.ok {
			t.Errorf("RampNextStep(%d, %d, %v) = %v, %t; want %v, %t", c.start, c.level, c.elapsed, next, ok, c.next, c.ok)
		}
		if !ok {
			continue
		}
		at := c.elapsed + next
		if before, after := alarms.RampLevel(c.start, c.level, at-time.Millisecond, alarms.RampDuration), alarms.RampLevel(c.start, c.level, at, alarms.RampDuration); after <= before {
			t.Errorf("RampNextStep(%d, %d, %v): no step at %v (%d then %d)", c.start, c.level, c.elapsed, at, before, after)
		}
	}
}
