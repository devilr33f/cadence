package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"git.shino.boo/devilreef/cadence/server/internal/events"
)

func TestNextState(t *testing.T) {
	q := &Question{Kind: QuestionClarify, Text: "which?"}
	cases := []struct {
		name   string
		from   string
		ev     Event
		status string // "" = rejected
	}{
		{"running progress", StatusRunning, Event{Kind: "progress", Progress: "x"}, StatusRunning},
		{"running waits", StatusRunning, Event{Kind: "waiting_input", Question: q}, StatusWaitingInput},
		{"running done", StatusRunning, Event{Kind: "done", Result: "r"}, StatusDone},
		{"running failed", StatusRunning, Event{Kind: "failed", ErrorCode: "boom"}, StatusFailed},
		{"running cancelled", StatusRunning, Event{Kind: "cancelled"}, StatusCancelled},
		{"running unknown", StatusRunning, Event{Kind: "unknown"}, StatusUnknown},
		{"waiting progress resumes", StatusWaitingInput, Event{Kind: "progress"}, StatusRunning},
		{"waiting done", StatusWaitingInput, Event{Kind: "done"}, StatusDone},
		{"waiting unknown", StatusWaitingInput, Event{Kind: "unknown"}, StatusUnknown},
		{"waiting only from running", StatusWaitingInput, Event{Kind: "waiting_input", Question: q}, ""},
		{"queued waits", StatusQueued, Event{Kind: "waiting_input", Question: q}, ""},
		{"queued progress", StatusQueued, Event{Kind: "progress"}, ""},
		{"waiting needs a question", StatusRunning, Event{Kind: "waiting_input"}, ""},
		{"waiting needs a valid question", StatusRunning, Event{Kind: "waiting_input", Question: &Question{Kind: "x", Text: "t"}}, ""},
		{"unknown resolves done", StatusUnknown, Event{Kind: "done"}, StatusDone},
		{"unknown resolves failed", StatusUnknown, Event{Kind: "failed"}, StatusFailed},
		{"unknown stays unknown", StatusUnknown, Event{Kind: "unknown"}, ""},
		{"unknown no progress", StatusUnknown, Event{Kind: "progress"}, ""},
		{"done final", StatusDone, Event{Kind: "progress"}, ""},
		{"done not failed", StatusDone, Event{Kind: "failed"}, ""},
		{"failed final", StatusFailed, Event{Kind: "done"}, ""},
		{"cancelled final", StatusCancelled, Event{Kind: "done"}, ""},
		{"unknown event kind", StatusRunning, Event{Kind: "exploded"}, ""},
		{"nul only question", StatusRunning, Event{Kind: "waiting_input", Question: &Question{Kind: QuestionClarify, Text: "\x00"}}, ""},
		{"token kept", StatusRunning, Event{Kind: "waiting_input", Question: &Question{Kind: QuestionClarify, Text: "q", Token: "ab-12_C"}}, StatusWaitingInput},
		{"token too long", StatusRunning, Event{Kind: "waiting_input", Question: &Question{Kind: QuestionClarify, Text: "q", Token: strings.Repeat("a", 65)}}, ""},
		{"token not plain", StatusRunning, Event{Kind: "waiting_input", Question: &Question{Kind: QuestionClarify, Text: "q", Token: "a b"}}, ""},
	}
	for _, c := range cases {
		u, kind, err := nextState(Task{Status: c.from, Progress: "old"}, c.ev)
		if c.status == "" {
			if !errors.Is(err, ErrTransition) {
				t.Errorf("%s: err %v", c.name, err)
			}
			continue
		}
		if err != nil || u.Status != c.status || kind != c.ev.Kind || u.Finished != (c.status != StatusRunning && c.status != StatusWaitingInput) {
			t.Errorf("%s: %+v %q %v", c.name, u, kind, err)
		}
	}
	if u, _, _ := nextState(Task{Status: StatusRunning}, Event{Kind: "waiting_input", Question: &Question{Kind: QuestionClarify, Text: "q", Token: "t1"}}); u.Question == nil || u.Question.Token != "t1" {
		t.Errorf("token lost: %+v", u.Question)
	}
	for _, bad := range []string{"has space", "UPPER", strings.Repeat("a", 65), "ünï", "a\nb", ""} {
		u, _, _ := nextState(Task{Status: StatusRunning}, Event{Kind: "failed", ErrorCode: bad})
		if *u.ErrorCode != "failed" {
			t.Errorf("failed code %q kept as %q", bad, *u.ErrorCode)
		}
		u, _, _ = nextState(Task{Status: StatusRunning}, Event{Kind: "unknown", ErrorCode: bad})
		if *u.ErrorCode != "unknown" {
			t.Errorf("unknown code %q kept as %q", bad, *u.ErrorCode)
		}
	}
	if u, _, _ := nextState(Task{Status: StatusRunning}, Event{Kind: "failed", ErrorCode: "rate_limit_429"}); *u.ErrorCode != "rate_limit_429" {
		t.Errorf("good code lost")
	}
	// the code of an unknown task goes when a kind resolves it to done or cancelled
	for _, kind := range []string{"done", "cancelled"} {
		u, _, err := nextState(Task{Status: StatusUnknown, ErrorCode: "cannot_resume"}, Event{Kind: kind, ErrorCode: "x"})
		if err != nil || u.ErrorCode != nil {
			t.Errorf("unknown -> %s keeps a code: %+v %v", kind, u.ErrorCode, err)
		}
	}
	u, _, _ := nextState(Task{Status: StatusUnknown, ErrorCode: "cannot_resume"}, Event{Kind: "failed", ErrorCode: "boom"})
	if *u.ErrorCode != "boom" {
		t.Errorf("unknown -> failed code %q", *u.ErrorCode)
	}
	// nul bytes cannot reach postgres
	u, _, _ = nextState(Task{Status: StatusRunning}, Event{Kind: "done", Progress: "a\x00b", Result: "r\x00s"})
	if u.Progress != "ab" || *u.Result != "rs" {
		t.Errorf("nul kept: %q %q", u.Progress, *u.Result)
	}
	u, _, _ = nextState(Task{Status: StatusRunning}, Event{Kind: "waiting_input", Question: &Question{Kind: QuestionClarify, Text: "q\x00?", Options: []string{"a\x00"}}})
	if u.Question.Text != "q?" || u.Question.Options[0] != "a" {
		t.Errorf("nul in question: %+v", u.Question)
	}
	// state carried between events
	u, _, _ = nextState(Task{Status: StatusRunning, Progress: "old"}, Event{Kind: "progress"})
	if u.Progress != "old" {
		t.Errorf("empty progress replaced: %q", u.Progress)
	}
	u, _, _ = nextState(Task{Status: StatusWaitingInput, Progress: "old", Question: q}, Event{Kind: "progress", Progress: "new"})
	if u.Progress != "new" || u.Question != nil {
		t.Errorf("resume: %+v", u)
	}
	u, _, _ = nextState(Task{Status: StatusRunning}, Event{Kind: "failed"})
	if u.ErrorCode == nil || *u.ErrorCode != "failed" {
		t.Errorf("default code: %+v", u.ErrorCode)
	}
	u, _, _ = nextState(Task{Status: StatusRunning}, Event{Kind: "done", Result: strings.Repeat("é", MaxResult), Progress: strings.Repeat("é", MaxProgress+5)})
	if len(*u.Result) > MaxResult || len([]rune(u.Progress)) != MaxProgress {
		t.Errorf("caps: %d %d", len(*u.Result), len([]rune(u.Progress)))
	}
}

