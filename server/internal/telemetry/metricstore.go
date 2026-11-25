package telemetry

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// MetricRetention is how long metric points are kept (M6b-D9).
const MetricRetention = 24 * time.Hour

// MetricRow is one stored data point; Attrs is a JSON object.
type MetricRow struct {
	DeviceID pgtype.UUID
	Name     string
	// Kind is KindGauge or KindSum
	Kind  int16
	Attrs []byte
	Time  time.Time
	// Start is a sum's start, zero for none
	Start time.Time
	Value float64
}

// MetricStore writes data points into metric_samples.
type MetricStore struct {
	q *db.Queries

	mu   sync.Mutex
	seen []func([]MetricRow)
}

func NewMetricStore(q *db.Queries) *MetricStore { return &MetricStore{q: q} }

// Insert writes rows in one statement.
func (s *MetricStore) Insert(ctx context.Context, rows []MetricRow) error {
	if len(rows) == 0 {
		return nil
	}
	p := db.InsertMetricSamplesParams{
		DeviceID: make([]pgtype.UUID, len(rows)), Name: make([]string, len(rows)), Kind: make([]int16, len(rows)),
		Attrs: make([]string, len(rows)), Ts: make([]pgtype.Timestamptz, len(rows)), StartTs: make([]pgtype.Timestamptz, len(rows)),
		Value: make([]float64, len(rows)),
	}
	for i, r := range rows {
		p.DeviceID[i], p.Name[i], p.Attrs[i], p.Value[i] = r.DeviceID, r.Name, string(r.Attrs), r.Value
		p.Kind[i], p.Ts[i] = r.Kind, pgtype.Timestamptz{Time: r.Time, Valid: true}
		p.StartTs[i] = pgtype.Timestamptz{Time: r.Start, Valid: !r.Start.IsZero()}
	}
	live, err := s.q.InsertMetricSamples(ctx, p)
	if err != nil {
		return storeError("insert metrics", err)
	}
	rows = stored(rows, func(r *MetricRow) pgtype.UUID { return r.DeviceID }, live)
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
func (s *MetricStore) OnInsert(fn func(rows []MetricRow)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen[:len(s.seen):len(s.seen)], fn)
}

// Prune deletes the points before before, returning how many.
func (s *MetricStore) Prune(ctx context.Context, before time.Time) (int64, error) {
	n, err := s.q.PruneMetricSamples(ctx, pgtype.Timestamptz{Time: before, Valid: true})
	return n, storeError("prune metrics", err)
}
