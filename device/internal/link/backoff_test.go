package link

import (
	"math/rand/v2"
	"testing"
	"time"
)

func TestDelaySequence(t *testing.T) {
	half := func() float64 { return 0.5 }
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60}
	for i, w := range want {
		if got := Delay(i+1, half); got != w*time.Second {
			t.Errorf("attempt %d: %v, want %v", i+1, got, w*time.Second)
		}
	}
}

func TestDelayJitterBounds(t *testing.T) {
	base := func(attempt int) time.Duration { return Delay(attempt, func() float64 { return 0.5 }) }
	for attempt := 1; attempt <= 12; attempt++ {
		b := base(attempt)
		if got := Delay(attempt, func() float64 { return 0 }); got != b*8/10 {
			t.Errorf("attempt %d, rnd 0: %v, want %v", attempt, got, b*8/10)
		}
		for range 100 {
			got := Delay(attempt, rand.Float64)
			if got < b*8/10 || got >= b*12/10 {
				t.Fatalf("attempt %d: %v outside [%v, %v)", attempt, got, b*8/10, b*12/10)
			}
		}
	}
	if got := Delay(1<<20, func() float64 { return 0.5 }); got != 60*time.Second {
		t.Fatalf("huge attempt: %v", got)
	}
}

func TestAttemptResetsAfterMinuteConnected(t *testing.T) {
	cases := []struct {
		attempt int
		up      time.Duration
		want    int
	}{
		{0, 0, 1},
		{3, 0, 4},
		{5, 59 * time.Second, 6},
		{5, 60 * time.Second, 1},
	}
	for _, c := range cases {
		if got := nextAttempt(c.attempt, c.up); got != c.want {
			t.Errorf("nextAttempt(%d, %v) = %d, want %d", c.attempt, c.up, got, c.want)
		}
	}
}