type mrig struct {
	*rig
	m    *Manager
	k    *FakeKind
	sub  *events.Subscription
	logs *syncBuf
}

func newMRig(t *testing.T) *mrig {
	r := newRig(t)
	bus := events.NewBus(0)
	buf := &syncBuf{}
	m := NewManager(ManagerDeps{Store: r.s, Bus: bus, Log: slog.New(slog.NewTextHandler(buf, nil))})
	k := NewFakeKind("fake")
	m.Register(k)
	sub := bus.Watch(func(e events.Event) bool { return e.Type == events.TaskEvent })
	t.Cleanup(sub.Close)
	return &mrig{rig: r, m: m, k: k, sub: sub, logs: buf}
}

func (r *mrig) run() {
	ctx, cancel := context.WithCancel(context.Background())
	stop := r.m.Run(ctx)
	r.t.Cleanup(func() { cancel(); stop() })
}

func (r *mrig) next() events.Event {
	r.t.Helper()
	select {
	case e := <-r.sub.C:
		return e
	case <-time.After(10 * time.Second):
		r.t.Fatal("no task event")
		return events.Event{}
	}
}

func (r *mrig) start(instr string) Task {
	r.t.Helper()
	tk, err := r.m.Start(context.Background(), "fake", Spec{Instructions: instr})
	if err != nil {
		r.t.Fatal(err)
	}
	return tk
}

