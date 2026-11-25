package hermes

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"git.shino.boo/devilreef/cadence/server/internal/tasks"
)

// job states; stopping and pausing wait for the run to end after a stop
const (
	stateActive   = "active"
	statePausing  = "pausing"
	statePaused   = "paused"
	stateStopping = "stopping"
	stateClosed   = "closed"
)

const saveTimeout = 5 * time.Second

// job is one task's run as the kind follows it. lock serializes every
// operation on it, upstream calls included; all fields need it.
type job struct {
	lock      chan struct{}
	following atomic.Bool

	session string
	task    uuid.UUID
	run     string
	gen     int32
	// tried is the highest generation submitted: a failed resume never reuses its key
	tried int32
	state string
	// shown is the prompt the task waits on, as the user saw it
	shown *Control
	// refusal is the code refusing an answer to shown from Cadence, if any
	refusal string
	seen    time.Time
	// no new prompt shows before quiet: the manager first takes the answer
	quiet    time.Time
	answered map[string]bool
	phase    string
	// tool is the run's current tool as last reported, "" for none
	tool string
	// stopSent: the stop of a pause or cancel reached Hermes
	stopSent bool

	polls        int
	failures     int
	failingSince time.Time
	started      time.Time
}

func newJob(session string, task uuid.UUID, now time.Time) *job {
	return &job{lock: make(chan struct{}, 1), session: session, task: task, answered: map[string]bool{}, started: now}
}

func (j *job) acquire(ctx context.Context) error {
	select {
	case j.lock <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fail(CodeBusy)
	}
}

func (j *job) release() { <-j.lock }

func sessionID(task uuid.UUID) string { return "cadence-" + task.String() }

// idempotencyKey is fixed per task and run generation, so a retried
// submission never makes a second run.
func idempotencyKey(task uuid.UUID, gen int32) string { return fmt.Sprintf("cadence-%s-%d", task, gen) }

// job finds the task's run: followed already, or loaded from the store and
// followed from now on (a restart's re-attach). A closed run is loaded but not
// followed.
func (k *Kind) job(ctx context.Context, ref string) (*job, error) {
	k.mu.Lock()
	j, ok := k.jobs[ref]
	k.mu.Unlock()
	if ok {
		return j, nil
	}
	r, err := k.store.get(ctx, ref)
	if errors.Is(err, errNoRow) {
		return nil, fail(CodeNotFound)
	}
	if err != nil {
		return nil, err
	}
	j = newJob(r.session, r.task, k.now())
	j.run, j.gen, j.tried, j.shown, j.seen = r.run, r.gen, max(r.tried, r.gen), r.control, r.shownAt
	if j.shown != nil {
		_, j.refusal = question(*j.shown)
		if j.seen.IsZero() {
			j.seen = k.now()
		}
	}
	switch r.phase {
	case phaseClosed:
		j.state = stateClosed
		return j, nil
	case phasePaused:
		// stopped, or being stopped, to wait for the user: make sure it ends
		j.state = statePausing
	default:
		j.state = stateActive
	}
	k.mu.Lock()
	if cur, ok := k.jobs[ref]; ok {
		k.mu.Unlock()
		return cur, nil
	}
	k.jobs[ref] = j
	k.mu.Unlock()
	k.log.Info("hermes_reattached", "task", j.task.String(), "state", j.state)
	k.follow(j)
	return j, nil
}

// follow polls j, one loop at a time, until its run ends or pauses.
func (k *Kind) follow(j *job) {
	if j.following.CompareAndSwap(false, true) && !k.spawn(func() { k.loop(j) }) {
		j.following.Store(false)
	}
}

// spawn runs fn in the kind's lifetime; false once the kind stopped.
func (k *Kind) spawn(fn func()) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.ctx.Err() != nil {
		return false
	}
	k.wg.Add(1)
	go func() {
		defer k.wg.Done()
		fn()
	}()
	return true
}

