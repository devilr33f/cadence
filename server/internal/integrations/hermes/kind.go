package hermes

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/tasks"
)

const (
	// Name is the kind's name in the task manager.
	Name = "hermes"
	// reapAttempts bounds the replays that look for a lost submission's run.
	reapAttempts = 5
)

type Deps struct {
	Client *Client
	Pool   *pgxpool.Pool
	Log    *slog.Logger
	Now    func() time.Time

	// zero values take the defaults
	PollEvery   time.Duration   // between polls of a run: 2 s
	MaxPolls    int             // polls in flight at most: 8
	Backoff     []time.Duration // after failed polls and submissions: 2, 4, 8, 16, 30 s
	StopWait    time.Duration   // how long a cancel waits to see its run end: 10 s
	PauseLead   time.Duration   // a prompt with a timeout pauses its run this long before: 15 s
	Unconfirmed time.Duration   // failing polls for this long make a run unknown: 23 h
	SubmitFor   time.Duration   // submission retries of one start or resume: 30 s
	Reprobe     time.Duration   // between re-probes of a server found incompatible: 10 min
	MaxRuns     int             // runs going at once: the client's, else 4
}

// Kind runs tasks on Hermes, one session per task. A task's ref is its
// session; the session's current run lives in the store, so a restart picks
// every open task up again. Task results are external agent output: callers
// treat them as untrusted.
type Kind struct {
	d     Deps
	log   *slog.Logger
	store *store
	sem   chan struct{}
	ch    chan tasks.Event

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu           sync.Mutex
	jobs         map[string]*job
	ready        bool
	incompatible bool
	// starting counts Starts past the capacity check, not yet followed
	starting int

	emitMu sync.RWMutex
	closed bool
}

var _ tasks.Approver = (*Kind)(nil)

func NewKind(d Deps) *Kind {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.PollEvery <= 0 {
		d.PollEvery = 2 * time.Second
	}
	if d.MaxPolls <= 0 {
		d.MaxPolls = 8
	}
	if len(d.Backoff) == 0 {
		d.Backoff = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}
	}
	if d.StopWait <= 0 {
		d.StopWait = 10 * time.Second
	}
	if d.PauseLead <= 0 {
		d.PauseLead = 15 * time.Second
	}
	if d.Unconfirmed <= 0 {
		d.Unconfirmed = 23 * time.Hour
	}
	if d.SubmitFor <= 0 {
		d.SubmitFor = 30 * time.Second
	}
	if d.Reprobe <= 0 {
		d.Reprobe = 10 * time.Minute
	}
	if d.MaxRuns <= 0 && d.Client != nil {
		d.MaxRuns = d.Client.MaxRuns()
	}
	if d.MaxRuns <= 0 {
		d.MaxRuns = defaultMaxRuns
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Kind{
		d: d, log: d.Log, store: newStore(d.Pool), sem: make(chan struct{}, d.MaxPolls), ch: make(chan tasks.Event, 64),
		ctx: ctx, cancel: cancel, jobs: map[string]*job{},
	}
}

func (k *Kind) Name() string { return Name }

func (k *Kind) Events() <-chan tasks.Event { return k.ch }

func (k *Kind) now() time.Time { return k.d.Now() }

// Run probes the server until it is ready and keeps the kind's polls going
// until ctx ends; stop waits for them and closes Events. Remote runs are
// never stopped by a shutdown.
func (k *Kind) Run(ctx context.Context) (stop func()) {
	k.wg.Add(2)
	go func() {
		defer k.wg.Done()
		select {
		case <-ctx.Done():
			k.cancel()
		case <-k.ctx.Done():
		}
	}()
	go func() {
		defer k.wg.Done()
		k.probeUntilReady()
	}()
	return func() {
		k.mu.Lock()
		k.cancel()
		k.mu.Unlock()
		k.wg.Wait()
		k.emitMu.Lock()
		if !k.closed {
			k.closed = true
			close(k.ch)
		}
		k.emitMu.Unlock()
	}
}

func (k *Kind) emit(ev tasks.Event) {
	k.emitMu.RLock()
	defer k.emitMu.RUnlock()
	if k.closed {
		return
	}
	select {
	case k.ch <- ev:
	case <-k.ctx.Done():
	}
}