func live(t *testing.T, e events.Event) Live {
	t.Helper()
	l, ok := e.Data.(Live)
	if !ok {
		t.Fatalf("data %T", e.Data)
	}
	return l
}

func TestStartAcksAndTitlesFromInstructions(t *testing.T) {
	r := newMRig(t)
	dev := r.device()
	instr := "  find the best espresso grinder\nsecret second line with details"
	tk, err := r.m.Start(context.Background(), "fake", Spec{Instructions: instr, Origin: dev})
	if err != nil {
		t.Fatal(err)
	}
	if tk.Status != StatusRunning || tk.ExternalRef != "fake-1" || tk.Title != "find the best espresso grinder" || tk.Origin != dev {
		t.Fatalf("task %+v", tk)
	}
	if len(r.k.Specs) != 1 || r.k.Specs[0].Instructions != instr || r.k.Specs[0].Title != tk.Title {
		t.Fatalf("kind got %+v", r.k.Specs)
	}
	var n int
	if err := r.pool.QueryRow(context.Background(), `SELECT count(*) FROM tasks WHERE title LIKE '%secret%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("instruction text stored: %d %v", n, err)
	}
	e := r.next()
	l := live(t, e)
	if e.Type != events.TaskEvent || e.DeviceID != dev.String() || l != (Live{ShortID: tk.ShortID, TaskKind: "fake", Kind: "progress", Status: StatusRunning, Seq: 1}) {
		t.Fatalf("ack event %+v %+v", e, l)
	}
	long, _ := r.m.Start(context.Background(), "fake", Spec{Title: strings.Repeat("é", 300), Instructions: "x"})
	if len([]rune(long.Title)) != MaxTitle {
		t.Fatalf("title %d", len([]rune(long.Title)))
	}
	empty, _ := r.m.Start(context.Background(), "fake", Spec{Instructions: " \n "})
	if empty.Title != "fake" {
		t.Fatalf("fallback title %q", empty.Title)
	}
	if _, err := r.m.Start(context.Background(), "nope", Spec{Instructions: "x"}); !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("unknown kind: %v", err)
	}
}

func TestStartErrorFailsTheTask(t *testing.T) {
	r := newMRig(t)
	r.k.StartErr = errors.New("upstream said: private words")
	tk, err := r.m.Start(context.Background(), "fake", Spec{Instructions: "x"})
	if !errors.Is(err, ErrStartFailed) || strings.Contains(err.Error(), "private") {
		t.Fatalf("err %v", err)
	}
	if tk.Status != StatusFailed || tk.ErrorCode != "start_failed" || tk.FinishedAt == nil || tk.ExternalRef != "" {
		t.Fatalf("task %+v", tk)
	}
	if l := live(t, r.next()); l.Kind != "failed" || l.Status != StatusFailed {
		t.Fatalf("event %+v", l)
	}
}

func TestFanInLifecycle(t *testing.T) {
	r := newMRig(t)
	r.run()
	tk := r.start("do it")
	r.next() // ack
	ref := tk.ExternalRef

	r.k.Emit(Event{Ref: ref, Kind: "progress", Progress: "step 1 of private stuff"})
	e := r.next()
	if l := live(t, e); l.Kind != "progress" || l.Status != StatusRunning || l.Seq != 2 || l.ShortID != tk.ShortID {
		t.Fatalf("progress event %+v", l)
	}
	raw, _ := json.Marshal(e.Data)
	if strings.Contains(string(raw), "private") {
		t.Fatalf("text in live data: %s", raw)
	}
	got, _ := r.m.Get(context.Background(), tk.ShortID)
	if got.Progress != "step 1 of private stuff" {
		t.Fatalf("progress %q", got.Progress)
	}

	r.k.Emit(Event{Ref: ref, Kind: "waiting_input", Question: &Question{Kind: QuestionApproval, Text: "delete it?", Options: []string{"yes", "no"}}})
	if l := live(t, r.next()); l.Status != StatusWaitingInput || l.Seq != 3 {
		t.Fatalf("waiting %+v", l)
	}
	got, _ = r.m.Get(context.Background(), tk.ShortID)
	if got.Status != StatusWaitingInput || got.Question == nil || got.Question.Text != "delete it?" || got.Progress != "step 1 of private stuff" {
		t.Fatalf("waiting task %+v", got)
	}

	if _, err := r.m.Send(context.Background(), tk.ShortID, "", "yes"); err != nil {
		t.Fatal(err)
	}
	if len(r.k.Sent) != 1 || r.k.Sent[0] != ref+":yes" {
		t.Fatalf("sent %v", r.k.Sent)
	}
	if l := live(t, r.next()); l.Status != StatusRunning || l.Kind != "progress" || l.Seq != 4 {
		t.Fatalf("resumed %+v", l)
	}
	got, _ = r.m.Get(context.Background(), tk.ShortID)
	if got.Status != StatusRunning || got.Question != nil {
		t.Fatalf("resumed task %+v", got)
	}

	r.k.Emit(Event{Ref: ref, Kind: "done", Result: "the answer"})
	if l := live(t, r.next()); l.Status != StatusDone || l.Kind != "done" || l.Seq != 5 {
		t.Fatalf("done %+v", l)
	}
	got, _ = r.m.Get(context.Background(), tk.ShortID)
	if got.Status != StatusDone || got.Result != "the answer" || got.FinishedAt == nil {
		t.Fatalf("done task %+v", got)
	}

	// terminal is final: a later event changes nothing and publishes nothing
	r.k.Emit(Event{Ref: ref, Kind: "failed", ErrorCode: "late"})
	waitLog(t, r.logs, "task_event_rejected")
	select {
	case e := <-r.sub.C:
		t.Fatalf("late event published %+v", e)
	default:
	}
	got, _ = r.m.Get(context.Background(), tk.ShortID)
	if got.Status != StatusDone || got.ErrorCode != "" {
		t.Fatalf("terminal changed %+v", got)
	}
	if !strings.Contains(r.logs.String(), "task_event_rejected") || strings.Contains(r.logs.String(), "private") ||
		strings.Contains(r.logs.String(), "the answer") || strings.Contains(r.logs.String(), "delete it") {
		t.Fatalf("logs %q", r.logs.String())
	}
}

func waitLog(t *testing.T, buf fmt.Stringer, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("no %q in logs", want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFanInFailedWithCode(t *testing.T) {
	r := newMRig(t)
	r.run()
	tk := r.start("x")
	r.next()
	r.k.Emit(Event{Ref: tk.ExternalRef, Kind: "failed", ErrorCode: "rate_limited"})
	if l := live(t, r.next()); l.Status != StatusFailed {
		t.Fatalf("%+v", l)
	}
	got, _ := r.m.Get(context.Background(), tk.ShortID)
	if got.ErrorCode != "rate_limited" || got.FinishedAt == nil {
		t.Fatalf("%+v", got)
	}
}

func TestFanInDropsStrangersAndBadEvents(t *testing.T) {
	r := newMRig(t)
	r.run()
	tk := r.start("x")
	r.next()
	r.k.Emit(Event{Ref: "nobody", Kind: "done"})
	r.k.Emit(Event{Ref: tk.ExternalRef, Kind: "waiting_input", Question: &Question{Kind: "bad", Text: "t"}})
	r.k.Emit(Event{Ref: tk.ExternalRef, Kind: "progress", Progress: "ok"})
	if l := live(t, r.next()); l.Kind != "progress" || l.Seq != 2 {
		t.Fatalf("%+v", l)
	}
	got, _ := r.m.Get(context.Background(), tk.ShortID)
	if got.Status != StatusRunning {
		t.Fatalf("%+v", got)
	}
}

func TestEventBeforeAckIsKept(t *testing.T) {
	r := newMRig(t)
	r.run()
	r.k.OnStart = func(ref string) {
		r.k.Emit(Event{Ref: ref, Kind: "progress", Progress: "early"})
		// let the fan-in see it before Start returns
		time.Sleep(100 * time.Millisecond)
	}
	tk := r.start("x")
	r.next() // ack
	if l := live(t, r.next()); l.Kind != "progress" || l.Seq != 2 {
		t.Fatalf("early event %+v", l)
	}
	got, _ := r.m.Get(context.Background(), tk.ShortID)
	if got.Progress != "early" {
		t.Fatalf("%+v", got)
	}
}

func TestEventTypeFeedAndWatch(t *testing.T) {
	r := newMRig(t)
	w := r.m.Watch()
	defer w.Close()
	tk := r.start("x")
	select {
	case e := <-w.C:
		if l := live(t, e); l.ShortID != tk.ShortID || e.Type != events.TaskEvent {
			t.Fatalf("%+v", l)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event on watch")
	}
}

func TestCancel(t *testing.T) {
	r := newMRig(t)
	r.run()
	tk := r.start("x")
	r.next()

	r.k.CancelErr = errors.New("nope")
	if _, err := r.m.Cancel(context.Background(), tk.ShortID); err == nil {
		t.Fatal("cancel error swallowed")
	}
	got, _ := r.m.Get(context.Background(), tk.ShortID)
	if got.Status != StatusRunning {
		t.Fatalf("status after failed cancel %s", got.Status)
	}
	r.k.CancelErr = nil
	c, err := r.m.Cancel(context.Background(), tk.ShortID)
	if err != nil || c.Status != StatusCancelled || c.FinishedAt == nil {
		t.Fatalf("cancel %+v %v", c, err)
	}
	if l := live(t, r.next()); l.Kind != "cancelled" || l.Status != StatusCancelled {
		t.Fatalf("%+v", l)
	}
	if _, err := r.m.Cancel(context.Background(), tk.ShortID); !errors.Is(err, ErrNotActive) {
		t.Fatalf("second cancel %v", err)
	}
	r.k.Emit(Event{Ref: tk.ExternalRef, Kind: "done"})
	if _, err := r.m.Send(context.Background(), tk.ShortID, "", "hi"); !errors.Is(err, ErrNotActive) {
		t.Fatalf("send to cancelled: %v", err)
	}
	if _, err := r.m.Cancel(context.Background(), "t-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing %v", err)
	}
}

func TestCancelQueuedWithoutRef(t *testing.T) {
	r := newMRig(t)
	q, _ := r.s.Create(context.Background(), "fake", "queued", uuid.Nil)
	c, err := r.m.Cancel(context.Background(), q.ShortID)
	if err != nil || c.Status != StatusCancelled || len(r.k.Cancelled) != 0 {
		t.Fatalf("%+v %v %v", c, err, r.k.Cancelled)
	}
}

func TestSendNeedsAnActiveTask(t *testing.T) {
	r := newMRig(t)
	tk := r.start("x")
	r.k.SendErr = errors.New("nope")
	if _, err := r.m.Send(context.Background(), tk.ShortID, "", "m"); err == nil {
		t.Fatal("send error swallowed")
	}
	r.k.SendErr = nil
	u, _ := r.s.Create(context.Background(), "fake", "q", uuid.Nil)
	if _, err := r.m.Send(context.Background(), u.ShortID, "", "m"); !errors.Is(err, ErrNotActive) {
		t.Fatalf("queued: %v", err)
	}
}

func TestInspect(t *testing.T) {
	r := newMRig(t)
	tk := r.start("x")
	r.k.Snapshots[tk.ExternalRef] = Snapshot{Status: StatusRunning, Progress: "p", Transcript: "t"}
	s, err := r.m.Inspect(context.Background(), tk.ShortID, DetailTranscript)
	if err != nil || s.Transcript != "t" {
		t.Fatalf("%+v %v", s, err)
	}
	done, _, _ := r.s.Apply(context.Background(), tk.ID, Update{Status: StatusDone, Result: sp("res"), Finished: true}, "done")
	s, err = r.m.Inspect(context.Background(), done.ShortID, DetailSummary)
	if err != nil || s.Status != StatusDone || s.Result != "res" {
		t.Fatalf("finished summary %+v %v", s, err)
	}
	if _, err := r.m.Inspect(context.Background(), "t-1", DetailSummary); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestReattachAfterRestart(t *testing.T) {
	r := newMRig(t)
	ctx := context.Background()
	mk := func(ref string) Task {
		tk, err := r.s.Create(ctx, "fake", "t "+ref, uuid.Nil)
		if err != nil {
			t.Fatal(err)
		}
		if ref != "" {
			tk, _, err = r.s.Apply(ctx, tk.ID, Update{Status: StatusRunning, ExternalRef: &ref}, "progress")
			if err != nil {
				t.Fatal(err)
			}
		}
		return tk
	}
	still, finished, lost, failing, waiting := mk("a"), mk("b"), mk("c"), mk("d"), mk("e")
	queued := mk("")
	orphanKind, _ := r.s.Create(ctx, "gone", "g", uuid.Nil)
	ref := "g"
	orphanKind, _, _ = r.s.Apply(ctx, orphanKind.ID, Update{Status: StatusRunning, ExternalRef: &ref}, "progress")
	waitingSame := mk("g")
	waitingSame, _, _ = r.s.Apply(ctx, waitingSame.ID, Update{Status: StatusWaitingInput, Question: &Question{Kind: QuestionClarify, Text: "same?"}}, "waiting_input")
	doneAlready := mk("f")
	_, _, _ = r.s.Apply(ctx, doneAlready.ID, Update{Status: StatusDone, Finished: true}, "done")

	r.k.Snapshots["a"] = Snapshot{Status: StatusRunning, Progress: "going"}
	r.k.Snapshots["b"] = Snapshot{Status: StatusDone, Result: "result"}
	r.k.Snapshots["c"] = Snapshot{Status: StatusUnknown, ErrorCode: "cannot_resume"}
	r.k.Snapshots["d"] = Snapshot{Status: StatusFailed, ErrorCode: "crashed"}
	r.k.Snapshots["e"] = Snapshot{Status: StatusWaitingInput, Question: &Question{Kind: QuestionClarify, Text: "which?"}}
	r.k.Snapshots["g"] = Snapshot{Status: StatusWaitingInput, Question: &Question{Kind: QuestionClarify, Text: "other?"}}

	r.m.Reattach(ctx)

	check := func(tk Task, status, field string) {
		t.Helper()
		got, _ := r.m.Get(ctx, tk.ShortID)
		if got.Status != status {
			t.Errorf("%s: status %s, want %s", tk.Title, got.Status, status)
		}
		if field != "" && got.Progress != field && got.Result != field && got.ErrorCode != field {
			t.Errorf("%s: %+v lacks %q", tk.Title, got, field)
		}
	}
	check(still, StatusRunning, "going")
	check(finished, StatusDone, "result")
	check(lost, StatusUnknown, "cannot_resume")
	check(failing, StatusFailed, "crashed")
	check(waiting, StatusWaitingInput, "")
	if w, _ := r.m.Get(ctx, waiting.ShortID); w.Question == nil || w.Question.Text != "which?" {
		t.Errorf("waiting snapshot lost its question: %+v", w.Question)
	}
	// already waiting: skipped, no new event row, question untouched
	check(waitingSame, StatusWaitingInput, "")
	if w, _ := r.m.Get(ctx, waitingSame.ShortID); w.Question.Text != "same?" {
		t.Errorf("waiting skip rewrote the question: %+v", w.Question)
	}
	var rows int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM task_events WHERE task_id = $1`, waitingSame.ID).Scan(&rows); err != nil || rows != 2 {
		t.Errorf("event rows of a skipped task: %d %v", rows, err)
	}
	check(queued, StatusUnknown, "no_ref")
	check(orphanKind, StatusUnknown, "kind_missing")
	check(doneAlready, StatusDone, "")
	if len(r.k.Specs) != 0 || len(r.k.Sent) != 0 {
		t.Fatalf("a task was restarted or resumed by the manager: %v %v", r.k.Specs, r.k.Sent)
	}
	if len(r.k.Inspected) != 6 {
		t.Fatalf("inspected %v", r.k.Inspected)
	}

	// an unconfirmed task is never inspected again as running: a second pass leaves unknown tasks alone
	r.k.Inspected = nil
	r.m.Reattach(ctx)
	if !slices.Equal(r.k.Inspected, []string{"a", "e", "g"}) {
		t.Errorf("second pass inspected %v: only the still-open tasks, never the unknown or finished ones", r.k.Inspected)
	}
	check(lost, StatusUnknown, "cannot_resume")
}

