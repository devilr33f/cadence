package telemetry_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	logsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/logs/v1"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

// a deleted device's rows are stored nowhere and reach no listener; the
// server's own lines and a live device's are kept
func TestInsertSkipsDeletedDevices(t *testing.T) {
	pool := storetest.Pool(t)
	q := db.New(pool)
	ctx := context.Background()
	live, err := q.CreateDevice(ctx, db.CreateDeviceParams{Name: "live", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	gone := pgtype.UUID{Bytes: [16]byte{9}, Valid: true}
	logs, metrics := telemetry.NewLogStore(q), telemetry.NewMetricStore(q)
	var heardLogs []telemetry.LogRow
	var heardPoints []telemetry.MetricRow
	logs.OnInsert(func(rows []telemetry.LogRow) { heardLogs = append(heardLogs, rows...) })
	metrics.OnInsert(func(rows []telemetry.MetricRow) { heardPoints = append(heardPoints, rows...) })
	if err := logs.Insert(ctx, []telemetry.LogRow{
		serverLine(start, "server line"),
		{Time: start, Observed: start, DeviceID: gone, Source: "kmsg", Body: "gone line"},
		{Time: start, Observed: start, DeviceID: live.ID, Source: "kmsg", Body: "live line"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := metrics.Insert(ctx, []telemetry.MetricRow{
		{DeviceID: gone, Name: "gone", Kind: telemetry.KindGauge, Attrs: []byte(`{}`), Time: start, Value: 1},
		{DeviceID: live.ID, Name: "live", Kind: telemetry.KindGauge, Attrs: []byte(`{}`), Time: start, Value: 2},
	}); err != nil {
		t.Fatal(err)
	}
	var bodies, names []string
	rows, err := pool.Query(ctx, `SELECT body FROM device_logs ORDER BY body`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var b string
		_ = rows.Scan(&b)
		bodies = append(bodies, b)
	}
	rows.Close()
	if err := pool.QueryRow(ctx, `SELECT array_agg(name) FROM metric_samples`).Scan(&names); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0] != "live line" || bodies[1] != "server line" || len(names) != 1 || names[0] != "live" {
		t.Fatalf("stored %v %v", bodies, names)
	}
	if len(heardLogs) != 2 || heardLogs[0].Body != "server line" || heardLogs[1].Body != "live line" ||
		len(heardPoints) != 1 || heardPoints[0].Name != "live" {
		t.Fatalf("heard %+v %+v", heardLogs, heardPoints)
	}
}

// an insert that races its device's delete waits for it, then stores nothing
func TestInsertWaitsForItsDevicesDelete(t *testing.T) {
	pool := storetest.Pool(t)
	q := db.New(pool)
	ctx := context.Background()
	dev, err := q.CreateDevice(ctx, db.CreateDeviceParams{Name: "d", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	logs, metrics := telemetry.NewLogStore(q), telemetry.NewMetricStore(q)
	if err := logs.Ensure(ctx, start); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM devices WHERE id = $1`, dev.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() {
		done <- logs.Insert(ctx, []telemetry.LogRow{{Time: start, Observed: start, DeviceID: dev.ID, Source: "kmsg", Body: "late"}})
	}()
	go func() {
		done <- metrics.Insert(ctx, []telemetry.MetricRow{{DeviceID: dev.ID, Name: "late", Kind: telemetry.KindGauge, Attrs: []byte(`{}`), Time: start, Value: 1}})
	}()
	select {
	case err := <-done:
		t.Fatalf("stored beside an open delete: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("insert still waiting")
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM device_logs) + (SELECT count(*) FROM metric_samples)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d rows of a deleted device, %v", n, err)
	}
}

// a device deleted between its request's token check and the store: the
// request is answered as any other, and nothing of it is stored
func TestIngestOfADeviceDeletedMidRequest(t *testing.T) {
	r := newRig(t)
	r.set(func() {
		r.beforeScrub = func() {
			if _, err := r.pool.Exec(context.Background(), `DELETE FROM devices WHERE id = $1::uuid`, r.devA); err != nil {
				t.Error(err)
			}
		}
	})
	a := r.send(logsPath, tokenA, logsRequest(record(start, logsv1.SeverityNumber_SEVERITY_NUMBER_INFO, "kmsg", "late line")))
	if a.status != 200 || len(a.body) != 0 {
		t.Fatalf("%d %q", a.status, a.body)
	}
	if got := r.storedLogs(); len(got) != 0 {
		t.Fatalf("stored %+v", got)
	}
}
