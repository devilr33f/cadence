package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"git.shino.boo/devilreef/cadence/server/internal/store"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

// migration 00039, multiroom groups: a device is in one group at most, the
// bounds hold, a deleted device leaves its group and no longer leads it, and
// the tables step down and up.
func TestGroupsMigration(t *testing.T) {
	ctx := context.Background()
	pool, err := store.Open(ctx, storetest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	gdb := stdlib.OpenDBFromPool(pool)
	defer func() { _ = gdb.Close() }()
	p, err := goose.NewProvider(goose.DialectPostgres, gdb, os.DirFS("migrations"))
	if err != nil {
		t.Fatal(err)
	}
	// up to 00039 alone: later tables (00048's per-output trims) hang off group_members, so stepping 00039 down
	// with them in place would fail on their foreign keys, not on 00039's own down
	if _, err := p.UpTo(ctx, 39); err != nil {
		t.Fatal(err)
	}
	var a, b, g, h string
	for _, d := range []*string{&a, &b} {
		if err := pool.QueryRow(ctx, `INSERT INTO devices (name, model, status) VALUES (gen_random_uuid()::text, 'sim', 'enrolled') RETURNING id`).Scan(d); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, `INSERT INTO groups (name, kind, preferred_leader, key_sealed) VALUES ('Home', 'group', $1, '\x00') RETURNING id`, a).Scan(&g); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO groups (name, kind, key_sealed) VALUES ('Pair', 'stereo_pair', '\x00') RETURNING id`).Scan(&h); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`INSERT INTO groups (name, kind, key_sealed) VALUES ('home', 'group', '\x00')`,
		`INSERT INTO groups (name, kind, key_sealed) VALUES ('x', 'party', '\x00')`,
		`INSERT INTO groups (name, kind, latency_ms, key_sealed) VALUES ('y', 'group', 199, '\x00')`,
		`INSERT INTO groups (name, kind, wait_ms, key_sealed) VALUES ('z', 'group', 10001, '\x00')`,
	} {
		if _, err := pool.Exec(ctx, bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO group_members (group_id, device_id, position) VALUES ($1, $2, 0), ($1, $3, 1)`, g, a, b); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO group_members (group_id, device_id, position) VALUES ($1, $2, 0)`, h, a); err == nil {
		t.Fatal("a device joined a second group")
	}
	if _, err := pool.Exec(ctx, `UPDATE group_members SET trim_ms = 501 WHERE device_id = $1`, b); err == nil {
		t.Fatal("trim 501 accepted")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM devices WHERE id = $1`, a); err != nil {
		t.Fatal(err)
	}
	var members int
	var leader *string
	if err := pool.QueryRow(ctx, `SELECT count(*), (SELECT preferred_leader::text FROM groups WHERE id = $1) FROM group_members WHERE group_id = $1`, g).Scan(&members, &leader); err != nil {
		t.Fatal(err)
	}
	if members != 1 || leader != nil {
		t.Fatalf("after the delete %d members, leader %v", members, leader)
	}
	if _, err := p.ApplyVersion(ctx, 39, false); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT 1 FROM groups`); err == nil {
		t.Fatal("groups still there")
	}
	if _, err := p.ApplyVersion(ctx, 39, true); err != nil {
		t.Fatal(err)
	}
}

// migration 00041, group auto-tune: every group, made before or after, tunes itself by default, and the flags step
// down and up
func TestGroupAutoTuneMigration(t *testing.T) {
	ctx := context.Background()
	pool, err := store.Open(ctx, storetest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	gdb := stdlib.OpenDBFromPool(pool)
	defer func() { _ = gdb.Close() }()
	p, err := goose.NewProvider(goose.DialectPostgres, gdb, os.DirFS("migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 40); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO groups (name, kind, key_sealed) VALUES ('Old', 'group', '\x00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 41); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO groups (name, kind, key_sealed, auto_wait) VALUES ('New', 'group', '\x00', false)`); err != nil {
		t.Fatal(err)
	}
	var on, off int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE auto_latency AND auto_bitrate AND auto_wait),
		count(*) FILTER (WHERE auto_latency AND auto_bitrate AND NOT auto_wait) FROM groups`).Scan(&on, &off); err != nil {
		t.Fatal(err)
	}
	if on != 1 || off != 1 {
		t.Fatalf("auto on %d, wait off %d", on, off)
	}
	if _, err := pool.Exec(ctx, `UPDATE groups SET auto_latency = NULL`); err == nil {
		t.Fatal("a null flag accepted")
	}
	if _, err := p.ApplyVersion(ctx, 41, false); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT auto_latency FROM groups`); err == nil {
		t.Fatal("auto_latency still there")
	}
	if _, err := p.ApplyVersion(ctx, 41, true); err != nil {
		t.Fatal(err)
	}
}

// migration 00049, ultrasonic live tracking: opt-in is off for old and new groups, each output keeps its last
// capability result, and both additions step down and up.
func TestGroupTrackingMigration(t *testing.T) {
	ctx := context.Background()
	pool, err := store.Open(ctx, storetest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	gdb := stdlib.OpenDBFromPool(pool)
	defer func() { _ = gdb.Close() }()
	p, err := goose.NewProvider(goose.DialectPostgres, gdb, os.DirFS("migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 48); err != nil {
		t.Fatal(err)
	}
	var d, g string
	if err := pool.QueryRow(ctx, `INSERT INTO devices (name, model, status) VALUES ('One', 'sim', 'enrolled') RETURNING id`).Scan(&d); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO groups (name, kind, key_sealed) VALUES ('Home', 'group', '\x00') RETURNING id`).Scan(&g); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO group_members (group_id, device_id, position) VALUES ($1, $2, 0)`, g, d); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO group_output_trims (group_id, device_id, output_id, trim_us) VALUES ($1, $2, 'speaker', 0)`, g, d); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 49); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	var ultrasonic string
	if err := pool.QueryRow(ctx, `SELECT tracking_enabled FROM groups WHERE id = $1`, g).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT ultrasonic FROM group_output_trims WHERE group_id = $1`, g).Scan(&ultrasonic); err != nil {
		t.Fatal(err)
	}
	if enabled || ultrasonic != "" {
		t.Fatalf("defaults enabled=%v ultrasonic=%q", enabled, ultrasonic)
	}
	if _, err := pool.Exec(ctx, `UPDATE groups SET tracking_enabled = true WHERE id = $1`, g); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE group_output_trims SET ultrasonic = 'ok' WHERE group_id = $1`, g); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE group_output_trims SET ultrasonic = 'unknown' WHERE group_id = $1`, g); err == nil {
		t.Fatal("an unknown ultrasonic result was accepted")
	}
	if _, err := p.ApplyVersion(ctx, 49, false); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT tracking_enabled FROM groups`); err == nil {
		t.Fatal("tracking_enabled still there")
	}
	if _, err := p.ApplyVersion(ctx, 49, true); err != nil {
		t.Fatal(err)
	}
}