func TestReattachInspectErrorMarksUnknown(t *testing.T) {
	r := newMRig(t)
	ctx := context.Background()
	tk, _ := r.s.Create(ctx, "fake", "t", uuid.Nil)
	ref := "x"
	_, _, _ = r.s.Apply(ctx, tk.ID, Update{Status: StatusRunning, ExternalRef: &ref}, "progress")
	r.k.InspectErr = errors.New("private upstream text")
	r.m.Reattach(ctx)
	got, _ := r.m.Get(ctx, tk.ShortID)
	if got.Status != StatusUnknown || got.ErrorCode != "inspect_failed" || strings.Contains(r.logs.String(), "private") {
		t.Fatalf("%+v %q", got, r.logs.String())
	}
}

func TestRunStopsAndReattachesOnce(t *testing.T) {
	r := newMRig(t)
	ctx := context.Background()
	tk, _ := r.s.Create(ctx, "fake", "t", uuid.Nil)
	ref := "x"
	_, _, _ = r.s.Apply(ctx, tk.ID, Update{Status: StatusRunning, ExternalRef: &ref}, "progress")
	r.k.Snapshots["x"] = Snapshot{Status: StatusDone, Result: "r"}
	rc, cancel := context.WithCancel(ctx)
	stop := r.m.Run(rc)
	if l := live(t, r.next()); l.ShortID != tk.ShortID || l.Status != StatusDone {
		t.Fatalf("%+v", l)
	}
	cancel()
	stop()
	if len(r.k.Inspected) != 1 {
		t.Fatalf("inspected %v", r.k.Inspected)
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestStartCancelledMeanwhileStays(t *testing.T) {
	r := newMRig(t)
	ctx := context.Background()
	r.k.OnStart = func(string) {
		open, _ := r.s.Open(ctx)
		if len(open) != 1 {
			t.Errorf("open %d", len(open))
			return
		}
		if _, err := r.m.Cancel(ctx, open[0].ShortID); err != nil {
			t.Error(err)
		}
	}
	tk, err := r.m.Start(ctx, "fake", Spec{Instructions: "x"})
	if !errors.Is(err, ErrNotActive) || tk.Status != StatusCancelled {
		t.Fatalf("%+v %v", tk, err)
	}
	got, _ := r.m.Get(ctx, tk.ShortID)
	if got.Status != StatusCancelled || got.ExternalRef != "" {
		t.Fatalf("ack overwrote the cancel: %+v", got)
	}
	if len(r.k.Cancelled) != 1 || r.k.Cancelled[0] != "fake-1" {
		t.Fatalf("kind not told to stop: %v", r.k.Cancelled)
	}
}

func TestCancelUnknownTaskTellsTheKindBestEffort(t *testing.T) {
	r := newMRig(t)
	ctx := context.Background()
	tk := r.start("x")
	if _, _, err := r.s.Apply(ctx, tk.ID, Update{Status: StatusUnknown, Finished: true}, "unknown"); err != nil {
		t.Fatal(err)
	}
	r.k.CancelErr = errors.New("gone")
	c, err := r.m.Cancel(ctx, tk.ShortID)
	if err != nil || c.Status != StatusCancelled || c.ErrorCode != "" || len(r.k.Cancelled) != 1 {
		t.Fatalf("%+v %v %v", c, err, r.k.Cancelled)
	}
}

func TestUnreportedAndMarkReported(t *testing.T) {
	r := newMRig(t)
	ctx := context.Background()
	d1, d2 := r.device(), r.device()
	mk := func(dev uuid.UUID, finish bool) Task {
		tk, err := r.m.Start(ctx, "fake", Spec{Instructions: "x", Origin: dev})
		if err != nil {
			t.Fatal(err)
		}
		if finish {
			tk, err = r.m.Cancel(ctx, tk.ShortID)
			if err != nil {
				t.Fatal(err)
			}
		}
		return tk
	}
	a, b, _ := mk(d1, true), mk(d2, true), mk(d1, false)
	got, err := r.m.Unreported(ctx, uuid.Nil)
	if err != nil || len(got) != 2 || got[0].ID != a.ID {
		t.Fatalf("all: %v %v", got, err)
	}
	if got, _ = r.m.Unreported(ctx, d2); len(got) != 1 || got[0].ID != b.ID {
		t.Fatalf("d2: %v", got)
	}
	if err := r.m.MarkReported(ctx, a.ShortID); err != nil {
		t.Fatal(err)
	}
	if err := r.m.MarkReported(ctx, a.ShortID); err != nil {
		t.Fatalf("marking twice: %v", err)
	}
	if got, _ = r.m.Unreported(ctx, d1); len(got) != 0 {
		t.Fatalf("after mark: %v", got)
	}
	if tk, _ := r.m.Get(ctx, a.ShortID); tk.ReportedAt == nil {
		t.Fatal("reported_at not set")
	}
	if err := r.m.MarkReported(ctx, "t-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestListOrdersShortIDsNumerically(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.pool.Exec(ctx, `SELECT setval('task_short_id_seq', 9998)`); err != nil {
		t.Fatal(err)
	}
	var want []string
	for range 3 {
		tk, err := r.s.Create(ctx, "hermes", "t", uuid.Nil)
		if err != nil {
			t.Fatal(err)
		}
		want = append([]string{tk.ShortID}, want...)
	}
	// same created_at: the tie falls to the number, not the text ("t-9999" > "t-10000")
	if _, err := r.pool.Exec(ctx, `UPDATE tasks SET created_at = '2026-01-01'`); err != nil {
		t.Fatal(err)
	}
	got, _ := r.s.List(ctx, Filter{})
	var ids []string
	for _, tk := range got {
		ids = append(ids, tk.ShortID)
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("list %v, want %v", ids, want)
	}
	open, _ := r.s.Open(ctx)
	slices.Reverse(want)
	ids = nil
	for _, tk := range open {
		ids = append(ids, tk.ShortID)
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("open %v, want %v", ids, want)
	}
}

// a kind that asks something else after a restart asks it anew: the old
// question is withdrawn and the new one is news
func TestReattachTakesAChangedQuestion(t *testing.T) {
	r := newMRig(t)
	ctx := context.Background()
	tk, _ := r.s.Create(ctx, "fake", "t", uuid.Nil)
	ref := "x"
	_, _, _ = r.s.Apply(ctx, tk.ID, Update{Status: StatusRunning, ExternalRef: &ref}, "progress")
	_, _, _ = r.s.Apply(ctx, tk.ID, Update{Status: StatusWaitingInput, Question: &Question{Kind: QuestionApproval, Text: "rm a?", Token: "a1"}}, "waiting_input")
	r.k.Snapshots["x"] = Snapshot{Status: StatusWaitingInput, Question: &Question{Kind: QuestionApproval, Text: "rm b?", Token: "b1"}}
	r.m.Reattach(ctx)
	got, _ := r.m.Get(ctx, tk.ShortID)
	if got.Status != StatusWaitingInput || got.Question == nil || got.Question.Token != "b1" || got.Question.Text != "rm b?" {
		t.Fatalf("task %+v %+v", got, got.Question)
	}
	evs, err := r.s.Events(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	if !slices.Equal(kinds, []string{"progress", "waiting_input", "progress", "waiting_input"}) {
		t.Fatalf("events %v", kinds)
	}
}
