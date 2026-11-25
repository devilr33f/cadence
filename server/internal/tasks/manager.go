package tasks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"git.shino.boo/devilreef/cadence/server/internal/events"
)

const (
	retentionEvery = time.Hour
	inspectTimeout = 10 * time.Second
	maxParked      = 64
	parkedFor      = time.Minute
	// startCancelTimeout bounds stopping what a kind began for a task cancelled while it started
	startCancelTimeout = 30 * time.Second
)

type ManagerDeps struct {
	Store *Store
	Bus   *events.Bus
	Log   *slog.Logger
	Now   func() time.Time
}

// Manager starts tasks on their kinds, folds the kinds' events into the
// store and tells the bus. All state changes take mu, so a transition is
// always checked against the stored task it applies to.
type Manager struct {
	d     ManagerDeps
	kinds map[string]Kind

	mu     sync.Mutex
	parked []parked
}

// parked is an event whose ref no task has yet: a kind may report before
// Start returned its ref.
type parked struct {
	kind string
	ev   Event
	at   time.Time
}

func NewManager(d ManagerDeps) *Manager {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Manager{d: d, kinds: map[string]Kind{}}
}

// Register adds a kind; call it before Run.
func (m *Manager) Register(k Kind) {
	if _, dup := m.kinds[k.Name()]; dup {
		panic("tasks: kind registered twice: " + k.Name())
	}
	m.kinds[k.Name()] = k
}

// Kinds names the registered kinds, sorted.
func (m *Manager) Kinds() []string { return slices.Sorted(maps.Keys(m.kinds)) }

// Watch subscribes in process to task events (data: Live). The subscriber
// closes it. It is lossy: a slow subscriber loses its oldest events (see
// Subscription.Dropped), so a consumer reconciles through Unreported after a
// drop and at start, and marks what it handled with MarkReported.
func (m *Manager) Watch() *events.Subscription {
	return m.d.Bus.Watch(func(e events.Event) bool { return e.Type == events.TaskEvent })
}

// Run fans the kinds' events in, re-attaches non-terminal tasks once and
// prunes finished ones hourly, until ctx ends; stop waits for all of it.
func (m *Manager) Run(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for _, k := range m.kinds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.fanIn(ctx, k)
		}()
	}
	// only the tasks that exist now: later ones are not a restart's leftovers
	open, err := m.d.Store.Open(ctx)
	if err != nil {
		m.d.Log.Error("task_reattach_failed")
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.reattach(ctx, open)
	}()
	stopRetention := m.d.Store.RunRetention(ctx, m.d.Log, retentionEvery)
	return func() { cancel(); wg.Wait(); stopRetention() }
}

func (m *Manager) fanIn(ctx context.Context, k Kind) {
	ch := k.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			m.handle(ctx, k.Name(), ev)
		}
	}
}

func (m *Manager) handle(ctx context.Context, kind string, ev Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.d.Store.ByRef(ctx, kind, ev.Ref)
	switch {
	case errors.Is(err, ErrNotFound):
		m.park(kind, ev)
		return
	case err != nil:
		if ctx.Err() == nil {
			m.d.Log.Error("task_event_failed", "kind", kind, "event", ev.Kind)
		}
		return
	}
	m.applyEvent(ctx, t, ev)
}

func (m *Manager) park(kind string, ev Event) {
	now := m.d.Now()
	kept := m.parked[:0]
	for _, p := range m.parked {
		if now.Sub(p.at) < parkedFor {
			kept = append(kept, p)
		}
	}
	m.parked = kept
	if len(m.parked) >= maxParked {
		m.parked = m.parked[1:]
	}
	m.parked = append(m.parked, parked{kind: kind, ev: ev, at: now})
	m.d.Log.Warn("task_event_unmatched", "kind", kind, "event", ev.Kind)
}

// drain handles the parked events of a ref that just got its task; mu held.
func (m *Manager) drain(ctx context.Context, t Task) {
	var mine []Event
	kept := m.parked[:0]
	for _, p := range m.parked {
		if p.kind == t.Kind && p.ev.Ref == t.ExternalRef {
			mine = append(mine, p.ev)
			continue
		}
		kept = append(kept, p)
	}
	m.parked = kept
	for _, ev := range mine {
		cur, err := m.d.Store.Get(ctx, t.ShortID)
		if err != nil {
			return
		}
		m.applyEvent(ctx, cur, ev)
	}
}