// probeUntilReady probes until the server answers, then re-probes slowly
// while it is found incompatible, so an upgraded server is taken again.
func (k *Kind) probeUntilReady() {
	for attempt := 0; ; {
		k.mu.Lock()
		ready, incompatible := k.ready, k.incompatible
		k.mu.Unlock()
		wait := k.d.Reprobe
		if !ready || incompatible {
			err := k.d.Client.Probe(k.ctx)
			if k.ctx.Err() != nil {
				return
			}
			switch {
			case err == nil, incompatible, Code(err) == CodeIncompatible:
				_ = k.probed(err)
			default:
				_ = k.probed(err)
				wait = k.backoff(attempt, retryAfter(err))
				attempt++
			}
		}
		if !sleep(k.ctx, wait) {
			return
		}
	}
}

// ensureReady probes the server once if no probe has passed yet; a server
// found incompatible is refused until a re-probe takes it again.
func (k *Kind) ensureReady(ctx context.Context) error {
	k.mu.Lock()
	ready, incompatible := k.ready, k.incompatible
	k.mu.Unlock()
	switch {
	case incompatible:
		return fail(CodeIncompatible)
	case ready:
		return nil
	}
	return k.probed(k.d.Client.Probe(ctx))
}

func (k *Kind) probed(err error) error {
	switch {
	case err == nil:
		k.mu.Lock()
		first := !k.ready || k.incompatible
		k.ready, k.incompatible = true, false
		k.mu.Unlock()
		if first {
			k.log.Info("hermes_ready")
		}
		return nil
	case Code(err) == CodeIncompatible:
		k.latch()
	default:
		k.log.Warn("hermes_probe_failed", "code", codeOf(err))
	}
	return asCode(err)
}

func (k *Kind) latch() {
	k.mu.Lock()
	first := !k.incompatible
	k.incompatible = true
	k.mu.Unlock()
	if first {
		k.log.Error("hermes_incompatible")
	}
}

// Start submits the task as the first run of its own session. A retry after
// a lost answer reuses the key and body, so Hermes makes one run.
func (k *Kind) Start(ctx context.Context, s tasks.Spec) (string, error) {
	if s.TaskID == uuid.Nil || strings.TrimSpace(s.Instructions) == "" {
		return "", fail(CodeBadInput)
	}
	if err := k.ensureReady(ctx); err != nil {
		return "", err
	}
	if err := k.reserve(); err != nil {
		return "", err
	}
	defer k.unreserve()
	j := newJob(sessionID(s.TaskID), s.TaskID, k.now())
	run, err := k.submit(ctx, j, s.Instructions, 1)
	if err != nil {
		return "", err
	}
	j.run, j.gen, j.tried, j.state = run, 1, 1, stateActive
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), saveTimeout)
	defer cancel()
	if err := k.store.create(sctx, row{session: j.session, task: j.task, run: run, gen: 1}); err != nil {
		// the task fails to start: its run must not go on unwatched
		k.stopUnwatched(run)
		return "", err
	}
	k.mu.Lock()
	k.jobs[j.session] = j
	k.mu.Unlock()
	k.follow(j)
	return j.session, nil
}

// reserve takes a run slot for a start: runs going (and starts under way)
// stay under the instance's cap.
func (k *Kind) reserve() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	going := k.starting
	for _, j := range k.jobs {
		if j.following.Load() { // a paused or ended run holds no slot
			going++
		}
	}
	if going >= k.d.MaxRuns {
		k.log.Warn("hermes_capacity", "runs", going)
		return fail(CodeCapacity)
	}
	k.starting++
	return nil
}

func (k *Kind) unreserve() {
	k.mu.Lock()
	k.starting--
	k.mu.Unlock()
}

