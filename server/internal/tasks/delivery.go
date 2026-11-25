package tasks

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// noticeTries bounds the writes of one delivery to the store.
const noticeTries = 3

// device is one device's reporting state, under Reporter.mu; its worker owns
// the passes.
type device struct {
	id   string
	kick chan struct{} // cap 1
	// queue holds the tasks owed an event turn, oldest first
	queue []string
	// busy is set by a kick and cleared only while the worker waits with no
	// kick pending
	busy    bool
	running bool
	// awakeUntil: the user began a session lately, so quiet hours yield;
	// settleUntil: a session ended or the device linked just now
	awakeUntil, settleUntil time.Time
	// retryAt and backoff pace announcements after a failed one
	retryAt time.Time
	backoff time.Duration
}

// SessionEnded hears a voice session of the device end; heard says a user
// turn ran in it. One the user began with the wake word or a press and spoke
// in means they are awake, quiet hours or not, and so do its follow-ups they
// spoke in; a false wake at night wakes no one. Whatever waited for the
// session goes once its end settled.
func (r *Reporter) SessionEnded(deviceID string, trigger cadencev2.Trigger, heard bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.deviceLocked(deviceID)
	now := r.d.Now()
	d.awakeUntil = AwakeUntil(trigger, heard, now, d.awakeUntil, r.lim.awake)
	d.settleUntil = now.Add(r.lim.settle)
	r.kickLocked(d, true)
}

// AwakeUntil is until when a device's user counts as awake once a session
// of trigger ended at now, given until, what it was before: a session the
// user began with the wake word or a press and spoke in keeps them awake for
// awake, and so does a follow-up they spoke in while they were; a false wake
// wakes no one. Quiet hours yield while the user is awake.
func AwakeUntil(trigger cadencev2.Trigger, heard bool, now, until time.Time, awake time.Duration) time.Time {
	switch {
	case !heard:
	case trigger == cadencev2.Trigger_TRIGGER_WAKE || trigger == cadencev2.Trigger_TRIGGER_BUTTON:
		return now.Add(awake)
	case trigger == cadencev2.Trigger_TRIGGER_FOLLOW_UP && now.Before(until):
		return now.Add(awake)
	}
	return until
}

// BusyChanged hears the device's busy state change: what busy mode held may
// go now.
func (r *Reporter) BusyChanged(deviceID string) { r.kick(deviceID, false) }

// AnnounceEnded hears an announcement on the device end: the next update may go.
func (r *Reporter) AnnounceEnded(deviceID string) { r.kick(deviceID, true) }

// Connected hears the device link; its updates go once it settled.
func (r *Reporter) Connected(deviceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.deviceLocked(deviceID)
	d.settleUntil = r.d.Now().Add(r.lim.settle)
	r.kickLocked(d, true)
}

func (r *Reporter) kick(deviceID string, fresh bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kickLocked(r.deviceLocked(deviceID), fresh)
}

// kickLocked has the device's worker look again; fresh (a session or an
// announcement ended, a link came) drops the backoff, since what failed may
// go now.
func (r *Reporter) kickLocked(d *device, fresh bool) {
	if fresh {
		d.retryAt, d.backoff = time.Time{}, 0
	}
	d.busy = true
	select {
	case d.kick <- struct{}{}:
	default:
	}
	r.startLocked(d)
}

func (r *Reporter) deviceLocked(id string) *device {
	d := r.devs[id]
	if d == nil {
		d = &device{id: id, kick: make(chan struct{}, 1)}
		r.devs[id] = d
	}
	return d
}

// startLocked starts the device's worker once Run began.
func (r *Reporter) startLocked(d *device) {
	if r.ctx == nil || d.running || r.ctx.Err() != nil {
		return
	}
	d.running = true
	ctx := r.ctx
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.work(ctx, d)
	}()
}

// work runs the device's passes: on a kick, and when a pass asks to look
// again later.
func (r *Reporter) work(ctx context.Context, d *device) {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer func() {
		timer.Stop()
		r.mu.Lock()
		d.running, d.busy = false, false
		r.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.kick:
		case <-timer.C:
		}
		r.mu.Lock()
		d.busy = true
		r.mu.Unlock()
		wait := r.pass(ctx, d)
		if wait > 0 {
			timer.Reset(wait)
		} else {
			timer.Stop()
		}
		r.mu.Lock()
		if len(d.kick) == 0 {
			d.busy = false
		}
		r.mu.Unlock()
	}
}

// pass runs what the device can take now: its event turns while no voice
// turn runs there, then its updates. It returns when to look again, 0 for at
// the next kick.
func (r *Reporter) pass(ctx context.Context, d *device) time.Duration {
	if wait := r.settling(d); wait > 0 {
		return wait
	}
	for ctx.Err() == nil && !r.d.Voice.SessionActive(d.id) {
		id, ok := r.pop(d)
		if !ok {
			break
		}
		r.report(ctx, d.id, id)
	}
	return r.deliver(ctx, d)
}

