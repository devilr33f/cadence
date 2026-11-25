package telemetry_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

// eventually polls cond for up to 10 s
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMaintenance(t *testing.T) {
	pool := storetest.Pool(t)
	q := db.New(pool)
	ctx := context.Background()
	dev, err := q.CreateDevice(ctx, db.CreateDeviceParams{Name: "d", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	logs, metrics := telemetry.NewLogStore(q), telemetry.NewMetricStore(q)
	// lines of the last ten days, and points of the last two
	for d := range 10 {
		if err := logs.Insert(ctx, []telemetry.LogRow{serverLine(start.AddDate(0, 0, -d), "line")}); err != nil {
			t.Fatal(err)
		}
	}
	var points []telemetry.MetricRow
	for _, age := range []time.Duration{0, 23 * time.Hour, 25 * time.Hour, 47 * time.Hour} {
		points = append(points, telemetry.MetricRow{DeviceID: dev.ID, Name: "g", Kind: telemetry.KindGauge, Attrs: []byte("{}"), Time: start.Add(-age), Value: 1})
	}
	if err := metrics.Insert(ctx, points); err != nil {
		t.Fatal(err)
	}
	clk := &clock{now: start}
	var days atomic.Int32
	days.Store(7)
	var failRetention atomic.Bool
	buf := &syncBuffer{}
	m := telemetry.NewMaintenance(telemetry.MaintenanceDeps{
		Logs: logs, Metrics: metrics, Now: clk.Now, Log: slog.New(slog.NewJSONHandler(buf, nil)), Every: time.Hour,
		Retention: func(context.Context) (int, error) {
			if failRetention.Load() {
				return 0, errors.New("settings down: password=hunter2")
			}
			return int(days.Load()), nil
		},
	})
	stop := m.Run(ctx)
	defer stop()
	// at start: tomorrow made, today and the 7 days before it kept, the points of the last 24 h kept
	eventually(t, "the first round", func() bool { return strings.Contains(buf.String(), `"msg":"telemetry_maintenance"`) })
	if got := partitions(t, pool); got != "20261104,20261105,20261106,20261107,20261108,20261109,20261110,20261111,20261112" {
		t.Fatalf("partitions %s", got)
	}
	var left int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM metric_samples`).Scan(&left)
	if left != 2 {
		t.Fatalf("%d points left", left)
	}
	// a lower setting is honoured at the next round, and a poke runs one at once
	days.Store(1)
	m.Poke()
	eventually(t, "the poked round", func() bool { return partitions(t, pool) == "20261110,20261111,20261112" })
	// a day later the hourly round makes the next day and drops one more
	clk.Add(24 * time.Hour)
	m.Poke()
	eventually(t, "the next day's round", func() bool { return partitions(t, pool) == "20261111,20261112,20261113" })
	// the setting unreadable: nothing is dropped, the partitions ahead are still made
	failRetention.Store(true)
	clk.Add(24 * time.Hour)
	m.Poke()
	eventually(t, "the round without the setting", func() bool { return partitions(t, pool) == "20261111,20261112,20261113,20261114" })
	stop()
	l := buf.String()
	if !strings.Contains(l, `"msg":"telemetry_maintenance"`) || !strings.Contains(l, `"partitions_dropped":2`) ||
		!strings.Contains(l, `"metrics_pruned":2`) || !strings.Contains(l, `"code":"retention"`) || strings.Contains(l, "hunter2") {
		t.Fatalf("log %s", l)
	}
}

// the loop's store calls end with it, and stop waits for them
func TestMaintenanceStops(t *testing.T) {
	pool := storetest.Pool(t)
	q := db.New(pool)
	m := telemetry.NewMaintenance(telemetry.MaintenanceDeps{
		Logs: telemetry.NewLogStore(q), Metrics: telemetry.NewMetricStore(q), Now: time.Now,
		Log: slog.New(slog.DiscardHandler), Every: time.Millisecond,
		Retention: func(context.Context) (int, error) { return 7, nil },
	})
	stop := m.Run(context.Background())
	time.Sleep(20 * time.Millisecond)
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not return")
	}
	m.Poke() // after stop: a no-op
}