// submit creates run gen of j's session, retrying lost answers with the same
// key and body.
func (k *Kind) submit(ctx context.Context, j *job, input string, gen int32) (string, error) {
	key, body := idempotencyKey(j.task, gen), submission(input, j.session)
	ctx, cancel := context.WithTimeout(ctx, k.d.SubmitFor)
	defer cancel()
	for attempt := 0; ; attempt++ {
		run, err := k.d.Client.CreateRun(ctx, body, key)
		if err == nil {
			k.log.Info("hermes_run_started", "task", j.task.String(), "gen", gen, "attempts", attempt+1)
			return run, nil
		}
		err = asCode(err)
		if !transient(err) || !sleep(ctx, k.backoff(attempt, retryAfter(err))) {
			k.log.Warn("hermes_submit_failed", "task", j.task.String(), "gen", gen, "code", Code(err), "attempts", attempt+1)
			if transient(err) {
				k.reap(body, key)
			}
			return "", err
		}
		k.log.Warn("hermes_submit_retry", "task", j.task.String(), "gen", gen, "code", Code(err))
	}
}

// reap stops the run a lost submission may have made, as nothing watches it:
// a replay of its key finds that run (or makes it) and it is stopped at once.
func (k *Kind) reap(body []byte, key string) {
	k.spawn(func() {
		for attempt := range reapAttempts {
			if !sleep(k.ctx, k.backoff(attempt, 0)) {
				return
			}
			run, err := k.d.Client.CreateRun(k.ctx, body, key)
			if err == nil {
				k.stopUnwatched(run)
				return
			}
			if !transient(err) {
				return
			}
		}
	})
}

func (k *Kind) stopUnwatched(run string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(k.ctx), saveTimeout)
	defer cancel()
	_, err := k.d.Client.StopRun(ctx, run)
	k.log.Warn("hermes_unwatched_stopped", "code", codeOf(err))
}

// Cancel stops the task's run and returns once Hermes shows it ended. A stop
// Hermes does not answer makes the task unknown; one it takes but does not
// finish in time is no cancel yet, and the polls report its end.
func (k *Kind) Cancel(ctx context.Context, ref string) error {
	j, err := k.job(ctx, ref)
	if err != nil {
		return err
	}
	if err := j.acquire(ctx); err != nil {
		return err
	}
	defer j.release()
	switch j.state {
	case stateClosed:
		return k.stopClosed(ctx, j)
	case statePaused:
		k.close(j, "cancelled")
		return nil
	}
	j.state, j.stopSent = stateStopping, false
	status, err := k.d.Client.StopRun(ctx, j.run)
	j.stopSent = ctx.Err() == nil
	k.log.Info("hermes_stop_requested", "task", j.task.String(), "reason", "cancel", "code", codeOf(err))
	switch {
	case Code(err) == CodeGone:
		k.close(j, "cancelled")
		return nil
	case err != nil && ctx.Err() != nil:
		k.follow(j)
		return fail(CodeTimeout)
	case err != nil:
		k.lost(j, CodeStopUnknown)
		return fail(CodeStopUnknown)
	case status == statusCancelled || status == statusInterrupted:
		k.close(j, "cancelled")
		return nil
	}
	return k.awaitStop(ctx, j)
}

func (k *Kind) awaitStop(ctx context.Context, j *job) error {
	deadline := time.Now().Add(k.d.StopWait)
	for {
		run, err := k.d.Client.GetRun(ctx, j.run)
		switch {
		case err == nil && (run.Status == statusCancelled || run.Status == statusInterrupted), Code(err) == CodeGone:
			k.close(j, "cancelled")
			return nil
		case err == nil && run.Status != statusRunning:
			// it ended on its own before the stop landed
			k.finish(j, run)
			return fail(CodeFinished)
		case Code(err) == CodeUnknown, Code(err) == CodeIncompatible:
			k.lost(j, CodeRunUnknown)
			return fail(CodeRunUnknown)
		}
		left := time.Until(deadline)
		if left <= 0 || !sleep(ctx, min(k.d.PollEvery, left)) {
			k.follow(j)
			return fail(CodeTimeout)
		}
	}
}

