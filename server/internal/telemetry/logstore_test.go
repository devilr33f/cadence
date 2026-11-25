package telemetry_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

func partitions(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		WHERE i.inhparent = 'device_logs'::regclass ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, strings.TrimPrefix(p, "device_logs_"))
	}
	return strings.Join(out, ",")
}

func serverLine(at time.Time, body string) telemetry.LogRow {
	return telemetry.LogRow{Time: at, Observed: at, Source: "server", Severity: 9, Body: body}
}

func countLogs(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM device_logs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPartitionsMadeOnDemandAndDropped(t *testing.T) {
	pool := storetest.Pool(t)
	s := telemetry.NewLogStore(db.New(pool))
	ctx := context.Background()
	// a day's partition holds [00:00, 24:00) UTC, whatever zone the time is in
	bishkek := time.FixedZone("+06", 6*3600)
	rows := []telemetry.LogRow{
		serverLine(start.AddDate(0, 0, -3), "three days ago"),
		serverLine(time.Date(2026, 11, 11, 5, 59, 59, 0, bishkek), "late on the 10th"),
		serverLine(time.Date(2026, 11, 11, 6, 0, 0, 0, bishkek), "midnight of the 11th"),
		serverLine(start.AddDate(0, 0, 1), "tomorrow"),
	}
	if err := s.Insert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if got := partitions(t, pool); got != "20261108,20261110,20261111,20261112" {
		t.Fatalf("partitions %s", got)
	}
	var day string
	if err := pool.QueryRow(ctx, `SELECT tableoid::regclass::text FROM device_logs WHERE body = 'midnight of the 11th'`).Scan(&day); err != nil || day != "device_logs_20261111" {
		t.Fatalf("midnight row in %s %v", day, err)
	}
	if err := s.Ensure(ctx, start, start.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	n, err := s.DropBefore(ctx, start.AddDate(0, 0, -1))
	if err != nil || n != 1 {
		t.Fatalf("dropped %d %v", n, err)
	}
	if got := partitions(t, pool); got != "20261110,20261111,20261112" || countLogs(t, pool) != 3 {
		t.Fatalf("after the drop: %s, %d rows", got, countLogs(t, pool))
	}
	// the dropped day is made again for a late line
	if err := s.Insert(ctx, rows[:1]); err != nil {
		t.Fatal(err)
	}
	if got := partitions(t, pool); got != "20261108,20261110,20261111,20261112" {
		t.Fatalf("partitions %s", got)
	}
	if n, err := s.DropBefore(ctx, start.AddDate(0, 0, 2)); err != nil || n != 4 || countLogs(t, pool) != 0 {
		t.Fatalf("dropped %d %v", n, err)
	}
}

// a partition dropped behind the store's back is made again: the insert never fails for it
func TestPartitionDroppedElsewhereMadeAgain(t *testing.T) {
	pool := storetest.Pool(t)
	s := telemetry.NewLogStore(db.New(pool))
	ctx := context.Background()
	if err := s.Insert(ctx, []telemetry.LogRow{serverLine(start, "first")}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DROP TABLE device_logs_20261111`); err != nil {
		t.Fatal(err)
	}
	if err := s.Insert(ctx, []telemetry.LogRow{serverLine(start, "second")}); err != nil {
		t.Fatal(err)
	}
	if countLogs(t, pool) != 1 {
		t.Fatalf("%d rows", countLogs(t, pool))
	}
}

// writers that each make the same days' partitions at once all succeed: two
// creates of one name collide without the function's lock
func TestConcurrentInsertsMakeEachPartitionOnce(t *testing.T) {
	pool := storetest.Pool(t)
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := range 64 {
		s := telemetry.NewLogStore(db.New(pool))
		wg.Go(func() {
			errs <- s.Insert(context.Background(), []telemetry.LogRow{serverLine(start.AddDate(0, 0, i%8), fmt.Sprint(i))})
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := "20261111,20261112,20261113,20261114,20261115,20261116,20261117,20261118"
	if got := partitions(t, pool); got != want || countLogs(t, pool) != 64 {
		t.Fatalf("%s, %d rows", got, countLogs(t, pool))
	}
}

// a store failure carries no row content, which postgres quotes in its detail lines
func TestStoreErrorCarriesNoRow(t *testing.T) {
	pool := storetest.Pool(t)
	s := telemetry.NewLogStore(db.New(pool))
	row := serverLine(start, "row text that must stay out")
	row.Severity = 9
	if _, err := pool.Exec(context.Background(), `ALTER TABLE device_logs ADD CONSTRAINT no_rows CHECK (severity < 0)`); err != nil {
		t.Fatal(err)
	}
	err := s.Insert(context.Background(), []telemetry.LogRow{row})
	if err == nil || strings.Contains(err.Error(), "row text") || strings.Contains(err.Error(), "no_rows") {
		t.Fatalf("err %v", err)
	}
}

// a partition call that cannot have its locks gives up in 2 s, rather than
// hold every insert behind it
func TestPartitionCallGivesUpOnALock(t *testing.T) {
	pool := storetest.Pool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('device_logs_partitions'))`); err != nil {
		t.Fatal(err)
	}
	s := telemetry.NewLogStore(db.New(pool))
	began := time.Now()
	if err := s.Ensure(ctx, start); err == nil {
		t.Fatal("made a partition without the lock")
	}
	if _, err := s.DropBefore(ctx, start); err == nil {
		t.Fatal("dropped without the lock")
	}
	if took := time.Since(began); took < 3*time.Second || took > 8*time.Second {
		t.Fatalf("two calls gave up after %v", took)
	}
	_ = tx.Rollback(ctx)
	if err := s.Ensure(ctx, start); err != nil {
		t.Fatal(err)
	}
}
