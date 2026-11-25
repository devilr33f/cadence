package telemetry

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// errStore stands for a store failure: postgres detail lines quote the row
// that failed, so the cause never leaves the store
var errStore = errors.New("telemetry: store failed")

func storeError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("telemetry: %s: %w", op, err)
	}
	// query_canceled: the statement timeout cut it
	if pg := (*pgconn.PgError)(nil); errors.As(err, &pg) && pg.Code == "57014" {
		return fmt.Errorf("telemetry: %s: %w", op, errReadTimeout)
	}
	return fmt.Errorf("telemetry: %s: %w", op, errStore)
}

// LogRow is one stored log line.
type LogRow struct {
	// Time is the record's time, as recordTime has it
	Time     time.Time
	Observed time.Time
	// DeviceID is not valid for the server's own lines
	DeviceID  pgtype.UUID
	Source    string
	Severity  int16
	Body      string
	Truncated bool
	Continued bool
}

// LogStore writes log lines into device_logs, one partition per UTC day of
// their time, made on demand: an insert never fails for a missing one.
type LogStore struct {
	q *db.Queries

	mu sync.Mutex
	// days whose partition this store made or saw made since its last drop
	known map[time.Time]bool
	seen  []func([]LogRow)
}

func NewLogStore(q *db.Queries) *LogStore { return &LogStore{q: q, known: map[time.Time]bool{}} }

func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Ensure makes the partitions of the days of ts that do not exist yet.
func (s *LogStore) Ensure(ctx context.Context, ts ...time.Time) error {
	for _, t := range ts {
		day := utcDay(t)
		s.mu.Lock()
		known := s.known[day]
		s.mu.Unlock()
		if known {
			continue
		}
		if err := s.q.EnsureDeviceLogPartition(ctx, pgtype.Date{Time: day, Valid: true}); err != nil {
			return storeError("partition", err)
		}
		s.mu.Lock()
		s.known[day] = true
		s.mu.Unlock()
	}
	return nil
}

// DropBefore drops the partitions of the days before day's, returning how many.
func (s *LogStore) DropBefore(ctx context.Context, day time.Time) (int, error) {
	n, err := s.q.DropDeviceLogPartitions(ctx, pgtype.Date{Time: utcDay(day), Valid: true})
	s.forget()
	return int(n), storeError("drop partitions", err)
}

func (s *LogStore) forget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.known)
}

// Insert writes rows in one statement, making their days' partitions first.
func (s *LogStore) Insert(ctx context.Context, rows []LogRow) error {
	if len(rows) == 0 {
		return nil
	}
	days := map[time.Time]bool{}
	p := db.InsertDeviceLogsParams{
		Ts: make([]pgtype.Timestamptz, len(rows)), ObservedTs: make([]pgtype.Timestamptz, len(rows)),
		DeviceID: make([]pgtype.UUID, len(rows)), Source: make([]string, len(rows)), Severity: make([]int16, len(rows)),
		Body: make([]string, len(rows)), Truncated: make([]bool, len(rows)), Continued: make([]bool, len(rows)),
	}
	for i, r := range rows {
		days[utcDay(r.Time)] = true
		p.Ts[i] = pgtype.Timestamptz{Time: r.Time, Valid: true}
		p.ObservedTs[i] = pgtype.Timestamptz{Time: r.Observed, Valid: true}
		p.DeviceID[i], p.Source[i], p.Severity[i], p.Body[i] = r.DeviceID, r.Source, r.Severity, r.Body
		p.Truncated[i], p.Continued[i] = r.Truncated, r.Continued
	}
	list := make([]time.Time, 0, len(days))
	for d := range days {
		list = append(list, d)
	}
	if err := s.Ensure(ctx, list...); err != nil {
		return err
	}
	live, err := s.q.InsertDeviceLogs(ctx, p)
	if noPartition(err) {
		// dropped behind this store's back: make it again, once
		s.forget()
		if err := s.Ensure(ctx, list...); err != nil {
			return err
		}
		live, err = s.q.InsertDeviceLogs(ctx, p)
	}
	if err != nil {
		return storeError("insert logs", err)
	}
	rows = stored(rows, func(r *LogRow) pgtype.UUID { return r.DeviceID }, live)
	s.mu.Lock()
	seen := s.seen
	s.mu.Unlock()
	for _, fn := range seen {
		fn(rows)
	}
	return nil
}

// OnInsert calls fn with the rows of each insert once stored; fn must not
// block nor keep rows, whose slice its caller may use again.
func (s *LogStore) OnInsert(fn func(rows []LogRow)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen[:len(s.seen):len(s.seen)], fn)
}

// noPartition is postgres' "no partition of relation found for row".
func noPartition(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23514"
}

// stored are the rows an insert kept: the server's own (no device) and those
// of the devices it found, live; a device deleted since its request was let in
// stores nothing, and its rows reach no listener.
func stored[R any](rows []R, device func(*R) pgtype.UUID, live []pgtype.UUID) []R {
	kept := func(r *R) bool {
		id := device(r)
		return !id.Valid || slices.Contains(live, id)
	}
	for i := range rows {
		if kept(&rows[i]) {
			continue
		}
		out := slices.Clone(rows[:i])
		for j := i + 1; j < len(rows); j++ {
			if kept(&rows[j]) {
				out = append(out, rows[j])
			}
		}
		return out
	}
	return rows
}
