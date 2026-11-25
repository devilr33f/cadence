package tasks

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/events"
)

func TestStartPassesTheTaskID(t *testing.T) {
	r := newMRig(t)
	tk := r.start("x")
	if len(r.k.Specs) != 1 || r.k.Specs[0].TaskID != tk.ID {
		t.Fatalf("spec %+v, task %s", r.k.Specs, tk.ID)
	}
}

type codedErr string

func (e codedErr) Error() string     { return "upstream said: private words" }
func (e codedErr) ErrorCode() string { return string(e) }

func TestApprove(t *testing.T) {
	r := newMRig(t)
	r.run()
	tk := r.start("x")
	r.next() // ack
	ctx := context.Background()

	if _, err := r.m.Approve(ctx, tk.ShortID, "", DecisionOnce, dash); !errors.Is(err, ErrNoApproval) {
		t.Fatalf("running task: %v", err)
	}
	r.k.Emit(Event{Ref: tk.ExternalRef, Kind: "waiting_input", Question: &Question{Kind: QuestionClarify, Text: "which?", Token: "c1"}})
	r.next()
	if _, err := r.m.Approve(ctx, tk.ShortID, "c1", DecisionOnce, dash); !errors.Is(err, ErrNoApproval) {
		t.Fatalf("clarify question: %v", err)
	}
	if _, err := r.m.Send(ctx, tk.ShortID, "other", "blue"); !errors.Is(err, ErrStale) {
		t.Fatalf("send to another question: %v", err)
	}
	if _, err := r.m.Send(ctx, tk.ShortID, "c1", "blue"); err != nil {
		t.Fatal(err)
	}
	r.next()
	if len(r.k.Tokens) != 1 || r.k.Tokens[0] != "c1" {
		t.Fatalf("tokens %v", r.k.Tokens)
	}

	r.k.Emit(Event{Ref: tk.ExternalRef, Kind: "waiting_input", Question: &Question{Kind: QuestionApproval, Text: "rm it?", Options: []string{"once", "deny"}, Token: "a1"}})
	r.next()
	for _, bad := range []string{"", "yes", "always", "ONCE"} {
		if _, err := r.m.Approve(ctx, tk.ShortID, "a1", bad, dash); !errors.Is(err, ErrInvalid) {
			t.Fatalf("decision %q: %v", bad, err)
		}
	}
	if _, err := r.m.Approve(ctx, tk.ShortID, "c1", DecisionOnce, dash); !errors.Is(err, ErrStale) {
		t.Fatalf("approval of a question no longer asked: %v", err)
	}
	r.k.ApproveErr = codedErr("prompt_changed")
	_, err := r.m.Approve(ctx, tk.ShortID, "a1", DecisionDeny, dash)
	var ke *KindError
	if !errors.As(err, &ke) || ke.Code != "prompt_changed" || strings.Contains(err.Error(), "private") {
		t.Fatalf("approve error: %v", err)
	}
	r.k.ApproveErr = codedErr("Not A Code")
	if _, err := r.m.Approve(ctx, tk.ShortID, "a1", DecisionDeny, dash); !errors.As(err, &ke) || ke.Code != "" {
		t.Fatalf("a code outside the charset passed: %v", err)
	}
	if got, _ := r.m.Get(ctx, tk.ShortID); got.Status != StatusWaitingInput {
		t.Fatalf("a failed approve changed the task: %+v", got)
	}
	r.k.ApproveErr = nil
	got, err := r.m.Approve(ctx, tk.ShortID, "a1", DecisionDeny, dash)
	if err != nil || got.Status != StatusRunning || got.Question != nil {
		t.Fatalf("approved %+v %v", got, err)
	}
	if l := live(t, r.next()); l.Kind != "answered" || l.Status != StatusWaitingInput {
		t.Fatalf("event %+v", l)
	}
	if l := live(t, r.next()); l.Kind != "progress" || l.Status != StatusRunning {
		t.Fatalf("event %+v", l)
	}
	if n := len(r.k.Approved); n != 3 || r.k.Approved[n-1] != tk.ExternalRef+":a1:deny" {
		t.Fatalf("approved %v", r.k.Approved)
	}
}

