package input_test

import (
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/input"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

type inputCounts struct {
	mu                     sync.Mutex
	wakes, acted, ignoredN int
}

func (c *inputCounts) WakeHit() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wakes++
}

func (c *inputCounts) CommandWord(acted bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if acted {
		c.acted++
	} else {
		c.ignoredN++
	}
}

func (c *inputCounts) get() (wakes, acted, ignored int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wakes, c.acted, c.ignoredN
}

// every wake audiod's engine heard counts, whether it began a session or not; every command word counts as acted on
// or ignored
func TestWakeHitsAndCommandWordsCounted(t *testing.T) {
	counts := &inputCounts{}
	h := newHarness(t, input.Options{Counts: counts})
	ready(h)
	// no engine yet: ignored, still a hit
	h.wake()
	h.envelope(&audiodv1.VendorStatus{Engines: true})
	h.advance(3 * time.Second)
	h.wake()
	h.trigger(t)
	h.envelope(&audiodv1.CommandWord{Id: 3})
	h.in.UseCommands(&policy{})
	h.envelope(&audiodv1.CommandWord{Id: 3})
	h.envelope(&audiodv1.CommandWord{Id: 1})
	if w, a, i := counts.get(); w != 2 || a != 1 || i != 2 {
		t.Fatalf("wakes %d, acted %d, ignored %d", w, a, i)
	}
}