// stopClosed stops the run of a task the kind no longer follows (an unknown
// one), best effort: only a confirmed end counts.
func (k *Kind) stopClosed(ctx context.Context, j *job) error {
	status, err := k.d.Client.StopRun(ctx, j.run)
	k.log.Info("hermes_stop_requested", "task", j.task.String(), "reason", "cancel_closed", "code", codeOf(err))
	switch {
	case Code(err) == CodeGone, err == nil && (status == statusCancelled || status == statusInterrupted):
		return nil
	case err == nil && (status == statusCompleted || status == statusFailed):
		return fail(CodeFinished)
	case err != nil:
		return asCode(err)
	}
	return fail(CodeTimeout)
}

// Inspect shows the task as Hermes has it now; when Hermes cannot be reached,
// as the kind last knew it. Its transcript is the run's output so far.
func (k *Kind) Inspect(ctx context.Context, ref string, d tasks.Detail) (tasks.Snapshot, error) {
	j, err := k.job(ctx, ref)
	if Code(err) == CodeNotFound {
		return tasks.Snapshot{Status: tasks.StatusUnknown, ErrorCode: CodeNotFound}, nil
	}
	if err != nil {
		return tasks.Snapshot{}, err
	}
	if err := j.acquire(ctx); err != nil {
		return tasks.Snapshot{}, err
	}
	defer j.release()
	known := k.known(j)
	paused := j.state == statePaused || j.state == statePausing
	if paused && d != tasks.DetailTranscript {
		return known, nil
	}
	run, err := k.d.Client.GetRun(ctx, j.run)
	switch Code(err) {
	case "":
	case CodeGone:
		if paused {
			return known, nil
		}
		return tasks.Snapshot{Status: tasks.StatusUnknown, ErrorCode: CodeRunGone}, nil
	case CodeUnknown, CodeIncompatible:
		return tasks.Snapshot{Status: tasks.StatusUnknown, ErrorCode: CodeRunUnknown}, nil
	default:
		return known, nil
	}
	s := known
	if !paused {
		s = k.runSnapshot(j, run)
	}
	if d == tasks.DetailTranscript {
		s.Transcript = tail(run.Output, tasks.MaxResult)
	}
	return s, nil
}

// known is the task as the kind last saw it.
func (k *Kind) known(j *job) tasks.Snapshot {
	switch {
	case j.state == stateClosed:
		return tasks.Snapshot{Status: tasks.StatusUnknown, ErrorCode: CodeUnconfirmed}
	case j.shown != nil:
		q, _ := question(*j.shown)
		return tasks.Snapshot{Status: tasks.StatusWaitingInput, Question: &q, Progress: progressOf(j.phase, j.tool)}
	}
	return tasks.Snapshot{Status: tasks.StatusRunning, Progress: progressOf(j.phase, j.tool)}
}

func (k *Kind) runSnapshot(j *job, run Run) tasks.Snapshot {
	switch run.Status {
	case statusRunning:
		if j.shown != nil {
			if live, ok := find(run.Controls, *j.shown); ok && live.canonical() == j.shown.canonical() {
				return k.known(j)
			}
		}
		return tasks.Snapshot{Status: tasks.StatusRunning, Progress: progressOf(run.Phase, run.Tool)}
	case statusCompleted:
		return tasks.Snapshot{Status: tasks.StatusDone, Result: head(run.Output, tasks.MaxResult)}
	case statusFailed:
		return tasks.Snapshot{Status: tasks.StatusFailed, ErrorCode: CodeFailed}
	case statusCancelled:
		return tasks.Snapshot{Status: tasks.StatusCancelled}
	}
	if j.state == stateStopping {
		return tasks.Snapshot{Status: tasks.StatusCancelled}
	}
	return tasks.Snapshot{Status: tasks.StatusFailed, ErrorCode: CodeInterrupted}
}

// tail keeps the newest n bytes of s, cut on a rune boundary.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

// head keeps the first n bytes of s, cut on a rune boundary.
func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 {
		if r, size := utf8.DecodeLastRuneInString(s); r != utf8.RuneError || size > 1 {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

// asCode turns a ctx error into a code; codes pass as they are.
func asCode(err error) error {
	if err == nil || Code(err) != "" {
		return err
	}
	return fail(CodeTimeout)
}

func codeOf(err error) string {
	if err == nil {
		return "ok"
	}
	return Code(asCode(err))
}