// applyEvent stores and publishes one kind event; mu held.
func (m *Manager) applyEvent(ctx context.Context, t Task, ev Event) {
	u, evKind, err := nextState(t, ev)
	if err != nil {
		m.d.Log.Warn("task_event_rejected", "short_id", t.ShortID, "kind", t.Kind, "status", t.Status, "event", ev.Kind)
		return
	}
	_, _ = m.commit(ctx, t, u, evKind)
}

// commit writes u with its event row and publishes; mu held.
func (m *Manager) commit(ctx context.Context, t Task, u Update, evKind string) (Task, error) {
	got, seq, err := m.d.Store.Apply(ctx, t.ID, u, evKind)
	if err != nil && ctx.Err() == nil && !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrNotFound) {
		// one retry for a transient failure; a rejected row would fail again
		got, seq, err = m.d.Store.Apply(ctx, t.ID, u, evKind)
	}
	if err != nil {
		if ctx.Err() == nil {
			m.d.Log.Error("task_store_failed", "short_id", t.ShortID, "kind", t.Kind, "event", evKind, "code", errCode(err))
		}
		return Task{}, err
	}
	if evKind != "" {
		m.publish(got, evKind, seq)
	}
	attrs := []any{"short_id", got.ShortID, "kind", got.Kind, "status", got.Status, "event", evKind, "seq", seq}
	if got.ErrorCode != "" && got.Terminal() {
		attrs = append(attrs, "code", got.ErrorCode)
	}
	if got.Terminal() {
		attrs = append(attrs, "duration_ms", m.d.Now().Sub(got.CreatedAt).Milliseconds())
	}
	m.d.Log.Info("task_changed", attrs...)
	return got, nil
}

func (m *Manager) publish(t Task, evKind string, seq int) {
	dev := ""
	if t.Origin != uuid.Nil {
		dev = t.Origin.String()
	}
	m.d.Bus.Publish(events.Event{
		Type: events.TaskEvent, DeviceID: dev, At: m.d.Now(),
		Data: Live{ShortID: t.ShortID, TaskKind: t.Kind, Kind: evKind, Status: t.Status, Seq: seq},
	})
}

func titleOf(s Spec, kind string) string {
	title := strings.TrimSpace(noNUL(s.Title))
	if title == "" {
		for line := range strings.SplitSeq(s.Instructions, "\n") {
			if line = strings.TrimSpace(noNUL(line)); line != "" {
				title = line
				break
			}
		}
	}
	if title == "" {
		title = kind
	}
	return truncRunes(title, MaxTitle)
}

// Start stores a queued task, starts it on its kind and marks it running
// once the kind acks. A kind that fails to start leaves the task failed with
// code start_failed, returned with ErrStartFailed.
func (m *Manager) Start(ctx context.Context, kind string, spec Spec) (Task, error) {
	k, ok := m.kinds[kind]
	if !ok {
		return Task{}, ErrUnknownKind
	}
	spec.Title = titleOf(spec, kind)
	t, err := m.d.Store.Create(ctx, kind, spec.Title, spec.Origin)
	if err != nil {
		return Task{}, err
	}
	// the task is stored whatever the caller's ctx does from here on
	dbctx := context.WithoutCancel(ctx)
	spec.TaskID = t.ID
	ref, err := k.Start(ctx, spec)
	got, stop, err := m.ack(dbctx, t, ref, err)
	if stop {
		// cancelled while starting: stop what the kind just began, outside mu,
		// as the kind may report on it meanwhile
		cctx, cancel := context.WithTimeout(dbctx, startCancelTimeout)
		defer cancel()
		if cerr := k.Cancel(cctx, ref); cerr != nil {
			m.d.Log.Warn("task_cancel_failed", "short_id", t.ShortID, "kind", t.Kind, "code", kindError("cancel", cerr).Code)
		}
	}
	return got, err
}

