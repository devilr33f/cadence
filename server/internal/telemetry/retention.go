package telemetry

import (
	"context"
	"log/slog"
	"time"
)

// MaintenanceDeps are what the maintenance loop needs.
type MaintenanceDeps struct {
	Logs    *LogStore
	Metrics *MetricStore
	// Retention is the logRetentionDays setting
	Retention func(ctx context.Context) (int, error)
	Now       func() time.Time
	Log       *slog.Logger
	// Every is the time between rounds, an hour unless set
	Every time.Duration
}

// Maintenance makes today's and tomorrow's log partitions, drops those past
// the retention and prunes metric points past MetricRetention: at start, every
// hour and at each Poke.
type Maintenance struct {
	d    MaintenanceDeps
	poke chan struct{}
}

func NewMaintenance(d MaintenanceDeps) *Maintenance {
	if d.Every <= 0 {
		d.Every = time.Hour
	}
	return &Maintenance{d: d, poke: make(chan struct{}, 1)}
}

// Poke runs a round soon, as after the retention setting changed; it never blocks.
func (m *Maintenance) Poke() {
	select {
	case m.poke <- struct{}{}:
	default:
	}
}

// Run starts the loop; stop ends it and waits for the round in progress.
func (m *Maintenance) Run(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(m.d.Every)
		defer t.Stop()
		for {
			m.round(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-m.poke:
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (m *Maintenance) round(ctx context.Context) {
	now := m.d.Now()
	today := utcDay(now)
	dropped := 0
	// the drop goes first: it forgets the days the store made, so the ones
	// ahead are asked of the store again, whoever dropped them
	if days, err := m.d.Retention(ctx); err != nil {
		m.failed(ctx, "retention")
	} else if dropped, err = m.d.Logs.DropBefore(ctx, today.AddDate(0, 0, -max(days, 1))); err != nil {
		m.failed(ctx, "drop")
	}
	if err := m.d.Logs.Ensure(ctx, today, today.AddDate(0, 0, 1)); err != nil {
		m.failed(ctx, "partitions")
	}
	pruned, err := m.d.Metrics.Prune(ctx, now.Add(-MetricRetention))
	if err != nil {
		m.failed(ctx, "prune")
	}
	if ctx.Err() == nil {
		m.d.Log.Info("telemetry_maintenance", "partitions_dropped", dropped, "metrics_pruned", pruned)
	}
}

// failed logs a round's failure by code: a cause can carry driver text
func (m *Maintenance) failed(ctx context.Context, code string) {
	if ctx.Err() == nil {
		m.d.Log.Warn("telemetry_maintenance_failed", "code", code)
	}
}
