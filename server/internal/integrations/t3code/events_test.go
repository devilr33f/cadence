package t3code_test

import (
	"encoding/json"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code"
)

func ptr(s string) *string { return &s }

func question(reqID string, labels ...string) t3code.TurnItem {
	opts := make([]t3code.QuestionOption, 0, len(labels))
	for _, l := range labels {
		opts = append(opts, t3code.QuestionOption{Label: l, Description: l})
	}
	return t3code.TurnItem{ID: "item-" + reqID, Type: t3code.ItemUserInputRequest, RequestID: reqID, RunID: ptr("run-2"),
		Questions: []t3code.Question{{ID: "q", Header: "H", Question: "Which?", Options: opts}}}
}

func event(t *testing.T, seq int64, typ string, payload any) t3code.ThreadItem {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return t3code.ThreadItem{Kind: t3code.ThreadEventKind, Sequence: seq, Event: &t3code.Event{Type: typ, Payload: b}}
}

// A snapshot is a baseline: an old finished run and an old error are not
// told again, an open question is, once.
func TestTrackerSnapshotBaseline(t *testing.T) {
	proj := t3code.ThreadProjection{
		Thread:          t3code.AppThread{ID: "th", Title: "Docs"},
		Runs:            []t3code.Run{{ID: "run-1", Status: "completed"}, {ID: "run-2", Status: "waiting"}},
		RuntimeRequests: []t3code.RuntimeRequest{{ID: "req-1", Kind: "user_input", Status: "pending"}},
		TurnItems:       []t3code.TurnItem{question("req-1", "A", "B"), {ID: "err-old", Type: t3code.ItemError, Failure: &t3code.Failure{Message: "old"}}},
	}
	tr := t3code.NewTracker("th")
	snap := t3code.ThreadItem{Kind: t3code.ThreadSnapshotKind, SnapshotSequence: 10, Projection: &proj}
	got := tr.Apply(snap)
	if len(got) != 1 || got[0].Kind != t3code.KindQuestion || got[0].Title != "Docs" || got[0].RunID != "run-2" {
		t.Fatalf("snapshot told %+v", got)
	}
	if again := tr.Apply(snap); len(again) != 0 {
		t.Fatalf("second snapshot told %+v", again)
	}
	if u := tr.Apply(event(t, 11, t3code.EventTurnItemUpdated, map[string]any{"id": "err-old", "type": "error", "failure": map[string]any{"class": "c", "message": "old", "code": nil}})); len(u) != 0 {
		t.Fatalf("old error told %+v", u)
	}
	if u := tr.Apply(event(t, 12, t3code.EventRunUpdated, t3code.Run{ID: "run-1", Status: "completed"})); len(u) != 0 {
		t.Fatalf("old run told %+v", u)
	}
	// the snapshot left the request out: it is no longer open
	proj.RuntimeRequests = nil
	got = tr.Apply(snap)
	if len(got) != 1 || got[0].Kind != t3code.KindResolved || got[0].RequestID != "req-1" {
		t.Fatalf("withdrawn %+v", got)
	}
}

// The token's revision follows the prompt: a changed option list makes a
// new token, and the old one is no longer pending.
func TestTokenRevision(t *testing.T) {
	tr := t3code.NewTracker("th")
	tr.Apply(event(t, 1, t3code.EventRuntimeRequestUpdated, t3code.RuntimeRequest{ID: "req", Kind: "user_input", Status: "pending"}))
	first := tr.Apply(event(t, 2, t3code.EventTurnItemUpdated, question("req", "A", "B")))
	if len(first) != 1 {
		t.Fatalf("question %+v", first)
	}
	if t3code.Token("req", question("req", "A", "B")) != first[0].Token {
		t.Fatal("token not stable")
	}
	second := tr.Apply(event(t, 3, t3code.EventTurnItemUpdated, question("req", "A", "B", "C")))
	if len(second) != 1 || second[0].Token == first[0].Token {
		t.Fatalf("changed prompt %+v", second)
	}
	if _, ok := tr.Pending(first[0].Token); ok {
		t.Error("stale token pending")
	}
	if u, ok := tr.Pending(second[0].Token); !ok || len(u.Questions[0].Options) != 3 {
		t.Error("current token not pending")
	}
	for _, bad := range []string{"", "req", ".x", "req."} {
		if _, ok := tr.Pending(bad); ok {
			t.Errorf("%q pending", bad)
		}
	}
	got := tr.Apply(event(t, 4, t3code.EventRuntimeRequestUpdated, t3code.RuntimeRequest{ID: "req", Kind: "user_input", Status: "expired"}))
	if len(got) != 1 || got[0].Kind != t3code.KindResolved || got[0].Token != second[0].Token {
		t.Fatalf("expired %+v", got)
	}
	if _, ok := tr.Pending(second[0].Token); ok {
		t.Error("expired token pending")
	}
}