// ack folds the kind's answer to Start into the task; stop reports a task
// cancelled meanwhile whose ref the kind must stop. A refused start carries
// the kind's code (a KindError) beside ErrStartFailed.
func (m *Manager) ack(ctx context.Context, t Task, ref string, startErr error) (Task, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if startErr != nil || ref == "" {
		code := "start_failed"
		failed, cerr := m.commit(ctx, t, Update{Status: StatusFailed, ErrorCode: &code, Finished: true, OnlyFrom: StatusQueued}, "failed")
		if errors.Is(cerr, ErrNotFound) {
			// cancelled while starting: that stands
			cur, gerr := m.d.Store.Get(ctx, t.ShortID)
			return cur, false, errors.Join(ErrStartFailed, gerr)
		}
		if cerr != nil {
			return Task{}, false, cerr
		}
		if startErr != nil {
			return failed, false, errors.Join(ErrStartFailed, kindError("start", startErr))
		}
		return failed, false, ErrStartFailed
	}
	running, err := m.commit(ctx, t, Update{Status: StatusRunning, ExternalRef: &ref, OnlyFrom: StatusQueued}, "progress")
	if errors.Is(err, ErrNotFound) {
		cur, gerr := m.d.Store.Get(ctx, t.ShortID)
		return cur, true, errors.Join(ErrNotActive, gerr)
	}
	if err != nil {
		return Task{}, false, err
	}
	m.drain(ctx, running)
	if cur, err := m.d.Store.Get(ctx, running.ShortID); err == nil {
		running = cur
	}
	return running, false, nil
}

func (m *Manager) Get(ctx context.Context, shortID string) (Task, error) {
	return m.d.Store.Get(ctx, shortID)
}

func (m *Manager) List(ctx context.Context, f Filter) ([]Task, error) {
	return m.d.Store.List(ctx, f)
}

func (m *Manager) kindOf(t Task) (Kind, error) {
	k, ok := m.kinds[t.Kind]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownKind, t.Kind)
	}
	return k, nil
}

// asked checks that token names the question a waiting task asks now.
func asked(t Task, token string) error {
	if t.Status == StatusWaitingInput && t.Question != nil && t.Question.Token != token {
		return ErrStale
	}
	return nil
}

// Send passes a message to a running or waiting task. Answering a waiting
// task resumes it; token is the question's, as the answer's giver saw it.
func (m *Manager) Send(ctx context.Context, shortID, token, msg string) (Task, error) {
	t, err := m.d.Store.Get(ctx, shortID)
	if err != nil {
		return Task{}, err
	}
	if t.Status != StatusRunning && t.Status != StatusWaitingInput {
		return Task{}, ErrNotActive
	}
	if err := asked(t, token); err != nil {
		return Task{}, err
	}
	k, err := m.kindOf(t)
	if err != nil {
		return Task{}, err
	}
	if err := k.Send(ctx, t.ExternalRef, token, msg); err != nil {
		ke := kindError("send", err)
		m.d.Log.Warn("task_send_failed", "short_id", t.ShortID, "kind", t.Kind, "code", ke.Code)
		return Task{}, ke
	}
	return m.answered(ctx, shortID, token, nil)
}

// Approve passes once or deny to a task waiting on the approval question
// token names, and keeps by on the task's answered event. Who may approve is
// the caller's policy; this only checks that the approval is pending and says
// who gave it, and hands the decision to the kind.
func (m *Manager) Approve(ctx context.Context, shortID, token, decision string, by Evidence) (Task, error) {
	if decision != DecisionOnce && decision != DecisionDeny {
		return Task{}, ErrInvalid
	}
	answer, ok := answerOf(decision, by)
	if !ok {
		return Task{}, ErrInvalid
	}
	t, err := m.d.Store.Get(ctx, shortID)
	if err != nil {
		return Task{}, err
	}
	if t.Status != StatusWaitingInput || t.Question == nil || t.Question.Kind != QuestionApproval {
		return Task{}, ErrNoApproval
	}
	if err := asked(t, token); err != nil {
		return Task{}, err
	}
	k, err := m.kindOf(t)
	if err != nil {
		return Task{}, err
	}
	a, ok := k.(Approver)
	if !ok {
		return Task{}, ErrNoApproval
	}
	if err := a.Approve(ctx, t.ExternalRef, token, decision); err != nil {
		ke := kindError("approve", err)
		m.d.Log.Warn("task_approve_failed", "short_id", t.ShortID, "kind", t.Kind, "code", ke.Code)
		return Task{}, ke
	}
	return m.answered(ctx, shortID, token, answer)
}

