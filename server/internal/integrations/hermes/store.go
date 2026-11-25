package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

const (
	phaseActive = "active"
	phasePaused = "paused"
	phaseClosed = "closed"
)

// row is what survives a restart: the run a task's session is on, the highest
// generation submitted, and the prompt the task waits on with when it showed
// (Hermes's text: user data, DB only).
type row struct {
	session string
	task    uuid.UUID
	run     string
	gen     int32
	tried   int32
	phase   string
	control *Control
	shownAt time.Time
}

var errNoRow = errors.New("hermes: no run for this ref")

// store errors carry no row content: postgres detail lines quote it
type store struct{ q *db.Queries }

func newStore(pool *pgxpool.Pool) *store { return &store{q: db.New(pool)} }

func (s *store) create(ctx context.Context, r row) error {
	if err := s.q.CreateHermesRun(ctx, db.CreateHermesRunParams{
		SessionID: r.session, TaskID: pgtype.UUID{Bytes: r.task, Valid: true}, RunID: r.run, Generation: r.gen,
	}); err != nil {
		return fail(CodeStore)
	}
	return nil
}

func (s *store) get(ctx context.Context, session string) (row, error) {
	r, err := s.q.GetHermesRun(ctx, session)
	if errors.Is(err, pgx.ErrNoRows) {
		return row{}, errNoRow
	}
	if err != nil {
		return row{}, fail(CodeStore)
	}
	out := row{session: r.SessionID, task: r.TaskID.Bytes, run: r.RunID, gen: r.Generation, tried: r.Tried, phase: r.Phase}
	if len(r.Control) > 0 {
		var c Control
		if json.Unmarshal(r.Control, &c) != nil {
			return row{}, fail(CodeStore)
		}
		out.control = &c
	}
	if r.ShownAt.Valid {
		out.shownAt = r.ShownAt.Time
	}
	return out, nil
}

func (s *store) save(ctx context.Context, r row) error {
	var control []byte
	var shown pgtype.Timestamptz
	if r.control != nil {
		control = []byte(r.control.canonical())
		shown = pgtype.Timestamptz{Time: r.shownAt, Valid: !r.shownAt.IsZero()}
	}
	n, err := s.q.SaveHermesRun(ctx, db.SaveHermesRunParams{
		SessionID: r.session, RunID: r.run, Generation: r.gen, Tried: max(r.tried, r.gen), Phase: r.phase,
		Control: control, ShownAt: shown,
	})
	if err != nil || n != 1 {
		return fail(CodeStore)
	}
	return nil
}