var dash = Evidence{Via: ViaDashboard}

// the user's words are kept on the task's events, so an approval can be
// traced to what was said; an approval without its evidence is refused
func TestApproveKeepsItsEvidence(t *testing.T) {
	r := newMRig(t)
	r.run()
	dev := r.device()
	tk, err := r.m.Start(context.Background(), "fake", Spec{Instructions: "x", Origin: dev})
	if err != nil {
		t.Fatal(err)
	}
	r.next()
	ctx := context.Background()
	r.k.Emit(Event{Ref: tk.ExternalRef, Kind: "waiting_input", Question: &Question{Kind: QuestionApproval, Text: "rm it?", Token: "a1"}})
	r.next()
	for _, bad := range []Evidence{{}, {Via: "phone"}, {Via: ViaVoice, Device: dev.String()}, {Via: ViaVoice, Device: dev.String(), Words: " \x00 "}} {
		if _, err := r.m.Approve(ctx, tk.ShortID, "a1", DecisionOnce, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("evidence %+v: %v", bad, err)
		}
	}
	if len(r.k.Approved) != 0 {
		t.Fatalf("approved without evidence: %v", r.k.Approved)
	}
	// the kind's resume is stored before Approve takes the lock again: the evidence stays
	r.k.OnApprove = func(ref string) {
		r.k.Emit(Event{Ref: ref, Kind: "progress"})
		eventually(t, "the resume", func() bool {
			got, _ := r.m.Get(ctx, tk.ShortID)
			return got.Status == StatusRunning
		})
	}
	words := "yes, delete the \x00old pods"
	if _, err := r.m.Approve(ctx, tk.ShortID, "a1", DecisionOnce, Evidence{Via: ViaVoice, Device: dev.String(), Words: words}); err != nil {
		t.Fatal(err)
	}
	var kind, answer string
	err = r.pool.QueryRow(ctx, `SELECT kind, answer::text FROM task_events WHERE task_id = $1 AND answer IS NOT NULL`, tk.ID).Scan(&kind, &answer)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"via": "voice", "words": "yes, delete the old pods", "device": "` + dev.String() + `", "decision": "once"}`
	if kind != "answered" || answer != want {
		t.Fatalf("event %s %s", kind, answer)
	}
	if strings.Contains(r.logs.String(), "old pods") {
		t.Fatal("the words reached the log")
	}
}

// a refused start passes the kind's own code on, so the caller can say why
func TestStartErrorCarriesTheKindCode(t *testing.T) {
	r := newMRig(t)
	r.k.StartErr = codedErr("too_busy")
	_, err := r.m.Start(context.Background(), "fake", Spec{Instructions: "x"})
	var ke *KindError
	if !errors.Is(err, ErrStartFailed) || !errors.As(err, &ke) || ke.Code != "too_busy" || strings.Contains(err.Error(), "private") {
		t.Fatalf("err %v", err)
	}
}

func TestKindsAreSorted(t *testing.T) {
	r := newMRig(t)
	r.m.Register(NewFakeKind("alpha"))
	if got := r.m.Kinds(); !slices.Equal(got, []string{"alpha", "fake"}) {
		t.Fatalf("kinds %v", got)
	}
}

// Active is what every turn shows: open tasks, and those finished in the last
// hour nobody reported yet
func TestActive(t *testing.T) {
	r := newMRig(t)
	r.run()
	ctx := context.Background()
	running, waiting, done, reported := r.start("a"), r.start("b"), r.start("c"), r.start("d")
	for range 4 {
		r.next()
	}
	r.k.Emit(Event{Ref: waiting.ExternalRef, Kind: "waiting_input", Question: &Question{Kind: QuestionClarify, Text: "which?", Token: "c1"}})
	r.k.Emit(Event{Ref: done.ExternalRef, Kind: "done", Result: "r"})
	r.k.Emit(Event{Ref: reported.ExternalRef, Kind: "failed"})
	for range 3 {
		r.next()
	}
	if err := r.m.MarkReported(ctx, reported.ShortID); err != nil {
		t.Fatal(err)
	}
	got, err := r.m.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, tk := range got {
		ids = append(ids, tk.ShortID+":"+tk.Status)
	}
	want := []string{running.ShortID + ":running", waiting.ShortID + ":waiting_input", done.ShortID + ":done"}
	if !slices.Equal(ids, want) {
		t.Fatalf("active %v, want %v", ids, want)
	}
	if got[1].Question == nil || got[1].Question.Token != "c1" {
		t.Fatalf("question %+v", got[1].Question)
	}
	// an hour on, the finished one is no longer shown
	later := NewManager(ManagerDeps{Store: r.s, Bus: events.NewBus(0), Now: func() time.Time { return time.Now().Add(61 * time.Minute) }})
	got, _ = later.Active(ctx)
	if len(got) != 2 {
		t.Fatalf("an hour on: %+v", got)
	}
}

type plainKind struct{ *FakeKind }

// Approve is hidden, so the kind is no Approver.
func (plainKind) Approve() {}

func TestApproveNeedsAnApprover(t *testing.T) {
	r := newMRig(t)
	k := plainKind{NewFakeKind("plain")}
	r.m.Register(k)
	r.run()
	tk, err := r.m.Start(context.Background(), "plain", Spec{Instructions: "x"})
	if err != nil {
		t.Fatal(err)
	}
	r.next()
	k.Emit(Event{Ref: tk.ExternalRef, Kind: "waiting_input", Question: &Question{Kind: QuestionApproval, Text: "rm it?"}})
	r.next()
	if _, err := r.m.Approve(context.Background(), tk.ShortID, "", DecisionOnce, dash); !errors.Is(err, ErrNoApproval) {
		t.Fatalf("no approver: %v", err)
	}
}

func TestStartCancelledMeanwhileReleasesTheLock(t *testing.T) {
	r := newMRig(t)
	r.run()
	ctx := context.Background()
	r.k.OnStart = func(string) {
		open, _ := r.s.Open(ctx)
		for _, o := range open {
			if _, err := r.m.Cancel(ctx, o.ShortID); err != nil {
				t.Error(err)
			}
		}
	}
	// a kind may report while it stops: the manager must be free to take it
	r.k.OnCancel = func(ref string) {
		r.k.Emit(Event{Ref: ref, Kind: "progress"})
		deadline := time.Now().Add(3 * time.Second)
		for !strings.Contains(r.logs.String(), "task_event_unmatched") {
			if time.Now().After(deadline) {
				t.Error("the event was never handled: start held the lock over the kind's cancel")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if tk, err := r.m.Start(ctx, "fake", Spec{Instructions: "x"}); !errors.Is(err, ErrNotActive) || tk.Status != StatusCancelled {
			t.Errorf("%+v %v", tk, err)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("start hung")
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("%s never happened", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// an answer resumes only the question it answers: one the kind asked while
// the answer went in stays asked
func TestAnswerResumesOnlyItsQuestion(t *testing.T) {
	r := newMRig(t)
	r.run()
	ctx := context.Background()
	tk := r.start("x")
	r.next()
	r.k.Emit(Event{Ref: tk.ExternalRef, Kind: "waiting_input", Question: &Question{Kind: QuestionApproval, Text: "rm it?", Token: "a1"}})
	r.next()
	r.k.OnApprove = func(ref string) {
		r.k.Emit(Event{Ref: ref, Kind: "progress"})
		r.k.Emit(Event{Ref: ref, Kind: "waiting_input", Question: &Question{Kind: QuestionApproval, Text: "rm the rest?", Token: "a2"}})
		eventually(t, "the next question", func() bool {
			got, _ := r.m.Get(ctx, tk.ShortID)
			return got.Question != nil && got.Question.Token == "a2"
		})
	}
	got, err := r.m.Approve(ctx, tk.ShortID, "a1", DecisionOnce, dash)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusWaitingInput || got.Question == nil || got.Question.Token != "a2" {
		t.Fatalf("the next question was resumed: %+v", got)
	}
	if cur, _ := r.m.Get(ctx, tk.ShortID); cur.Status != StatusWaitingInput || cur.Question.Token != "a2" {
		t.Fatalf("stored %+v", cur)
	}
}
