package otlp_test

import (
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp"
)

// a request outlasts the server's own budget for it (5 s for the token lookup,
// 5 s for a decompression slot, 10 s to store): one given up sooner may be
// stored all the same, and its records again on the retry
func TestRequestTimeoutOutlastsTheServer(t *testing.T) {
	c, err := otlp.NewClient("https://cadence.invalid", token, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := otlp.Timeout(c); d != 30*time.Second {
		t.Fatalf("timeout %v, want 30 s", d)
	}
}

// a backoff the device picks spreads over ±20 %, a wait the server asks for
// over 0 to +20 %: never shorter than asked
func TestJitterAndStretch(t *testing.T) {
	const d = 10 * time.Second
	near := func(a, b time.Duration) bool { return (a - b).Abs() <= time.Microsecond }
	for _, c := range []struct {
		r               float64
		jitter, stretch time.Duration
	}{
		{0, 8 * time.Second, 10 * time.Second},
		{0.5, 10 * time.Second, 11 * time.Second},
		{0.99, 11960 * time.Millisecond, 11980 * time.Millisecond},
	} {
		rnd := func() float64 { return c.r }
		if got := otlp.Jitter(d, rnd); !near(got, c.jitter) {
			t.Errorf("jitter at %v: %v, want %v", c.r, got, c.jitter)
		}
		if got := otlp.Stretch(d, rnd); !near(got, c.stretch) {
			t.Errorf("stretch at %v: %v, want %v", c.r, got, c.stretch)
		}
	}
}
