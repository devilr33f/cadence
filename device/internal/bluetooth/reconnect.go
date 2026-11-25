package bluetooth

import (
	"context"
	"slices"
	"time"
)

const (
	defaultReconnectFirst = 2 * time.Second
	defaultReconnectMax   = 60 * time.Second
	defaultReconnectFor   = 10 * time.Minute
)

// reconnector brings the known speaker back once the adapter is up: at start and each time it is powered again, by
// Connect alone (never a pairing, never discoverable), with a backoff it gives up after; callers hold Bluetooth.mu
type reconnector struct {
	// the adapter was present and powered at the last reconcile
	up bool
	// a reconnect is wanted: since the adapter came up no speaker connected; a speaker seen again by discovery
	// starts it over while wanted, so one the owner disconnected is left alone
	pending bool
	// attempts run: pending and not given up
	armed bool
	// the next attempt may start
	due      bool
	inflight bool
	attempt  int
	// the backoff waited so far, against ReconnectFor
	waited time.Duration
	// changes with each arm and reset, so a stale attempt or timer changes nothing
	gen   int
	timer *time.Timer
	// the target was seen by discovery (an rssi) at the last reconcile
	seen bool
}

func (r *reconnector) stopTimer() {
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
}

// reset forgets everything but an attempt still in flight: the adapter or the backend went away
func (r *reconnector) reset() {
	r.stopTimer()
	*r = reconnector{gen: r.gen + 1, inflight: r.inflight}
}

func (r *reconnector) arm() {
	r.stopTimer()
	r.pending, r.armed, r.due, r.attempt, r.waited = true, true, true, 0, 0
	r.gen++
}

func (r *reconnector) disarm() {
	r.stopTimer()
	r.pending, r.armed, r.due = false, false, false
	r.gen++
}

// reconnectTarget is the speaker to bring back: the config's, else the most recently active one, of the bonded and
// trusted speakers bluez knows
func (o *output) reconnectTarget(snap Snapshot) (Device, bool) {
	want := make([]string, 0, len(o.known)+1)
	if o.speaker != "" {
		want = append(want, o.speaker)
	}
	for i := len(o.known) - 1; i >= 0; i-- {
		want = append(want, o.known[i])
	}
	for _, id := range want {
		if d, ok := byID(snap, id); ok && d.Paired && d.Trusted && isSpeaker(d) {
			return d, true
		}
	}
	return Device{}, false
}

// reconnect arms at start and on each power-up of the adapter, stops once any speaker is connected, and starts the
// next attempt when it is due; only Run's goroutine calls it
func (b *Bluetooth) reconnect(ctx context.Context, be Backend, snap Snapshot) {
	o := b.out
	if o == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	r := &o.rc
	a := snap.Adapter
	if a == nil || !a.Powered || !b.cfg.Enabled {
		if r.up {
			r.reset()
		}
		return
	}
	if !r.up {
		r.up = true
		r.arm()
	}
	if slices.ContainsFunc(snap.Devices, func(d Device) bool { return d.Connected && isSpeaker(d) }) {
		if r.pending {
			r.disarm()
		}
		return
	}
	d, ok := o.reconnectTarget(snap)
	seen := ok && d.RSSI != 0
	if seen && !r.seen && r.pending {
		// in range again: start over, now
		r.arm()
	}
	r.seen = seen
	if !ok || !r.armed || !r.due || r.inflight {
		return
	}
	r.due, r.inflight = false, true
	r.attempt++
	b.attempts.Add(1)
	go b.attemptReconnect(ctx, be, d.Path, ShortID(a.Address, d.Address), r.gen, r.attempt)
}

func (b *Bluetooth) attemptReconnect(ctx context.Context, be Backend, path, id string, gen, attempt int) {
	defer b.attempts.Done()
	err := be.Connect(ctx, path)
	b.mu.Lock()
	o := b.out
	r := &o.rc
	r.inflight = false
	current := gen == r.gen
	code, gaveUp := "ok", false
	switch {
	case err == nil:
		if current {
			r.pending, r.armed = false, false
		}
	case ctx.Err() != nil:
		code = "cancelled"
	default:
		code = "connect_failed"
		if !current {
			break
		}
		delay := o.opts.ReconnectFirst
		for i := 1; i < attempt && delay < o.opts.ReconnectMax; i++ {
			delay *= 2
		}
		delay = min(delay, o.opts.ReconnectMax)
		if r.waited+delay > o.opts.ReconnectFor {
			r.armed, gaveUp = false, true
			break
		}
		r.waited += delay
		r.timer = time.AfterFunc(delay, func() {
			b.mu.Lock()
			if o.rc.gen == gen {
				o.rc.due = true
			}
			b.mu.Unlock()
			b.wake()
		})
	}
	b.mu.Unlock()
	b.log.Info("bluetooth_reconnect", "id", id, "attempt", attempt, "code", code)
	if gaveUp {
		b.log.Warn("bluetooth_reconnect", "id", id, "attempt", attempt, "code", "gave_up")
	}
	b.wake()
}
