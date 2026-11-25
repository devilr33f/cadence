package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

type Store struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool, q: db.New(pool)} }

// wrap keeps row content out of errors: postgres detail lines quote the failing row.
func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("tasks: %s: %w", op, ErrNotFound)
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		if len(pg.Code) == 5 && (pg.Code[:2] == "23" || pg.Code == "22P02") {
			return fmt.Errorf("tasks: %s: %w (%s)", op, ErrInvalid, pg.ConstraintName)
		}
		return &DBError{Op: op, Code: pg.Code}
	}
	return fmt.Errorf("tasks: %s: %w", op, err)
}

// DBError is a postgres failure without its message, which may quote row text.
type DBError struct{ Op, Code string }

func (e *DBError) Error() string { return "tasks: " + e.Op + ": db error " + e.Code }

// errCode is a short log code for a store error.
func errCode(err error) string {
	var d *DBError
	switch {
	case errors.As(err, &d):
		return d.Code
	case errors.Is(err, ErrInvalid):
		return "invalid"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	}
	return "error"
}

func pgUUID(u uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: u, Valid: u != uuid.Nil} }

func optText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

func toTask(r db.Task) (Task, error) {
	t := Task{
		ID: r.ID.Bytes, ShortID: r.ShortID, Kind: r.Kind, Title: r.Title, Status: r.Status, Progress: r.Progress,
		Result: r.Result.String, ErrorCode: r.ErrorCode.String, ExternalRef: r.ExternalRef.String,
		CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
	if r.OriginDeviceID.Valid {
		t.Origin = r.OriginDeviceID.Bytes
	}
	if r.FinishedAt.Valid {
		v := r.FinishedAt.Time
		t.FinishedAt = &v
	}
	if r.ReportedAt.Valid {
		v := r.ReportedAt.Time
		t.ReportedAt = &v
	}
	if len(r.Question) > 0 {
		var q Question
		if err := json.Unmarshal(r.Question, &q); err != nil {
			return Task{}, errors.New("tasks: decode question")
		}
		t.Question = &q
	}
	return t, nil
}

func toTasks(rows []db.Task) ([]Task, error) {
	out := make([]Task, 0, len(rows))
	for _, r := range rows {
		t, err := toTask(r)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// Create stores a queued task with the next short id.
func (s *Store) Create(ctx context.Context, kind, title string, origin uuid.UUID) (Task, error) {
	r, err := s.q.CreateTask(ctx, db.CreateTaskParams{Kind: kind, Title: title, OriginDeviceID: pgUUID(origin)})
	if err != nil {
		return Task{}, wrap("create", err)
	}
	return toTask(r)
}

func (s *Store) Get(ctx context.Context, shortID string) (Task, error) {
	r, err := s.q.GetTaskByShortID(ctx, shortID)
	if err != nil {
		return Task{}, wrap("get", err)
	}
	return toTask(r)
}

func (s *Store) ByRef(ctx context.Context, kind, ref string) (Task, error) {
	r, err := s.q.GetTaskByRef(ctx, db.GetTaskByRefParams{Kind: kind, ExternalRef: pgtype.Text{String: ref, Valid: true}})
	if err != nil {
		return Task{}, wrap("by ref", err)
	}
	return toTask(r)
}

func (s *Store) List(ctx context.Context, f Filter) ([]Task, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	statuses := f.Statuses
	if statuses == nil {
		statuses = []string{}
	}
	rows, err := s.q.ListTasks(ctx, db.ListTasksParams{
		Kind: f.Kind, Statuses: statuses, OriginDeviceID: pgUUID(f.Origin), MaxRows: int32(limit),
	})
	if err != nil {
		return nil, wrap("list", err)
	}
	return toTasks(rows)
}

// Open lists tasks that are not finished, oldest first.
func (s *Store) Open(ctx context.Context) ([]Task, error) {
	rows, err := s.q.ListOpenTasks(ctx)
	if err != nil {
		return nil, wrap("open", err)
	}
	return toTasks(rows)
}

// Update is the new state of a task; Apply writes all of it.
type Update struct {
	Status      string
	Progress    string
	Question    *Question
	Result      *string
	ErrorCode   *string
	ExternalRef *string // nil keeps
	Finished    bool
	// OnlyFrom, when set, makes Apply write only a task still in that status
	// (else ErrNotFound): a late writer never overwrites a concurrent change.
	OnlyFrom string
}

// Apply writes u and, when eventKind is set, the next task_events row in one
// transaction. It returns the stored task and the row's seq (0: none). A
// reportable event leaves the task unreported until it is told.
func (s *Store) Apply(ctx context.Context, id uuid.UUID, u Update, eventKind string) (Task, int, error) {
	var q []byte
	if u.Question != nil {
		b, err := json.Marshal(u.Question)
		if err != nil {
			return Task{}, 0, errors.New("tasks: encode question")
		}
		q = b
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Task{}, 0, wrap("begin", err)
	}
	// a no-op once committed
	defer func() { _ = tx.Rollback(ctx) }()
	qs := s.q.WithTx(tx)
	row, err := qs.UpdateTask(ctx, db.UpdateTaskParams{
		ID: pgUUID(id), Status: u.Status, Progress: u.Progress, Question: q,
		Result: optText(u.Result), ErrorCode: optText(u.ErrorCode), ExternalRef: optText(u.ExternalRef), Finished: u.Finished,
		Unreport: reportable(eventKind),
		OnlyFrom: pgtype.Text{String: u.OnlyFrom, Valid: u.OnlyFrom != ""},
	})
	if err != nil {
		return Task{}, 0, wrap("update", err)
	}
	seq := 0
	if eventKind != "" {
		n, err := qs.AddTaskEvent(ctx, db.AddTaskEventParams{TaskID: pgUUID(id), Kind: eventKind})
		if err != nil {
			return Task{}, 0, wrap("event", err)
		}
		seq = int(n)
	}
	if err := tx.Commit(ctx); err != nil {
		return Task{}, 0, wrap("commit", err)
	}
	t, err := toTask(row)
	return t, seq, err
}

// LatestSeq is the seq of a task's latest event, 0 for none.
func (s *Store) LatestSeq(ctx context.Context, id uuid.UUID) (int, error) {
	n, err := s.q.LatestTaskEventSeq(ctx, pgUUID(id))
	return int(n), wrap("latest seq", err)
}

// Answered adds an answered event with its payload and returns its seq.
func (s *Store) Answered(ctx context.Context, id uuid.UUID, answer []byte) (int, error) {
	n, err := s.q.AddTaskEvent(ctx, db.AddTaskEventParams{TaskID: pgUUID(id), Kind: "answered", Answer: answer})
	if err != nil {
		return 0, wrap("event", err)
	}
	return int(n), nil
}

// Prune deletes tasks finished before olderThan.
func (s *Store) Prune(ctx context.Context, olderThan time.Time) (int64, error) {
	n, err := s.q.PruneTasks(ctx, pgtype.Timestamptz{Time: olderThan, Valid: true})
	return n, wrap("prune", err)
}

// ScrubWords drops the user's words from approval answers older than
// olderThan; the decision and who gave it stay.
func (s *Store) ScrubWords(ctx context.Context, olderThan time.Time) (int64, error) {
	n, err := s.q.ScrubTaskAnswerWords(ctx, pgtype.Timestamptz{Time: olderThan, Valid: true})
	return n, wrap("scrub", err)
}

// PruneNotices deletes notices delivered before olderThan.
func (s *Store) PruneNotices(ctx context.Context, olderThan time.Time) (int64, error) {
	n, err := s.q.PruneTaskNotices(ctx, pgtype.Timestamptz{Time: olderThan, Valid: true})
	return n, wrap("prune notices", err)
}

// RunRetention prunes and scrubs at start and then every interval until ctx
// ends; stop waits for the loop. Each step runs whether the others failed.
func (s *Store) RunRetention(ctx context.Context, log *slog.Logger, every time.Duration) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	prune := func() {
		now := time.Now()
		step := func(op string, run func(context.Context, time.Time) (int64, error), olderThan time.Time) int64 {
			n, err := run(ctx, olderThan)
			if err != nil && ctx.Err() == nil {
				log.Error("task_prune_failed", "op", op, "code", errCode(err))
			}
			return n
		}
		n := step("prune", s.Prune, now.Add(-Retention))
		scrubbed := step("scrub", s.ScrubWords, now.Add(-WordsRetention))
		notices := step("notices", s.PruneNotices, now.Add(-NoticeRetention))
		if ctx.Err() == nil {
			log.Info("tasks_pruned", "count", n, "words_scrubbed", scrubbed, "notices_pruned", notices)
		}
	}
	go func() {
		defer close(done)
		prune()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				prune()
			}
		}
	}()
	return func() { cancel(); <-done }
}

