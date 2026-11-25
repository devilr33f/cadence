package tasks

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

type rig struct {
	t    *testing.T
	pool *pgxpool.Pool
	s    *Store
}

func newRig(t *testing.T) *rig {
	pool := storetest.Pool(t)
	return &rig{t: t, pool: pool, s: NewStore(pool)}
}

func (r *rig) device() uuid.UUID {
	r.t.Helper()
	d, err := db.New(r.pool).CreateDevice(context.Background(), db.CreateDeviceParams{Name: rand.Text(), Model: "sim"})
	if err != nil {
		r.t.Fatal(err)
	}
	return d.ID.Bytes
}

func sp(s string) *string { return &s }

func TestStoreCRUDAndShortIDs(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	dev := r.device()
	a, err := r.s.Create(ctx, "hermes", "first", dev)
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.s.Create(ctx, "hermes", "second", uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	na, nb := shortNum(t, a.ShortID), shortNum(t, b.ShortID)
	if nb != na+1 {
		t.Fatalf("short ids %s, %s", a.ShortID, b.ShortID)
	}
	if a.Status != StatusQueued || a.Origin != dev || b.Origin != uuid.Nil || a.Progress != "" || a.Question != nil {
		t.Fatalf("created %+v / %+v", a, b)
	}
	got, err := r.s.Get(ctx, a.ShortID)
	if err != nil || got.ID != a.ID {
		t.Fatalf("get %+v %v", got, err)
	}
	if _, err := r.s.Get(ctx, "t-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}

	q := &Question{Kind: QuestionApproval, Text: "ok?", Options: []string{"yes", "no"}}
	upd, seq, err := r.s.Apply(ctx, a.ID, Update{Status: StatusWaitingInput, Progress: "p", Question: q, ExternalRef: sp("ref-a")}, "waiting_input")
	if err != nil || seq != 1 || upd.Question == nil || upd.Question.Text != "ok?" || len(upd.Question.Options) != 2 || upd.ExternalRef != "ref-a" {
		t.Fatalf("apply %+v seq %d %v", upd, seq, err)
	}
	_, seq, err = r.s.Apply(ctx, a.ID, Update{Status: StatusDone, Result: sp("r"), Finished: true}, "done")
	if err != nil || seq != 2 {
		t.Fatalf("second apply seq %d %v", seq, err)
	}
	ref, err := r.s.ByRef(ctx, "hermes", "ref-a")
	if err != nil || ref.ID != a.ID || ref.Result != "r" || ref.FinishedAt == nil || ref.Question != nil || ref.ExternalRef != "ref-a" {
		t.Fatalf("by ref %+v %v", ref, err)
	}
	if _, err := r.s.ByRef(ctx, "other", "ref-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other kind: %v", err)
	}

	all, err := r.s.List(ctx, Filter{})
	if err != nil || len(all) != 2 || all[0].ID != b.ID {
		t.Fatalf("list %v %v", all, err)
	}
	done, _ := r.s.List(ctx, Filter{Statuses: []string{StatusDone}})
	byDev, _ := r.s.List(ctx, Filter{Origin: dev})
	byKind, _ := r.s.List(ctx, Filter{Kind: "nope"})
	open, _ := r.s.Open(ctx)
	if len(done) != 1 || len(byDev) != 1 || len(byKind) != 0 || len(open) != 1 || open[0].ID != b.ID {
		t.Fatalf("filters %d %d %d %d", len(done), len(byDev), len(byKind), len(open))
	}
}

func shortNum(t *testing.T, id string) int {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimPrefix(id, "t-"))
	if err != nil || !strings.HasPrefix(id, "t-") {
		t.Fatalf("short id %q", id)
	}
	return n
}

