package capability

import (
	"slices"
	"sync"
)

// Availability is which of a device's capabilities have their hardware right now: a screen board's display.screen
// while its hdmi sink is plugged in, say. An unavailable capability stays reported (its descriptor, its config on the
// server and on the device are kept), its commands are answered "unavailable", and the server hears of it on the
// capability's state deltas (StateDelta.unavailable), so it can come back without a new report. Every capability is
// available until Set says otherwise. Safe for concurrent use.
type Availability struct {
	mu   sync.Mutex
	down map[string]bool
	subs []chan struct{}
}

func NewAvailability() *Availability { return &Availability{down: map[string]bool{}} }

// Set marks ids available or not and reports whether anything changed; each subscriber hears of a change once.
func (a *Availability) Set(available bool, ids ...string) bool {
	a.mu.Lock()
	changed := false
	for _, id := range ids {
		if a.down[id] == !available {
			continue
		}
		changed = true
		if available {
			delete(a.down, id)
		} else {
			a.down[id] = true
		}
	}
	subs := slices.Clone(a.subs)
	a.mu.Unlock()
	if changed {
		for _, c := range subs {
			select {
			case c <- struct{}{}:
			default:
			}
		}
	}
	return changed
}

// Available reports whether id has its hardware now; a nil Availability has every capability available.
func (a *Availability) Available(id string) bool {
	if a == nil {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.down[id]
}

// Changed is a new subscription: it receives (coalesced) after each Set that changed anything. A nil Availability
// never changes.
func (a *Availability) Changed() <-chan struct{} {
	c := make(chan struct{}, 1)
	if a == nil {
		return c
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.subs = append(a.subs, c)
	return c
}