// Unreported lists finished tasks (done, failed, cancelled, unknown) nobody
// has reported yet, oldest first; a zero device means all.
func (s *Store) Unreported(ctx context.Context, device uuid.UUID) ([]Task, error) {
	rows, err := s.q.ListUnreportedTasks(ctx, pgUUID(device))
	if err != nil {
		return nil, wrap("unreported", err)
	}
	return toTasks(rows)
}

// MarkReported sets reported_at once; marking again is a no-op.
func (s *Store) MarkReported(ctx context.Context, shortID string) error {
	n, err := s.q.MarkTaskReported(ctx, shortID)
	if err != nil {
		return wrap("mark reported", err)
	}
	if n == 0 {
		_, err = s.Get(ctx, shortID)
	}
	return err
}

// MarkReportedAt marks a task told up to its event seq; a no-op while a newer
// reportable event is still to be told, or once marked.
func (s *Store) MarkReportedAt(ctx context.Context, shortID string, seq int) error {
	return s.inTx(ctx, func(qs *db.Queries) error { return markReportedAt(ctx, qs, shortID, seq) })
}

// markReportedAt locks the task first, so that the mark, a statement of its
// own with a fresh snapshot, sees an event an Apply committed meanwhile.
func markReportedAt(ctx context.Context, qs *db.Queries, shortID string, seq int) error {
	if _, err := qs.LockTask(ctx, shortID); err != nil {
		return wrap("lock", err)
	}
	_, err := qs.MarkTaskReportedAt(ctx, db.MarkTaskReportedAtParams{ShortID: shortID, Seq: seq32(seq)})
	return wrap("mark reported", err)
}

// inTx runs fn in one transaction, committed when fn returns nil.
func (s *Store) inTx(ctx context.Context, fn func(qs *db.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return wrap("begin", err)
	}
	// a no-op once committed
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	return wrap("commit", tx.Commit(ctx))
}

// Pending is a task whose latest reportable event, Seq, no one told or
// turned into a notice yet; Task is as it stood with that event.
type Pending struct {
	Task Task
	Seq  int
}

// ToReport lists, oldest change first, the tasks with a device whose latest
// reportable event still needs its event turn; shortID "" means all tasks.
func (s *Store) ToReport(ctx context.Context, shortID string) ([]Pending, error) {
	rows, err := s.q.ListTasksToReport(ctx, pgtype.Text{String: shortID, Valid: shortID != ""})
	if err != nil {
		return nil, wrap("to report", err)
	}
	out := make([]Pending, 0, len(rows))
	for _, r := range rows {
		t, err := toTask(r.Task)
		if err != nil {
			return nil, err
		}
		out = append(out, Pending{Task: t, Seq: int(r.Seq)})
	}
	return out, nil
}
