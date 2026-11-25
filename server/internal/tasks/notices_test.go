package tasks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func (r *rig) apply(id uuid.UUID, u Update, kind string) int {
	r.t.Helper()
	_, seq, err := r.s.Apply(context.Background(), id, u, kind)
	if err != nil {
		r.t.Fatal(err)
	}
	return seq
}

// pendingOf names each pending event as short id, device and seq.
func pendingOf(ps []Pending) []string {
	out := []string{}
	for _, p := range ps {
		out = append(out, fmt.Sprintf("%s %s %d", p.Task.ShortID, p.Task.Origin, p.Seq))
	}
	return out
}

func pend(tk Task, dev uuid.UUID, seq int) []string {
	return []string{fmt.Sprintf("%s %s %d", tk.ShortID, dev, seq)}
}

func (r *rig) reported(shortID string) bool {
	r.t.Helper()
	got, err := r.s.Get(context.Background(), shortID)
	if err != nil {
		r.t.Fatal(err)
	}
	return got.ReportedAt != nil
}

// a waiting_input, done or failed event is news again, even for a task told
// before (an unknown one a kind later resolved); marking an older event never
// hides a newer one
func TestReportableEventsReopenTheReport(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	dev := r.device()
	tk, _ := r.s.Create(ctx, "hermes", "t", dev)
	r.apply(tk.ID, Update{Status: StatusRunning, ExternalRef: sp("ref")}, "progress")
	r.apply(tk.ID, Update{Status: StatusUnknown, ErrorCode: sp("lost"), Finished: true}, "unknown")
	if err := r.s.MarkReported(ctx, tk.ShortID); err != nil || !r.reported(tk.ShortID) {
		t.Fatalf("mark %v", err)
	}
	done := r.apply(tk.ID, Update{Status: StatusDone, Result: sp("r"), Finished: true}, "done")
	if r.reported(tk.ShortID) {
		t.Fatal("the resolution of an unknown task stays reported")
	}
	if un, _ := r.s.Unreported(ctx, dev); len(un) != 1 || un[0].ShortID != tk.ShortID {
		t.Fatalf("unreported %+v", un)
	}
	if got, err := r.s.ToReport(ctx, ""); err != nil || !slices.Equal(pendingOf(got), pend(tk, dev, done)) || got[0].Task.Status != StatusDone {
		t.Fatalf("to report %+v %v", got, err)
	}
	if err := r.s.MarkReportedAt(ctx, tk.ShortID, done-1); err != nil || r.reported(tk.ShortID) {
		t.Fatalf("an older event marked the newer one: %v", err)
	}
	if err := r.s.MarkReportedAt(ctx, tk.ShortID, done); err != nil || !r.reported(tk.ShortID) {
		t.Fatalf("mark at %d: %v", done, err)
	}
	if got, _ := r.s.ToReport(ctx, tk.ShortID); len(got) != 0 {
		t.Fatalf("to report after the mark %+v", got)
	}
	if err := r.s.MarkReportedAt(ctx, "t-1", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown task: %v", err)
	}

	// a resume or a cancel is no news
	other, _ := r.s.Create(ctx, "hermes", "o", dev)
	r.apply(other.ID, Update{Status: StatusRunning, ExternalRef: sp("ref-o")}, "progress")
	wait := r.apply(other.ID, Update{Status: StatusWaitingInput, Question: &Question{Kind: QuestionClarify, Text: "which?"}}, "waiting_input")
	if got, _ := r.s.ToReport(ctx, other.ShortID); !slices.Equal(pendingOf(got), pend(other, dev, wait)) {
		t.Fatalf("waiting to report %+v", got)
	}
	_ = r.s.MarkReportedAt(ctx, other.ShortID, wait)
	r.apply(other.ID, Update{Status: StatusRunning}, "progress")
	r.apply(other.ID, Update{Status: StatusCancelled, Finished: true}, "cancelled")
	if !r.reported(other.ShortID) {
		t.Fatal("a resume or cancel reopened the report")
	}
}