// answered resumes a task the kind took an answer to the question token
// names for, first keeping the answer's payload (when there is one) on an
// answered event: the kind may have reported the resume already, or even
// asked its next question, which the answer leaves asked. The store follows
// the kind whatever the caller's ctx does now.
func (m *Manager) answered(ctx context.Context, shortID, token string, answer []byte) (Task, error) {
	ctx = context.WithoutCancel(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, err := m.d.Store.Get(ctx, shortID)
	if err != nil {
		return Task{}, err
	}
	if answer != nil {
		if seq, err := m.d.Store.Answered(ctx, cur.ID, answer); err != nil {
			m.d.Log.Error("task_store_failed", "short_id", cur.ShortID, "kind", cur.Kind, "event", "answered", "code", errCode(err))
		} else {
			m.publish(cur, "answered", seq)
		}
	}
	got, err := m.resume(ctx, cur, token)
	if err == nil {
		m.noteOwnAnswer(ctx, got)
	}
	return got, err
}

// resume moves a task waiting on the question token names on; mu held.
func (m *Manager) resume(ctx context.Context, cur Task, token string) (Task, error) {
	if cur.Status != StatusWaitingInput || (cur.Question != nil && cur.Question.Token != token) {
		return cur, nil
	}
	u, evKind, err := nextState(cur, Event{Kind: "progress"})
	if err != nil {
		return cur, nil
	}
	return m.commit(ctx, cur, u, evKind)
}

// noteOwnAnswer records, for an event turn that answered its own task, the
// event the answer's resume made: the latest one of a task now running, as
// the kind may have reported the resume first. mu held.
func (m *Manager) noteOwnAnswer(ctx context.Context, t Task) {
	own := ownAnswersOf(ctx)
	if own == nil || t.Status != StatusRunning {
		return
	}
	if seq, err := m.d.Store.LatestSeq(ctx, t.ID); err == nil {
		own.note(t.ShortID, seq)
	}
}

// Cancel asks the kind to stop a task and marks it cancelled. A queued or
// unknown task has nothing to ask, so it is just marked.
func (m *Manager) Cancel(ctx context.Context, shortID string) (Task, error) {
	t, err := m.d.Store.Get(ctx, shortID)
	if err != nil {
		return Task{}, err
	}
	if t.Terminal() {
		return Task{}, ErrNotActive
	}
	switch {
	case t.Status == StatusRunning || t.Status == StatusWaitingInput:
		k, err := m.kindOf(t)
		if err != nil {
			return Task{}, err
		}
		if err := k.Cancel(ctx, t.ExternalRef); err != nil {
			ke := kindError("cancel", err)
			m.d.Log.Warn("task_cancel_failed", "short_id", t.ShortID, "kind", t.Kind, "code", ke.Code)
			return Task{}, ke
		}
	case t.Status == StatusUnknown && t.ExternalRef != "":
		// best effort: the kind may still hold it, but the owner's word stands either way
		if k, err := m.kindOf(t); err == nil {
			if err := k.Cancel(ctx, t.ExternalRef); err != nil {
				m.d.Log.Warn("task_cancel_failed", "short_id", t.ShortID, "kind", t.Kind)
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, err := m.d.Store.Get(ctx, shortID)
	if err != nil {
		return Task{}, err
	}
	if cur.Terminal() {
		return Task{}, ErrNotActive
	}
	u := keep(cur)
	u.Status, u.Finished = StatusCancelled, true
	return m.commit(ctx, cur, u, "cancelled")
}

// Inspect shows a task. A finished one answers from the store; others ask
// their kind.
func (m *Manager) Inspect(ctx context.Context, shortID string, d Detail) (Snapshot, error) {
	t, err := m.d.Store.Get(ctx, shortID)
	if err != nil {
		return Snapshot{}, err
	}
	if t.Terminal() && d != DetailTranscript {
		return Snapshot{Status: t.Status, Progress: t.Progress, Result: t.Result, ErrorCode: t.ErrorCode}, nil
	}
	k, err := m.kindOf(t)
	if err != nil {
		return Snapshot{}, err
	}
	if t.ExternalRef == "" {
		return Snapshot{Status: t.Status, Progress: t.Progress}, nil
	}
	s, err := k.Inspect(ctx, t.ExternalRef, d)
	if err != nil {
		m.d.Log.Warn("task_inspect_failed", "short_id", t.ShortID, "kind", t.Kind)
		return Snapshot{}, ErrInspect
	}
	return s, nil
}

// Reattach asks each kind about the non-terminal tasks left by a restart
// and folds the answers in. The kind decides whether it can resume; one that
// cannot, or has no answer, leaves the task unknown. Nothing is restarted or
// replayed.
func (m *Manager) Reattach(ctx context.Context) {
	open, err := m.d.Store.Open(ctx)
	if err != nil {
		if ctx.Err() == nil {
			m.d.Log.Error("task_reattach_failed")
		}
		return
	}
	m.reattach(ctx, open)
}

func (m *Manager) reattach(ctx context.Context, open []Task) {
	for _, t := range open {
		if ctx.Err() != nil {
			return
		}
		m.reattachOne(ctx, t)
	}
}

func (m *Manager) reattachOne(ctx context.Context, t Task) {
	ev := Event{Ref: t.ExternalRef, Kind: "unknown"}
	k, ok := m.kinds[t.Kind]
	switch {
	case !ok:
		ev.ErrorCode = "kind_missing"
	case t.ExternalRef == "":
		ev.ErrorCode = "no_ref"
	default:
		ictx, cancel := context.WithTimeout(ctx, inspectTimeout)
		s, err := k.Inspect(ictx, t.ExternalRef, DetailSummary)
		cancel()
		if err != nil {
			ev.ErrorCode = "inspect_failed"
			break
		}
		ev = snapshotEvent(t.ExternalRef, s)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, err := m.d.Store.Get(ctx, t.ShortID)
	if err != nil {
		return
	}
	if cur.Status == StatusQueued {
		// a task the restart caught between create and ack has no ref to resume
		u := keep(cur)
		u.Status, u.Finished = StatusUnknown, true
		code := ev.ErrorCode
		if code == "" {
			code = "no_ref"
		}
		u.ErrorCode = &code
		_, _ = m.commit(ctx, cur, u, "unknown")
		return
	}
	if ev.Kind == "waiting_input" && cur.Status == StatusWaitingInput {
		if !validQuestion(ev.Question) || (cur.Question != nil && cur.Question.Token == ev.Question.Token) {
			return
		}
		// the kind asks something else now: the old question is withdrawn
		// first, so the new one is asked, and news, in its own right
		m.applyEvent(ctx, cur, Event{Ref: ev.Ref, Kind: "progress"})
		if cur, err = m.d.Store.Get(ctx, t.ShortID); err != nil {
			return
		}
	}
	m.applyEvent(ctx, cur, ev)
}

func snapshotEvent(ref string, s Snapshot) Event {
	ev := Event{Ref: ref, Progress: s.Progress, Question: s.Question, Result: s.Result, ErrorCode: cleanCode(s.ErrorCode, "")}
	switch s.Status {
	case StatusRunning:
		ev.Kind = "progress"
	case StatusWaitingInput, StatusDone, StatusFailed, StatusCancelled, StatusUnknown:
		ev.Kind = s.Status
	default:
		ev.Kind, ev.ErrorCode = "unknown", "bad_snapshot"
	}
	return ev
}

// Active lists what every turn shows (S0 §11.3), household-wide: the open
// tasks, oldest first, then those finished within ReportWindow nobody
// reported yet.
func (m *Manager) Active(ctx context.Context) ([]Task, error) {
	open, err := m.d.Store.Open(ctx)
	if err != nil {
		return nil, err
	}
	finished, err := m.d.Store.Unreported(ctx, uuid.Nil)
	if err != nil {
		return nil, err
	}
	since := m.d.Now().Add(-ReportWindow)
	for _, t := range finished {
		if t.FinishedAt != nil && t.FinishedAt.After(since) {
			open = append(open, t)
		}
	}
	return open, nil
}

// Unreported lists finished tasks nobody reported yet (zero device: all).
func (m *Manager) Unreported(ctx context.Context, device uuid.UUID) ([]Task, error) {
	return m.d.Store.Unreported(ctx, device)
}

// MarkReported records that a finished task was told to its owner.
func (m *Manager) MarkReported(ctx context.Context, shortID string) error {
	return m.d.Store.MarkReported(ctx, shortID)
}