func TestStoreChecks(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.s.Create(ctx, "hermes", strings.Repeat("x", MaxTitle+1), uuid.Nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("long title: %v", err)
	}
	if _, err := r.s.Create(ctx, "hermes", "", uuid.Nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty title: %v", err)
	}
	tk, _ := r.s.Create(ctx, "hermes", "t", uuid.Nil)
	cases := map[string]Update{
		"status":   {Status: "paused"},
		"progress": {Status: StatusRunning, Progress: strings.Repeat("p", MaxProgress+1)},
		"result":   {Status: StatusDone, Result: sp(strings.Repeat("r", MaxResult+1))},
		"question": {Status: StatusWaitingInput, Question: &Question{Kind: "other", Text: "x"}},
		"qtext":    {Status: StatusWaitingInput, Question: &Question{Kind: QuestionClarify, Text: ""}},
		"code":     {Status: StatusFailed, ErrorCode: sp("has space")},
		"longcode": {Status: StatusFailed, ErrorCode: sp(strings.Repeat("a", 65))},
	}
	for name, u := range cases {
		if _, _, err := r.s.Apply(ctx, tk.ID, u, ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := r.s.Apply(ctx, tk.ID, Update{Status: StatusRunning}, "running"); !errors.Is(err, ErrInvalid) {
		t.Errorf("event kind: %v", err)
	}
	// a failed event insert rolls the update back
	got, _ := r.s.Get(ctx, tk.ShortID)
	if got.Status != StatusQueued {
		t.Errorf("status after rejected applies: %s", got.Status)
	}
	if _, _, err := r.s.Apply(ctx, uuid.New(), Update{Status: StatusRunning}, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
}

func TestDeviceDeletionNullsOrigin(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	dev := r.device()
	tk, _ := r.s.Create(ctx, "hermes", "t", dev)
	if _, err := r.pool.Exec(ctx, `DELETE FROM devices WHERE id = $1`, dev); err != nil {
		t.Fatal(err)
	}
	got, err := r.s.Get(ctx, tk.ShortID)
	if err != nil || got.Origin != uuid.Nil {
		t.Fatalf("origin %v %v", got.Origin, err)
	}
}

func TestRetention(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	old, _ := r.s.Create(ctx, "hermes", "old", uuid.Nil)
	fresh, _ := r.s.Create(ctx, "hermes", "fresh", uuid.Nil)
	open, _ := r.s.Create(ctx, "hermes", "open", uuid.Nil)
	for _, id := range []uuid.UUID{old.ID, fresh.ID} {
		if _, _, err := r.s.Apply(ctx, id, Update{Status: StatusDone, Finished: true}, "done"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.pool.Exec(ctx, `UPDATE tasks SET finished_at = now() - interval '31 days' WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}
	var buf lockedBuffer
	stop := r.s.RunRetention(ctx, slog.New(slog.NewTextHandler(&buf, nil)), 20*time.Millisecond)
	// the tick logs only after its last step, so waiting for the deletion alone
	// could stop it in between
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := r.s.Get(ctx, old.ShortID)
		if errors.Is(err, ErrNotFound) && strings.Contains(buf.String(), "tasks_pruned") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("old task kept or no log: %v %q", err, buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	for _, tk := range []Task{fresh, open} {
		if _, err := r.s.Get(ctx, tk.ShortID); err != nil {
			t.Fatalf("%s pruned: %v", tk.Title, err)
		}
	}
	var n int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM task_events WHERE task_id = $1`, old.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("events of a pruned task: %d %v", n, err)
	}
	if !strings.Contains(buf.String(), "tasks_pruned") || strings.Contains(buf.String(), "old") {
		t.Fatalf("logs %q", buf.String())
	}
}

func TestConcurrentCreatesGetDistinctShortIDs(t *testing.T) {
	r := newRig(t)
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tk, err := r.s.Create(context.Background(), "hermes", "t", uuid.Nil)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- tk.ShortID
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate %s", id)
		}
		seen[id] = true
	}
}

// the user's words on an approval go after 14 days, as transcripts do; who
// decided what stays for the task's audit
func TestRetentionScrubsApprovalWords(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	tk, _ := r.s.Create(ctx, "hermes", "t", uuid.Nil)
	answer := func(words string) []byte {
		b, _ := answerOf(DecisionOnce, Evidence{Via: ViaVoice, Device: "dev-1", Words: words})
		return b
	}
	oldSeq, err := r.s.Answered(ctx, tk.ID, answer("delete the OLD pods"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Answered(ctx, tk.ID, answer("wipe the new disk")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pool.Exec(ctx, `UPDATE task_events SET at = now() - interval '15 days' WHERE task_id = $1 AND seq = $2`, tk.ID, oldSeq); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	stop := r.s.RunRetention(ctx, slog.New(slog.NewTextHandler(&buf, nil)), 20*time.Millisecond)
	t.Cleanup(stop)
	answers := func() []string {
		rows, err := r.pool.Query(ctx, `SELECT answer::text FROM task_events WHERE task_id = $1 ORDER BY seq`, tk.ID)
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
	deadline := time.Now().Add(5 * time.Second)
	for strings.Contains(strings.Join(answers(), ""), "OLD") {
		if time.Now().After(deadline) {
			t.Fatalf("old words kept: %v", answers())
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := answers()
	if len(got) != 2 || got[0] != `{"via": "voice", "device": "dev-1", "decision": "once"}` || !strings.Contains(got[1], "wipe the new disk") {
		t.Fatalf("answers %v", got)
	}
	stop()
	if strings.Contains(buf.String(), "wipe") {
		t.Fatalf("logs %q", buf.String())
	}
}

// each retention step runs on its own: a prune that fails skips neither the
// words' scrub nor the notices' prune
func TestRetentionStepsRunAlone(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.pool.Exec(ctx, `CREATE FUNCTION refuse() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused'; END $$;
		CREATE TRIGGER refuse BEFORE DELETE ON tasks FOR EACH ROW EXECUTE FUNCTION refuse()`); err != nil {
		t.Fatal(err)
	}
	dev := r.device()
	old, _ := r.s.Create(ctx, "hermes", "old", dev)
	seq, _ := r.s.Answered(ctx, old.ID, func() []byte {
		b, _ := answerOf(DecisionOnce, Evidence{Via: ViaVoice, Device: "dev-1", Words: "delete the OLD pods"})
		return b
	}())
	done := r.apply(old.ID, Update{Status: StatusDone, Finished: true}, "done")
	if err := r.s.AddNotice(ctx, Notice{TaskID: old.ID, EventSeq: done, DeviceID: dev, Text: "old notice"}); err != nil {
		t.Fatal(err)
	}
	if err := r.s.Delivered(ctx, Notice{TaskID: old.ID, ShortID: old.ShortID, EventSeq: done}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE tasks SET finished_at = now() - interval '31 days' WHERE id = $1`,
		`UPDATE task_events SET at = now() - interval '15 days' WHERE task_id = $1 AND seq = ` + strconv.Itoa(seq),
		`UPDATE task_notices SET delivered_at = now() - interval '15 days' WHERE task_id = $1`,
	} {
		if _, err := r.pool.Exec(ctx, q, old.ID); err != nil {
			t.Fatal(err)
		}
	}
	var buf syncBuf
	stop := r.s.RunRetention(ctx, slog.New(slog.NewTextHandler(&buf, nil)), 20*time.Millisecond)
	t.Cleanup(stop)
	count := func(q string) int {
		var n int
		if err := r.pool.QueryRow(ctx, q, old.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	deadline := time.Now().Add(5 * time.Second)
	for count(`SELECT count(*) FROM task_notices WHERE task_id = $1`) != 0 ||
		count(`SELECT count(*) FROM task_events WHERE task_id = $1 AND answer ? 'words'`) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("a failed prune skipped the scrub or the notices")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	if _, err := r.s.Get(ctx, old.ShortID); err != nil {
		t.Fatalf("the refused prune deleted the task: %v", err)
	}
	if logs := buf.String(); !strings.Contains(logs, "task_prune_failed") || !strings.Contains(logs, "op=prune") || strings.Contains(logs, "OLD") {
		t.Fatalf("logs %q", logs)
	}
}

// lockedBuffer is a log sink the retention goroutine writes while the test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