// what the reporter still owes a turn: the latest reportable event of a task
// with a device, neither reported nor turned into a notice
func TestToReportSkipsWhatIsHandled(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	dev := r.device()
	running, _ := r.s.Create(ctx, "hermes", "running", dev)
	r.apply(running.ID, Update{Status: StatusRunning, ExternalRef: sp("a")}, "progress")
	orphan, _ := r.s.Create(ctx, "hermes", "orphan", uuid.Nil)
	r.apply(orphan.ID, Update{Status: StatusDone, Finished: true}, "done")
	noticed, _ := r.s.Create(ctx, "hermes", "noticed", dev)
	seq := r.apply(noticed.ID, Update{Status: StatusDone, Finished: true}, "done")
	if err := r.s.AddNotice(ctx, Notice{TaskID: noticed.ID, EventSeq: seq, DeviceID: dev, Text: "it is done"}); err != nil {
		t.Fatal(err)
	}
	failed, _ := r.s.Create(ctx, "hermes", "failed", dev)
	fseq := r.apply(failed.ID, Update{Status: StatusFailed, ErrorCode: sp("boom"), Finished: true}, "failed")
	if got, err := r.s.ToReport(ctx, ""); err != nil || !slices.Equal(pendingOf(got), pend(failed, dev, fseq)) {
		t.Fatalf("to report %+v %v", got, err)
	}
}

func TestNoticesOnePerTaskOldestFirst(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	dev, other := r.device(), r.device()
	a, _ := r.s.Create(ctx, "hermes", "a", dev)
	r.apply(a.ID, Update{Status: StatusRunning, ExternalRef: sp("a")}, "progress")
	wait := r.apply(a.ID, Update{Status: StatusWaitingInput, Question: &Question{Kind: QuestionClarify, Text: "which?"}}, "waiting_input")
	b, _ := r.s.Create(ctx, "hermes", "b", dev)
	bDone := r.apply(b.ID, Update{Status: StatusDone, Finished: true}, "done")
	for _, n := range []struct {
		id   uuid.UUID
		seq  int
		text string
	}{{a.ID, wait, "a asks"}, {b.ID, bDone, "b is done"}} {
		if err := r.s.AddNotice(ctx, Notice{TaskID: n.id, EventSeq: n.seq, DeviceID: dev, Text: n.text}); err != nil {
			t.Fatal(err)
		}
	}
	next := func(want string, stale bool) Notice {
		t.Helper()
		n, err := r.s.NextNotice(ctx, dev)
		if err != nil || n.Text != want || n.Stale != stale || n.DeviceID != dev {
			t.Fatalf("next %+v %v, want %q stale %v", n, err, want, stale)
		}
		return n
	}
	if n := next("a asks", false); n.TaskID != a.ID || n.ShortID != a.ShortID || n.EventSeq != wait {
		t.Fatalf("notice %+v", n)
	}
	if _, err := r.s.NextNotice(ctx, other); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other device: %v", err)
	}
	// the question was answered: what it said is no news now
	r.apply(a.ID, Update{Status: StatusRunning}, "progress")
	next("a asks", true)
	aDone := r.apply(a.ID, Update{Status: StatusDone, Finished: true}, "done")
	if err := r.s.AddNotice(ctx, Notice{TaskID: a.ID, EventSeq: aDone, DeviceID: dev, Text: "a is done"}); err != nil {
		t.Fatal(err)
	}
	// a late notice of an older event never replaces a newer one
	if err := r.s.AddNotice(ctx, Notice{TaskID: a.ID, EventSeq: wait, DeviceID: dev, Text: "a asks again"}); err != nil {
		t.Fatal(err)
	}
	if n, err := r.s.Undelivered(ctx, dev); err != nil || n != 2 {
		t.Fatalf("undelivered %d %v", n, err)
	}
	next("b is done", false)
	if err := r.s.Delivered(ctx, Notice{TaskID: b.ID, ShortID: b.ShortID, EventSeq: bDone}); err != nil {
		t.Fatal(err)
	}
	next("a is done", false)
	if devs, err := r.s.NoticeDevices(ctx); err != nil || !slices.Equal(devs, []uuid.UUID{dev}) {
		t.Fatalf("devices %v %v", devs, err)
	}
	if err := r.s.Delivered(ctx, Notice{TaskID: a.ID, ShortID: a.ShortID, EventSeq: aDone}); err != nil {
		t.Fatal(err)
	}
	// delivered is final: the same event is never noticed twice
	if err := r.s.AddNotice(ctx, Notice{TaskID: a.ID, EventSeq: aDone, DeviceID: dev, Text: "a is done"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.NextNotice(ctx, dev); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delivery: %v", err)
	}
	if n, _ := r.s.Undelivered(ctx, uuid.Nil); n != 0 {
		t.Fatalf("undelivered %d", n)
	}
	if devs, _ := r.s.NoticeDevices(ctx); len(devs) != 0 {
		t.Fatalf("devices %v", devs)
	}

	c, _ := r.s.Create(ctx, "hermes", "c", dev)
	cDone := r.apply(c.ID, Update{Status: StatusDone, Finished: true}, "done")
	if err := r.s.AddNotice(ctx, Notice{TaskID: c.ID, EventSeq: cDone, DeviceID: dev, Text: ""}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty text: %v", err)
	}
	if err := r.s.AddNotice(ctx, Notice{TaskID: c.ID, EventSeq: cDone, DeviceID: dev, Text: strings.Repeat("é", MaxNotice+1)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("long text: %v", err)
	}
	_ = r.s.AddNotice(ctx, Notice{TaskID: c.ID, EventSeq: cDone, DeviceID: dev, Text: "c is done"})
	if err := r.s.DropNotice(ctx, c.ID, cDone); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.NextNotice(ctx, dev); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after the drop: %v", err)
	}
	// a device that goes takes its notices along
	_ = r.s.AddNotice(ctx, Notice{TaskID: c.ID, EventSeq: cDone, DeviceID: other, Text: "c for the other"})
	if _, err := r.pool.Exec(ctx, `DELETE FROM devices WHERE id = $1`, other); err != nil {
		t.Fatal(err)
	}
	if n, _ := r.s.Undelivered(ctx, uuid.Nil); n != 0 {
		t.Fatalf("undelivered after the device went: %d", n)
	}
}

