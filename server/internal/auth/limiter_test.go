package auth_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

// limit is a limiter on a fake clock that records its window reports.
type limit struct {
	*auth.Limiter
	c       *clock
	reports []string
}

func newLimit() *limit {
	l := &limit{c: newClock()}
	l.Limiter = auth.NewLimiterAt(l.c.Now, func(key string, count int) {
		l.reports = append(l.reports, fmt.Sprintf("%s %d", key, count))
	})
	return l
}

// five failures per bucket in 15 minutes; the wait ends when the oldest leaves
func TestLimiterPerBucket(t *testing.T) {
	l := newLimit()
	for i := range 5 {
		if _, _, _, ok := l.Begin("ip:198.51.100.1", true); !ok {
			t.Fatalf("attempt %d refused", i+1)
		}
		l.c.Add(time.Minute)
	}
	_, wait, first, ok := l.Begin("ip:198.51.100.1", true)
	if ok || wait != 10*time.Minute || !first {
		t.Fatalf("sixth: ok %v wait %v first %v", ok, wait, first)
	}
	if _, _, first, _ := l.Begin("ip:198.51.100.1", true); first {
		t.Fatal("the second refusal of a window counts as first")
	}
	if _, _, _, ok := l.Begin("ip:198.51.100.2", true); !ok {
		t.Fatal("another ip refused")
	}
	l.c.Add(10 * time.Minute)
	if _, _, _, ok := l.Begin("ip:198.51.100.1", true); !ok {
		t.Fatal("refused after the oldest left the window")
	}
	// the window's refusals are reported once it ends
	if !slices.Equal(l.reports, []string{"ip:198.51.100.1 2"}) {
		t.Fatalf("reports %q", l.reports)
	}
}

// twenty failures in 15 minutes from any ips refuse every ip, not a known browser
func TestLimiterGlobal(t *testing.T) {
	l := newLimit()
	for i := range 20 {
		if _, _, _, ok := l.Begin(fmt.Sprintf("ip:203.0.113.%d", i), true); !ok {
			t.Fatalf("attempt %d refused", i+1)
		}
		l.c.Add(time.Second)
	}
	_, wait, first, ok := l.Begin("ip:192.0.2.1", true)
	if ok || wait != 15*time.Minute-20*time.Second || !first {
		t.Fatalf("21st: ok %v wait %v first %v", ok, wait, first)
	}
	if _, _, _, ok := l.Begin("browser:b1", false); !ok {
		t.Fatal("a known browser waits on the global bucket")
	}
	l.c.Add(wait)
	l.Sweep()
	if !slices.Equal(l.reports, []string{"global 1"}) {
		t.Fatalf("reports %q", l.reports)
	}
	if _, _, _, ok := l.Begin("ip:192.0.2.1", true); !ok {
		t.Fatal("refused after the window")
	}
}

// a known browser's failures count in its own bucket only
func TestLimiterBrowserBucket(t *testing.T) {
	l := newLimit()
	for i := range 5 {
		if _, _, _, ok := l.Begin("browser:b1", false); !ok {
			t.Fatalf("browser attempt %d refused", i+1)
		}
	}
	if _, _, _, ok := l.Begin("browser:b1", false); ok {
		t.Fatal("sixth browser attempt allowed")
	}
	for i := range 20 {
		if _, _, _, ok := l.Begin(fmt.Sprintf("ip:203.0.113.%d", i), true); !ok {
			t.Fatalf("global attempt %d refused: the browser's counted", i+1)
		}
	}
}

// a success clears its bucket and frees its own global slot; an abandoned
// attempt (the store failed) counts for nothing
func TestLimiterSuccessAndAbandon(t *testing.T) {
	l := newLimit()
	for range 4 {
		l.Begin("ip:198.51.100.1", true)
	}
	a, _, _, _ := l.Begin("ip:198.51.100.1", true)
	l.Succeeded(a)
	for i := range 5 {
		if _, _, _, ok := l.Begin("ip:198.51.100.1", true); !ok {
			t.Fatalf("after success, attempt %d refused", i+1)
		}
	}
	b, _, _, ok := l.Begin("ip:198.51.100.3", true)
	if !ok {
		t.Fatal("refused")
	}
	l.Abandon(b)
	for i := range 5 {
		if _, _, _, ok := l.Begin("ip:198.51.100.3", true); !ok {
			t.Fatalf("after abandon, attempt %d refused", i+1)
		}
	}
	// 4 failures before the success stay in the global count: 4+5+5 = 14
	for i := range 6 {
		if _, _, _, ok := l.Begin(fmt.Sprintf("ip:203.0.113.%d", i), true); !ok {
			t.Fatalf("global attempt %d refused", 15+i)
		}
	}
	if _, _, _, ok := l.Begin("ip:203.0.113.99", true); ok {
		t.Fatal("21st global attempt allowed")
	}
}

// an attempt is held from its start, so parallel guesses cannot pass the limit
func TestLimiterHoldsInFlightAttempts(t *testing.T) {
	l := newLimit()
	for range 5 {
		l.Begin("ip:198.51.100.1", true)
	}
	if _, _, _, ok := l.Begin("ip:198.51.100.1", true); ok {
		t.Fatal("sixth in-flight attempt allowed")
	}
}
