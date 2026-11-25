package alarms

import (
	"slices"
	"time"
)

// Snooze puts off the ring that kind and id name, or any when both are empty, by minutes (0: the config's): the one
// that sounds or waits, which stops, else the one that stopped within SnoozeWindow. It rings again then at its own
// level; its schedule stays. ErrNothing when there is none.
func (r *Ringer) Snooze(kind Kind, id string, minutes int) (Snooze, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		if due, ok := r.take(kind, id, StopSnooze); ok {
			due.AutoSnoozes = 0
			return r.book.SnoozeRing(due, minutes)
		}
	}
	return r.book.SnoozeLast(kind, id, minutes)
}

// SnoozeStopped snoozes the ring a press of the action button just stopped, by the config's minutes: the button held
// down. Nothing when the last ring stopped otherwise.
func (r *Ringer) SnoozeStopped() {
	last, ok := r.book.Last()
	if !ok || last.StoppedBy != StopButton {
		return
	}
	_, _ = r.book.SnoozeLast(last.Kind, last.ID, 0)
}

// LastStopped is the ring that last stopped, and how long ago by the ringer's clock.
func (r *Ringer) LastStopped() (LastRing, time.Duration, bool) {
	last, ok := r.book.Last()
	if !ok {
		return LastRing{}, 0, false
	}
	return last, r.now().Sub(last.StoppedAt), true
}

// StopFor stops the ring kind and id name, or the one that sounds when both are empty, for a stop_ring command; one
// that waits is dropped. It reports the ring stopped.
func (r *Ringer) StopFor(kind Kind, id string) (Ring, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Ring{}, false
	}
	return r.take(kind, id, StopRemote)
}

// take stops the ring that sounds when it matches, else drops the first that waits and matches. callers hold mu
func (r *Ringer) take(kind Kind, id string, reason StopReason) (Ring, bool) {
	match := func(x Ring) bool { return (kind == "" || x.Kind == kind) && (id == "" || x.ID == id) }
	now := r.now()
	if c := r.cur; c != nil && match(c.Ring) {
		due := c.Ring
		r.stop(reason, now)
		r.wake()
		return due, true
	}
	if kind == "" && id == "" {
		return Ring{}, false
	}
	i := slices.IndexFunc(r.waiting, match)
	if i < 0 {
		return Ring{}, false
	}
	due := r.waiting[i]
	r.waiting = slices.Delete(r.waiting, i, i+1)
	r.log.Info("ring_stop", "kind", string(due.Kind), "id", due.ID, "reason", string(reason))
	r.book.Stopped(due, reason, now)
	return due, true
}