// a spoken update goes after 14 days, as transcripts do (D8); one still
// waiting stays with its task
func TestRetentionPrunesOldDeliveredNotices(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	dev := r.device()
	var tks []Task
	for _, title := range []string{"old", "fresh", "waiting"} {
		tk, _ := r.s.Create(ctx, "hermes", title, dev)
		seq := r.apply(tk.ID, Update{Status: StatusDone, Finished: true}, "done")
		if err := r.s.AddNotice(ctx, Notice{TaskID: tk.ID, EventSeq: seq, DeviceID: dev, Text: title + " notice"}); err != nil {
			t.Fatal(err)
		}
		if title != "waiting" {
			if err := r.s.Delivered(ctx, Notice{TaskID: tk.ID, ShortID: tk.ShortID, EventSeq: seq}); err != nil {
				t.Fatal(err)
			}
		}
		tks = append(tks, tk)
	}
	if _, err := r.pool.Exec(ctx, `UPDATE task_notices SET delivered_at = now() - interval '15 days' WHERE task_id = $1`, tks[0].ID); err != nil {
		t.Fatal(err)
	}
	notices := func() []string {
		rows, err := r.pool.Query(ctx, `SELECT text FROM task_notices ORDER BY text`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	var buf syncBuf
	stop := r.s.RunRetention(ctx, slog.New(slog.NewTextHandler(&buf, nil)), 20*time.Millisecond)
	t.Cleanup(stop)
	deadline := time.Now().Add(5 * time.Second)
	for slices.Contains(notices(), "old notice") {
		if time.Now().After(deadline) {
			t.Fatalf("old notice kept: %v", notices())
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	if got := notices(); !slices.Equal(got, []string{"fresh notice", "waiting notice"}) {
		t.Fatalf("notices %v", got)
	}
	if !strings.Contains(buf.String(), "notices_pruned=") || strings.Contains(buf.String(), "notice ") {
		t.Fatalf("logs %q", buf.String())
	}
}

// a mark waits for an event being stored meanwhile and sees it: the newer
// event stays to be told
func TestMarkSeesAConcurrentEvent(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	dev := r.device()
	tk, _ := r.s.Create(ctx, "hermes", "t", dev)
	r.apply(tk.ID, Update{Status: StatusRunning, ExternalRef: sp("ref")}, "progress")
	asked := r.apply(tk.ID, Update{Status: StatusWaitingInput, Question: &Question{Kind: QuestionClarify, Text: "which?"}}, "waiting_input")
	// an Apply of a done event, caught between its writes and its commit
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE tasks SET status = 'done', question = NULL, finished_at = now(), reported_at = NULL WHERE id = $1`, tk.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO task_events (task_id, seq, kind) VALUES ($1, $2, 'done')`, tk.ID, asked+1); err != nil {
		t.Fatal(err)
	}
	marked := make(chan error, 1)
	go func() { marked <- r.s.MarkReportedAt(ctx, tk.ShortID, asked) }()
	waitUntil(t, "the mark waiting on the task", func() bool {
		var n int
		_ = r.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&n)
		return n > 0
	})
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-marked; err != nil {
		t.Fatal(err)
	}
	if r.reported(tk.ShortID) {
		t.Fatal("the mark of the question hid the done event")
	}
}

// a spoken notice of a question tells that question, by its token, on its
// device only; the first telling stays
func TestDeliveredNoticeTellsItsQuestion(t *testing.T) {
	r := newMRig(t)
	ctx := context.Background()
	dev, other := r.device(), r.device()
	tk, _ := r.s.Create(ctx, "fake", "t", dev)
	r.apply(tk.ID, Update{Status: StatusRunning, ExternalRef: sp("ref")}, "progress")
	asked := r.apply(tk.ID, Update{Status: StatusWaitingInput, Question: &Question{Kind: QuestionApproval, Text: "rm it?", Token: "a1"}}, "waiting_input")
	told := func(token string, device uuid.UUID, turn string) bool {
		t.Helper()
		ok, err := r.m.ToldBefore(ctx, tk.ShortID, token, device, turn)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if err := r.s.AddNotice(ctx, Notice{TaskID: tk.ID, EventSeq: asked, DeviceID: dev, Token: "a1", Text: "it asks"}); err != nil {
		t.Fatal(err)
	}
	n, err := r.s.NextNotice(ctx, dev)
	if err != nil || n.Token != "a1" {
		t.Fatalf("notice %+v %v", n, err)
	}
	if told("a1", dev, "turn-1") {
		t.Fatal("told before it was spoken")
	}
	if err := r.s.Delivered(ctx, n); err != nil {
		t.Fatal(err)
	}
	if !told("a1", dev, "turn-1") || told("a1", other, "turn-1") || told("a2", dev, "turn-1") || told("a1", dev, "") {
		t.Fatal("told on the wrong device, question or turn")
	}
	// a turn that reads it again later does not make it this turn's
	if err := r.m.MarkTold(ctx, tk.ShortID, "a1", dev, "turn-1"); err != nil || !told("a1", dev, "turn-1") {
		t.Fatalf("the first telling was replaced: %v", err)
	}
	if err := r.m.MarkTold(ctx, tk.ShortID, "b1", other, "turn-2"); err != nil || told("b1", other, "turn-2") || !told("b1", other, "turn-3") {
		t.Fatalf("a turn's own reading: %v", err)
	}
}