func (k *Kind) loop(j *job) {
	delay := k.d.PollEvery
	for {
		t := time.NewTimer(delay)
		select {
		case <-k.ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if j.acquire(k.ctx) != nil {
			return
		}
		next, done := k.poll(j)
		if done {
			j.following.Store(false)
		}
		j.release()
		if done {
			return
		}
		delay = next
	}
}

// poll takes one look at the run; done means stop following it.
func (k *Kind) poll(j *job) (time.Duration, bool) {
	if j.state == stateClosed || j.state == statePaused {
		return 0, true
	}
	select {
	case k.sem <- struct{}{}:
	case <-k.ctx.Done():
		return 0, true
	}
	run, err := k.d.Client.GetRun(k.ctx, j.run)
	<-k.sem
	j.polls++
	if err != nil {
		return k.pollFailed(j, err)
	}
	j.failures, j.failingSince = 0, time.Time{}
	if run.Status != statusRunning {
		k.finish(j, run)
		return 0, true
	}
	switch j.state {
	case stateStopping:
		// the stop is on its way and the end decides; one cut short is sent again
		if !j.stopSent {
			k.restop(j)
		}
	case statePausing:
		if !j.stopSent {
			k.pause(j)
		}
	default:
		k.reconcile(j, run)
		if k.pauseDue(j) {
			k.pause(j)
		}
	}
	return k.d.PollEvery, j.state == stateClosed || j.state == statePaused
}

func (k *Kind) pollFailed(j *job, err error) (time.Duration, bool) {
	if k.ctx.Err() != nil {
		return 0, true
	}
	switch code := Code(err); code {
	case CodeGone:
		switch j.state {
		case statePausing:
			k.paused(j)
		case stateStopping:
			k.emit(tasks.Event{Ref: j.session, Kind: "cancelled"})
			k.close(j, "cancelled")
		default:
			k.lost(j, CodeRunGone)
		}
		return 0, true
	case CodeUnknown:
		k.lost(j, CodeRunUnknown)
		return 0, true
	case CodeIncompatible:
		k.latch()
		k.lost(j, CodeIncompatible)
		return 0, true
	}
	now := k.now()
	if j.failingSince.IsZero() {
		j.failingSince = now
	}
	j.failures++
	if now.Sub(j.failingSince) >= k.d.Unconfirmed {
		k.lost(j, CodeUnconfirmed)
		return 0, true
	}
	k.log.Warn("hermes_poll_failed", "task", j.task.String(), "code", Code(err), "failures", j.failures)
	return k.backoff(j.failures-1, retryAfter(err)), false
}

// finish folds a run's end into the task. A pause's own stop is no end: the
// task keeps waiting on its prompt.
func (k *Kind) finish(j *job, run Run) {
	stopped := run.Status == statusCancelled || run.Status == statusInterrupted
	switch {
	case stopped && j.state == statePausing:
		k.paused(j)
		return
	case run.Status == statusCompleted:
		k.emit(tasks.Event{Ref: j.session, Kind: "done", Result: head(run.Output, tasks.MaxResult)})
	case run.Status == statusFailed:
		k.emit(tasks.Event{Ref: j.session, Kind: "failed", ErrorCode: CodeFailed})
	case run.Status == statusCancelled || (stopped && j.state == stateStopping):
		k.emit(tasks.Event{Ref: j.session, Kind: "cancelled"})
	default:
		k.emit(tasks.Event{Ref: j.session, Kind: "failed", ErrorCode: CodeInterrupted})
	}
	k.close(j, run.Status)
}

func (k *Kind) pauseDue(j *job) bool {
	if j.state != stateActive || j.shown == nil || j.refusal != "" || j.shown.TimeoutS <= 0 {
		return false
	}
	timeout := time.Duration(j.shown.TimeoutS * float64(time.Second))
	return !k.now().Before(j.seen.Add(timeout - k.d.PauseLead))
}

// pause stops a run whose prompt Hermes would soon give up on, keeping the
// prompt: a late answer resumes the task in a new run of the same session.
func (k *Kind) pause(j *job) {
	j.state = statePausing
	k.save(j)
	status, err := k.d.Client.StopRun(k.ctx, j.run)
	if k.ctx.Err() != nil {
		return
	}
	j.stopSent = true
	k.log.Info("hermes_stop_requested", "task", j.task.String(), "reason", "pause", "code", Code(err))
	switch {
	case Code(err) == CodeGone:
		k.paused(j)
	case err != nil:
		// a stop nobody confirmed: never resumed, never claimed stopped
		k.lost(j, CodeStopUnknown)
	case status == statusCancelled || status == statusInterrupted:
		k.paused(j)
	}
}

// restop sends again the stop of a cancel whose caller gave up before it
// reached Hermes; the manager was told nothing, so the end is reported here.
func (k *Kind) restop(j *job) {
	status, err := k.d.Client.StopRun(k.ctx, j.run)
	if k.ctx.Err() != nil {
		return
	}
	j.stopSent = true
	k.log.Info("hermes_stop_requested", "task", j.task.String(), "reason", "cancel_retry", "code", codeOf(err))
	switch {
	case Code(err) == CodeGone, err == nil && (status == statusCancelled || status == statusInterrupted):
		k.emit(tasks.Event{Ref: j.session, Kind: "cancelled"})
		k.close(j, "cancelled")
	case err != nil:
		k.lost(j, CodeStopUnknown)
	}
}

func (k *Kind) paused(j *job) {
	j.state = statePaused
	k.save(j)
	k.log.Info("hermes_task_paused", "task", j.task.String(), "gen", j.gen)
}

// lost makes the task unknown: its run can no longer be confirmed, and it is
// never restarted or replayed.
func (k *Kind) lost(j *job, code string) {
	k.emit(tasks.Event{Ref: j.session, Kind: "unknown", ErrorCode: code})
	k.log.Warn("hermes_run_unknown", "task", j.task.String(), "code", code)
	k.close(j, "unknown")
}

func (k *Kind) close(j *job, outcome string) {
	j.state, j.shown = stateClosed, nil
	k.save(j)
	k.mu.Lock()
	delete(k.jobs, j.session)
	k.mu.Unlock()
	k.log.Info("hermes_run_ended", "task", j.task.String(), "outcome", outcome, "polls", j.polls,
		"duration_ms", k.now().Sub(j.started).Milliseconds())
}

// save records j; a failure is logged, the run goes on in memory.
func (k *Kind) save(j *job) {
	if k.persist(j) != nil {
		k.log.Error("hermes_store_failed", "task", j.task.String())
	}
}

func (k *Kind) persist(j *job) error {
	phase := phaseActive
	switch j.state {
	case statePausing, statePaused:
		phase = phasePaused
	case stateClosed:
		phase = phaseClosed
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(k.ctx), saveTimeout)
	defer cancel()
	return k.store.save(ctx, row{session: j.session, run: j.run, gen: j.gen, tried: j.tried, phase: phase, control: j.shown, shownAt: j.seen})
}

// backoff is the n-th retry's wait, at least Retry-After, at most the last step.
func (k *Kind) backoff(n int, retryAfter time.Duration) time.Duration {
	steps := k.d.Backoff
	return min(steps[len(steps)-1], max(steps[min(n, len(steps)-1)], retryAfter))
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