func (r *Reporter) settling(d *device) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return d.settleUntil.Sub(r.d.Now())
}

// deliver announces the device's updates, oldest first, one at a time: each
// waits for the device to be online, without a session or an announcement
// under way, out of quiet hours unless the user is awake, and past any
// backoff. A session, an announcement's end or a link kicks the next look;
// quiet hours wait for the sweep.
func (r *Reporter) deliver(ctx context.Context, d *device) time.Duration {
	store := r.d.Manager.d.Store
	dev, err := uuid.Parse(d.id)
	if err != nil {
		return 0
	}
	for ctx.Err() == nil {
		if !r.d.Online(d.id) || r.d.Voice.SessionActive(d.id) || r.d.Voice.Announcing(d.id) {
			return 0
		}
		if wait := r.backingOff(d); wait > 0 {
			return wait
		}
		n, err := store.NextNotice(ctx, dev)
		switch {
		case errors.Is(err, ErrNotFound):
			return 0
		case err != nil:
			r.storeFailed(ctx, "deliver", err)
			return r.retryLater(d)
		}
		key := eventKey(n.ShortID, n.EventSeq)
		if r.wasSpoken(key) {
			if !r.delivered(ctx, n) {
				return r.retryLater(d)
			}
			continue
		}
		if n.Stale {
			r.d.Log.Info("task_notice_stale", "short_id", n.ShortID, "seq", n.EventSeq, "device_id", d.id)
			// its event counts as told: a status a newer event left showing (a
			// question answered, its resume not reported yet) runs no turn again
			if err := store.MarkReportedAt(ctx, n.ShortID, n.EventSeq); err != nil {
				r.storeFailed(ctx, "mark", err)
				return r.retryLater(d)
			}
			if err := store.DropNotice(ctx, n.TaskID, n.EventSeq); err != nil {
				r.storeFailed(ctx, "drop", err)
				return r.retryLater(d)
			}
			continue
		}
		if !r.awakeNow(ctx, d) {
			return 0
		}
		if err := r.d.Voice.Announce(ctx, d.id, n.Text); err != nil {
			if ctx.Err() != nil {
				return 0
			}
			wait := r.retryLater(d)
			r.d.Log.Warn("task_notice_failed", "short_id", n.ShortID, "seq", n.EventSeq, "device_id", d.id, "retry_ms", wait.Milliseconds())
			return wait
		}
		r.d.Log.Info("task_notice_announced", "short_id", n.ShortID, "seq", n.EventSeq, "device_id", d.id)
		r.markSpoken(key)
		r.resetBackoff(d)
		if !r.delivered(ctx, n) {
			return r.retryLater(d)
		}
	}
	return 0
}

// awakeNow reports whether an update may be spoken now: while the device is
// not busy (busy mode holds it, awake or not; its end kicks the device), and
// outside its quiet hours or while the user is awake. Settings that do not
// read hold it.
func (r *Reporter) awakeNow(ctx context.Context, d *device) bool {
	if r.d.Busy != nil && r.d.Busy(ctx, d.id) {
		return false
	}
	v, err := r.d.Settings(ctx, d.id)
	if err != nil {
		if ctx.Err() == nil {
			r.d.Log.Error("task_report_failed", "op", "settings", "device_id", d.id)
		}
		return false
	}
	now := r.d.Now()
	if !v.QuietHours.Quiet(now, v.TimeZone) {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return now.Before(d.awakeUntil)
}

// delivered records a spoken notice and reports its event together, trying
// the store a few times; false when it did not store, and the run still never
// speaks the notice again.
func (r *Reporter) delivered(ctx context.Context, n Notice) bool {
	if err := tries(ctx, func() error { return r.d.Manager.d.Store.Delivered(ctx, n) }); err != nil {
		r.storeFailed(ctx, "delivered", err)
		return false
	}
	r.forgetSpoken(eventKey(n.ShortID, n.EventSeq))
	return true
}

// tries runs a store write up to noticeTries times, until it succeeds or ctx ends.
func tries(ctx context.Context, write func() error) error {
	var err error
	for range noticeTries {
		if err = write(); err == nil || ctx.Err() != nil {
			break
		}
	}
	return err
}

func (r *Reporter) backingOff(d *device) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return d.retryAt.Sub(r.d.Now())
}

func (r *Reporter) resetBackoff(d *device) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d.retryAt, d.backoff = time.Time{}, 0
}

// retryLater backs the device's announcements off and returns the wait.
func (r *Reporter) retryLater(d *device) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	d.backoff = min(max(2*d.backoff, r.lim.backoff), r.lim.maxBackoff)
	d.retryAt = r.d.Now().Add(d.backoff)
	return d.backoff
}

func (r *Reporter) markSpoken(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spoken[key] = true
}

func (r *Reporter) forgetSpoken(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.spoken, key)
}

func (r *Reporter) wasSpoken(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.spoken[key]
}