func TestTrackerRuns(t *testing.T) {
	tr := t3code.NewTracker("th")
	tr.Apply(event(t, 1, t3code.EventRunCreated, t3code.Run{ID: "r", Status: "running"}))
	tr.Apply(event(t, 2, t3code.EventMessageUpdated, map[string]any{"id": "m1", "runId": "r", "role": "assistant", "text": "partial", "streaming": true}))
	tr.Apply(event(t, 3, t3code.EventMessageUpdated, map[string]any{"id": "m1", "runId": "r", "role": "assistant", "text": "All done.", "streaming": false}))
	tr.Apply(event(t, 4, "thread.metadata-updated", map[string]any{"id": "th", "title": "Renamed"}))
	got := tr.Apply(event(t, 5, t3code.EventRunUpdated, t3code.Run{ID: "r", Status: "rolled_back"}))
	if len(got) != 1 || got[0].Status != "rolled_back" || got[0].Text != "All done." || got[0].Title != "Renamed" {
		t.Fatalf("stopped %+v", got)
	}
	if again := tr.Apply(event(t, 6, t3code.EventRunUpdated, t3code.Run{ID: "r", Status: "rolled_back"})); len(again) != 0 {
		t.Fatalf("told twice %+v", again)
	}
	tr.Apply(event(t, 7, t3code.EventRunCreated, t3code.Run{ID: "r2", Status: "running"}))
	got = tr.Apply(event(t, 8, t3code.EventRunUpdated, t3code.Run{ID: "r2", Status: "failed"}))
	if len(got) != 2 || got[0].Kind != t3code.KindStopped || got[1].Kind != t3code.KindError {
		t.Fatalf("failed %+v", got)
	}
	// payloads that do not decode, and kinds no one knows, tell nothing
	for _, it := range []t3code.ThreadItem{
		{Kind: t3code.ThreadEventKind, Event: &t3code.Event{Type: t3code.EventRunUpdated, Payload: json.RawMessage(`"x"`)}},
		{Kind: "unknown-kind"}, {Kind: t3code.ThreadSynchronized},
		event(t, 9, "future.type", map[string]any{"status": "failed"}),
	} {
		if u := tr.Apply(it); len(u) != 0 {
			t.Errorf("%+v told %+v", it, u)
		}
	}
}

// An approval is told only for an approval kind with its approval item; a
// request kind the user does not answer (auth_refresh) is not.
func TestTrackerApprovalKinds(t *testing.T) {
	tr := t3code.NewTracker("th")
	tr.Apply(event(t, 1, t3code.EventTurnItemUpdated, map[string]any{"id": "i", "type": "approval_request", "requestId": "a", "requestKind": "file-change", "prompt": "Edit?", "options": []any{map[string]any{"decision": "accept", "label": "OK"}}}))
	got := tr.Apply(event(t, 2, t3code.EventRuntimeRequestUpdated, t3code.RuntimeRequest{ID: "a", Kind: "file-change", Status: "pending"}))
	if len(got) != 1 || got[0].Kind != t3code.KindApproval || got[0].Approval.Prompt != "Edit?" {
		t.Fatalf("approval %+v", got)
	}
	tr.Apply(event(t, 3, t3code.EventTurnItemUpdated, map[string]any{"id": "j", "type": "approval_request", "requestId": "b", "requestKind": "command"}))
	if got := tr.Apply(event(t, 4, t3code.EventRuntimeRequestUpdated, t3code.RuntimeRequest{ID: "b", Kind: "auth_refresh", Status: "pending"})); len(got) != 0 {
		t.Fatalf("auth refresh told %+v", got)
	}
}

func TestShellTracker(t *testing.T) {
	s := t3code.NewShellTracker()
	th := t3code.ThreadShell{ID: "th", ProjectID: "p", Title: "T", Status: "completed", LatestRunID: ptr("r1")}
	if u := s.Apply(t3code.ShellItem{Kind: t3code.ShellSnapshotKind, Snapshot: &t3code.ShellSnapshot{Threads: []t3code.ThreadShell{th}, Projects: []t3code.Project{{ID: "p"}}}}); len(u) != 0 {
		t.Fatalf("baseline told %+v", u)
	}
	run := th
	run.Status, run.LatestRunID = "running", ptr("r2")
	if u := s.Apply(t3code.ShellItem{Kind: t3code.ShellThreadUpdated, Sequence: 2, Thread: &run}); len(u) != 0 {
		t.Fatalf("running told %+v", u)
	}
	wait := run
	wait.Status, wait.PendingRuntimeRequest = "waiting", &t3code.PendingRequest{ID: "req", Kind: "command"}
	u := s.Apply(t3code.ShellItem{Kind: t3code.ShellThreadUpdated, Sequence: 3, Thread: &wait})
	if len(u) != 1 || u[0].Kind != t3code.KindApproval || !u[0].NeedsDetail || u[0].RequestID != "req" {
		t.Fatalf("pending %+v", u)
	}
	failed := run
	failed.Status, failed.LastErrorCode = "failed", ptr("overloaded")
	u = s.Apply(t3code.ShellItem{Kind: t3code.ShellThreadUpdated, Sequence: 4, Thread: &failed})
	if len(u) != 2 || u[0].Kind != t3code.KindStopped || u[0].RunID != "r2" || u[1].Kind != t3code.KindError || u[1].ErrorCode != "overloaded" {
		t.Fatalf("failed %+v", u)
	}
	fresh := t3code.ThreadShell{ID: "new", ProjectID: "p", Status: "waiting", PendingRuntimeRequest: &t3code.PendingRequest{ID: "q", Kind: "user_input"}}
	u = s.Apply(t3code.ShellItem{Kind: t3code.ShellThreadUpdated, Sequence: 5, Thread: &fresh})
	if len(u) != 1 || u[0].Kind != t3code.KindQuestion || len(s.Threads) != 2 {
		t.Fatalf("new thread %+v", u)
	}
	s.Apply(t3code.ShellItem{Kind: t3code.ShellThreadRemoved, Sequence: 6, ThreadID: "new"})
	s.Apply(t3code.ShellItem{Kind: t3code.ShellProjectRemoved, Sequence: 7, ProjectID: "p"})
	if len(s.Threads) != 1 || len(s.Projects) != 0 {
		t.Fatalf("removals %+v %+v", s.Threads, s.Projects)
	}
}
