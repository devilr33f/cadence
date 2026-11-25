package logs_test

import (
	"sync"
	"testing"
	"time"
)

// fakeClock fires an After once Advance passes its time; every After call is
// reported on afters, so a test sees what the exporter waits for.
type fakeClock struct {
	afters chan time.Duration

	mu      sync.Mutex
	now     time.Time
	waiters []waiter
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock(now time.Time) *fakeClock {
	return &fakeClock{now: now, afters: make(chan time.Duration, 4096)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	w := waiter{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.waiters = append(c.waiters, w)
	c.mu.Unlock()
	c.afters <- d
	return w.ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	left := c.waiters[:0]
	for _, w := range c.waiters {
		if w.at.After(c.now) {
			left = append(left, w)
			continue
		}
		w.ch <- c.now
	}
	c.waiters = left
}

// next waits for the exporter's next After and returns its duration.
func (c *fakeClock) next(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-c.afters:
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("the exporter waits for nothing")
		return 0
	}
}

// expect waits for the next After, checks its duration and lets it fire.
func (c *fakeClock) expect(t *testing.T, want time.Duration) {
	t.Helper()
	if d := c.next(t); d != want {
		t.Fatalf("waits %v, want %v", d, want)
	}
	c.Advance(want)
}

// skipTo waits for the exporter's first After of d, passing over the others.
func (c *fakeClock) skipTo(t *testing.T, d time.Duration) {
	t.Helper()
	got := c.next(t)
	for got != d {
		got = c.next(t)
	}
}
