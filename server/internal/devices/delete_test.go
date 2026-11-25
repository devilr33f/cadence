package devices_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// deviceColumns are every column holding a device's id (partitions aside): a
// new one fails TestDeviceColumnsAreKnown until seed fills it and the delete
// is known to clear it
var deviceColumns = []string{
	"conversation_compactions.device_id", "conversation_messages.device_id", "conversation_turns.device_id",
	"device_access.device_id", "device_capabilities.device_id", "device_capability_secrets.device_id", "device_claims.device_id", "device_logs.device_id",
	"enroll_codes.device_id", "group_members.device_id", "group_output_trims.device_id", "ha_usage.device_id", "memories.device_id", "memory_learning_runs.device_id",
	"metric_samples.device_id", "ops_runs.device_id", "play_history.device_id", "ring_sounds.device_id", "subscription_items.device_id", "subscription_told.device_id", "subscriptions.device_id",
	"task_notices.device_id", "task_told.device_id", "tasks.origin_device_id",
}

// telemetry is left to its retention, so a delete never walks the log partitions
var keptColumns = []string{"device_logs.device_id", "metric_samples.device_id"}

func TestDeviceColumnsAreKnown(t *testing.T) {
	e := newEnv(t)
	rows, err := e.pool.Query(context.Background(), `
		SELECT c.table_name || '.' || c.column_name FROM information_schema.columns c
		JOIN pg_class k ON k.relname = c.table_name AND k.relnamespace = 'public'::regnamespace
		WHERE c.table_schema = 'public' AND c.column_name LIKE '%device_id' AND NOT k.relispartition
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatal(err)
		}
		got = append(got, col)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, deviceColumns) {
		t.Fatalf("device columns %v", got)
	}
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

// seed gives the device a row in every table keyed to it, and its settings
func seed(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO enroll_codes (code_hash, device_id, model, expires_at) VALUES (sha256(convert_to($1, 'UTF8')), $1::uuid, 'sim', now())`,
		`INSERT INTO device_capabilities (device_id, capability, version, descriptor) VALUES ($1::uuid, 'audio', 1, '{}')`,
		`INSERT INTO device_capability_secrets (device_id, capability, name, sealed) VALUES ($1::uuid, 'audio', 'token', '\x00')`,
		`INSERT INTO settings (scope, data) VALUES ($1, '{"followUp":{"enabled":false}}')`,
		`INSERT INTO memories (scope, device_id, kind, text, confidence, source, status) VALUES ('device', $1::uuid, 'fact', 'f', 0.5, 'manual', 'active')`,
		`INSERT INTO conversation_messages (device_id, turn_id, seq, role, content) VALUES ($1::uuid, 't1', 0, 'user', '{}')`,
		`INSERT INTO conversation_turns (device_id, turn_id, trigger) VALUES ($1::uuid, 't1', 'wake')`,
		`WITH c AS (INSERT INTO conversation_compactions (device_id, upto_message_id, summary, model) VALUES ($1::uuid, 1, 's', 'm') RETURNING id)
		 INSERT INTO memory_learning_runs (compaction_id, device_id, applied, dropped, pending) SELECT id, $1::uuid, 0, 0, 0 FROM c`,
		`WITH k AS (INSERT INTO tasks (short_id, kind, title, origin_device_id, status) VALUES ('T' || md5($1), 'k', 't', $1::uuid, 'running') RETURNING id),
		 n AS (INSERT INTO task_notices (task_id, event_seq, device_id, text) SELECT id, 1, $1::uuid, 'n' FROM k)
		 INSERT INTO task_told (task_id, token, device_id) SELECT id, 'tok', $1::uuid FROM k`,
		`WITH s AS (INSERT INTO subscriptions (device_id, topic, module, instance, events, delivery, until, created_by) VALUES ($1::uuid, 'm:i:*', 'm', 'i', '{stopped}', 'speaker', 'unsubscribed', 'api') RETURNING id)
		 INSERT INTO subscription_items (device_id, subscription_id, module, instance, topic, event, delivery, at) SELECT $1::uuid, id, 'm', 'i', 'm:i:x', 'stopped', 'speaker', now() FROM s`,
		`INSERT INTO subscription_told (module, instance, ref_id, token, device_id) VALUES ('m', 'i', 'r', 'tok', $1::uuid)`,
		// a claim approved for it: its device_id goes null (the screen's poll answers removed)
		`INSERT INTO device_claims (id_hash, code, board, state, device_id, created_at, expires_at) VALUES (sha256(convert_to($1, 'UTF8')), 'ABCDEF', 'sim-tv', 'approved', $1::uuid, now(), now())`,
		// a multiroom group's member: the cascade drops it with the device (the groups service then rekeys the group)
		`WITH g AS (INSERT INTO groups (name, kind, key_sealed) VALUES ('g-' || $1, 'group', '\x00') RETURNING id)
		 INSERT INTO group_members (group_id, device_id, position) SELECT id, $1::uuid, 0 FROM g`,
		// its calibrated output's trim: the member's cascade drops it too
		`INSERT INTO group_output_trims (group_id, device_id, output_id, trim_us)
		 SELECT group_id, device_id, 'speaker', 0 FROM group_members WHERE device_id = $1::uuid`,
		`INSERT INTO ha_usage (device_id, entity_id, score, updated_at) VALUES ($1::uuid, 'light.a', 1, now())`,
		`INSERT INTO play_history (device_id, source, kind, dedupe_key) VALUES ($1::uuid, 'url', 'track', 'k')`,
		`INSERT INTO ring_sounds (id, device_id, kind, source_key, sha256, audio, size) VALUES (substr(md5($1), 1, 16), $1::uuid, 'speech', md5($1) || md5($1), md5($1) || md5($1), '\x00', 1)`,
		`INSERT INTO device_logs (ts, observed_ts, device_id, source, severity, body) VALUES (now(), now(), $1::uuid, 'cadenced', 9, 'line')`,
		`INSERT INTO metric_samples (device_id, name, kind, ts, value) VALUES ($1::uuid, 'm', 1, now(), 1)`,
		// an out-of-band access row and an update run: the cascade drops them with the device
		`INSERT INTO device_access (device_id, kind, addr) VALUES ($1::uuid, 'adb', '192.168.1.9:5555')`,
		`INSERT INTO ops_runs (device_id, board, target_version, transport, plan) VALUES ($1::uuid, 'sim', 'v1', 'link', '{}')`,
	} {
		exec(t, pool, q, id)
	}
}

// counts are the device's rows by column, and its settings as settings.scope
func counts(t *testing.T, pool *pgxpool.Pool, id string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, col := range append([]string{"settings.scope", "devices.id"}, deviceColumns...) {
		table, column, _ := strings.Cut(col, ".")
		var n int
		err := pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table+` WHERE `+column+`::text = $1`, id).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		out[col] = n
	}
	return out
}

func newTwin(t *testing.T, e *env) string {
	t.Helper()
	d, err := e.q.CreateDevice(context.Background(), db.CreateDeviceParams{Name: "hall", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	return d.ID.String()
}

func TestDeleteDevice(t *testing.T) {
	e := newEnv(t)
	exec(t, e.pool, `SELECT device_logs_ensure_partition((now() AT TIME ZONE 'UTC')::date)`)
	twin := newTwin(t, e)
	seed(t, e.pool, e.id)
	seed(t, e.pool, twin)
	for col, n := range counts(t, e.pool, e.id) {
		if n != 1 {
			t.Fatalf("seed: %d rows of %s", n, col)
		}
	}
	app := e.app()
	if code, body := call(t, app, http.MethodDelete, "/api/v1/devices/"+strings.ToUpper(e.id), ""); code != 204 || body != "" {
		t.Fatalf("%d %s", code, body)
	}
	for col, n := range counts(t, e.pool, e.id) {
		want := 0
		if slices.Contains(keptColumns, col) {
			want = 1
		}
		if n != want {
			t.Fatalf("%d rows of %s left, want %d", n, col, want)
		}
	}
	for col, n := range counts(t, e.pool, twin) {
		if n != 1 {
			t.Fatalf("the twin's %s: %d rows", col, n)
		}
	}
	// a household task outlives the device that started it
	var tasks int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM tasks WHERE origin_device_id IS NULL`).Scan(&tasks); err != nil || tasks != 1 {
		t.Fatalf("%d tasks without an origin, %v", tasks, err)
	}
	if got := e.hub.removedIDs(); !slices.Equal(got, []string{e.id}) {
		t.Fatalf("hub removed %v", got)
	}
	if n := e.changes.Load(); n != 1 {
		t.Fatalf("%d changes heard", n)
	}
	if line := strings.TrimSpace(e.logs.String()); line == "" || !strings.HasSuffix(line, "msg=device_deleted device_id="+e.id) ||
		strings.Count(line, "\n") != 0 {
		t.Fatalf("log %q", e.logs)
	}
	for _, c := range [][3]string{
		{http.MethodDelete, "", ""}, {http.MethodGet, "", ""}, {http.MethodPatch, "", `{"name":"x"}`},
	} {
		if code, body := call(t, app, c[0], "/api/v1/devices/"+e.id+c[1], c[2]); code != 404 || body != `{"error":"not_found"}` {
			t.Fatalf("%s after the delete: %d %s", c[0], code, body)
		}
	}
	if code, body := call(t, app, http.MethodDelete, "/api/v1/devices/nope", ""); code != 404 || body != `{"error":"not_found"}` {
		t.Fatalf("bad id: %d %s", code, body)
	}
	if got := e.hub.removedIDs(); len(got) != 1 {
		t.Fatalf("a 404 reached the hub: %v", got)
	}
}

// with ?status=pending a delete takes a device only while it is pending: one
// enrolled by then answers 409 not_pending and keeps every row and its link,
// so a stale enrol wizard's Remove never takes a speaker that just enrolled
func TestDeletePendingOnly(t *testing.T) {
	e := newEnv(t)
	exec(t, e.pool, `SELECT device_logs_ensure_partition((now() AT TIME ZONE 'UTC')::date)`)
	enrolled := newTwin(t, e)
	exec(t, e.pool, `UPDATE devices SET status = 'enrolled', token_hash = '\x01', enrolled_at = now() WHERE id = $1::uuid`, enrolled)
	seed(t, e.pool, enrolled)
	before := counts(t, e.pool, enrolled)
	app := e.app()
	if code, body := call(t, app, http.MethodDelete, "/api/v1/devices/"+enrolled+"?status=pending", ""); code != 409 || body != `{"error":"not_pending"}` {
		t.Fatalf("enrolled: %d %s", code, body)
	}
	for col, n := range counts(t, e.pool, enrolled) {
		if n != before[col] {
			t.Fatalf("%s: %d rows, were %d", col, n, before[col])
		}
	}
	if len(e.hub.removedIDs()) != 0 || e.changes.Load() != 0 || strings.Contains(e.logs.String(), "device_deleted") {
		t.Fatalf("a refused delete reached the link: removed %v, log %s", e.hub.removedIDs(), e.logs)
	}
	for _, c := range []struct {
		path string
		code int
		want string
	}{
		{"/api/v1/devices/00000000-0000-0000-0000-000000000009?status=pending", 404, `{"error":"not_found"}`},
		{"/api/v1/devices/" + e.id + "?status=enrolled", 422, `{"error":"invalid","path":"/query/status"}`},
	} {
		if code, body := call(t, app, http.MethodDelete, c.path, ""); code != c.code || body != c.want {
			t.Fatalf("%s: %d %s", c.path, code, body)
		}
	}
	if code, body := call(t, app, http.MethodDelete, "/api/v1/devices/"+e.id+"?status=pending", ""); code != 204 {
		t.Fatalf("pending: %d %s", code, body)
	}
	if n := counts(t, e.pool, e.id)["devices.id"]; n != 0 || !slices.Equal(e.hub.removedIDs(), []string{e.id}) {
		t.Fatalf("pending left %d rows, hub removed %v", n, e.hub.removedIDs())
	}
	// without the precondition an enrolled device goes as ever
	if code, body := call(t, app, http.MethodDelete, "/api/v1/devices/"+enrolled, ""); code != 204 {
		t.Fatalf("plain: %d %s", code, body)
	}
}

// an enrolment that commits while a pending-only delete waits on the device
// row wins: the delete reads the row again, finds it enrolled and takes
// nothing, all in its one statement
func TestDeletePendingOnlyLosesToARacingEnrol(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE devices SET status = 'enrolled', token_hash = '\x02', enrolled_at = now() WHERE id = $1::uuid`, e.id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.svc.DeletePending(ctx, e.id) }()
	select {
	case err := <-done:
		t.Fatalf("deleted beside an open enrolment: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, devices.ErrNotPending) {
			t.Fatalf("after the enrolment: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delete still waiting")
	}
	if n := counts(t, e.pool, e.id)["devices.id"]; n != 1 || len(e.hub.removedIDs()) != 0 {
		t.Fatalf("%d device rows, hub removed %v", n, e.hub.removedIDs())
	}
}

// a delete that fails midway leaves every row, and the link, as they were
func TestDeleteIsOneTransaction(t *testing.T) {
	e := newEnv(t)
	exec(t, e.pool, `SELECT device_logs_ensure_partition((now() AT TIME ZONE 'UTC')::date)`)
	seed(t, e.pool, e.id)
	before := counts(t, e.pool, e.id)
	// the cascade reaches ha_usage inside the delete, before the settings row goes
	exec(t, e.pool, `CREATE FUNCTION refuse_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused-xyz'; END $$`)
	exec(t, e.pool, `CREATE TRIGGER refuse_delete BEFORE DELETE ON ha_usage FOR EACH ROW EXECUTE FUNCTION refuse_delete()`)
	app := e.app()
	if code, body := call(t, app, http.MethodDelete, "/api/v1/devices/"+e.id, ""); code != 500 || body != `{"error":"internal"}` {
		t.Fatalf("%d %s", code, body)
	}
	for col, n := range counts(t, e.pool, e.id) {
		if n != before[col] {
			t.Fatalf("%s: %d rows, were %d", col, n, before[col])
		}
	}
	if len(e.hub.removedIDs()) != 0 || e.changes.Load() != 0 || !e.hub.Online(e.id) {
		t.Fatalf("a failed delete reached the link: removed %v", e.hub.removedIDs())
	}
	if logs := e.logs.String(); !strings.Contains(logs, "msg=device_delete_failed") || strings.Contains(logs, "refused-xyz") ||
		strings.Contains(logs, "device_deleted") {
		t.Fatalf("log %s", logs)
	}
	exec(t, e.pool, `DROP TRIGGER refuse_delete ON ha_usage`)
	if code, body := call(t, app, http.MethodDelete, "/api/v1/devices/"+e.id, ""); code != 204 {
		t.Fatalf("retry %d %s", code, body)
	}
}

// a settings patch racing the delete leaves no row of the gone device: the
// delete waits on the device row the patch holds (its FOR KEY SHARE), then
// removes the row the patch stored (an orphan row would keep a mode in use)
func TestDeleteLeavesNoSettingsOfARacingPatch(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// a patch as settings makes one, open: the device row held, its row stored
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `WITH d AS (SELECT id FROM devices WHERE id = $1::uuid FOR KEY SHARE)
		INSERT INTO settings (scope, data) SELECT d.id::text, '{"allowedModes":["fast","smart"]}' FROM d`, e.id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.svc.Delete(ctx, e.id) }()
	select {
	case err := <-done:
		t.Fatalf("deleted beside an open patch: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("after the patch: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delete still waiting")
	}
	if n := counts(t, e.pool, e.id); n["settings.scope"] != 0 || n["devices.id"] != 0 {
		t.Fatalf("left %v", n)
	}
}

// a delete Postgres aborts to break a deadlock or a serialization conflict
// (a learning run's insert, the conversation prune) runs once more; a second
// abort answers 500 with nothing deleted
func TestDeleteRetriesAnAbortedTransaction(t *testing.T) {
	for _, c := range []struct {
		code   string
		aborts int
		status int
		tries  int
	}{
		{"40P01", 1, 204, 2}, {"40001", 1, 204, 2}, {"40001", 2, 500, 2}, {"23505", 1, 500, 1},
	} {
		e := newEnv(t)
		exec(t, e.pool, `SELECT device_logs_ensure_partition((now() AT TIME ZONE 'UTC')::date)`)
		seed(t, e.pool, e.id)
		exec(t, e.pool, `CREATE SEQUENCE delete_tries`)
		// nextval is not rolled back with the aborted transaction, so it counts the tries
		exec(t, e.pool, fmt.Sprintf(`CREATE FUNCTION abort_delete() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN IF nextval('delete_tries') <= %d THEN RAISE EXCEPTION 'aborted-xyz' USING ERRCODE = '%s'; END IF; RETURN OLD; END $$`,
			c.aborts, c.code))
		exec(t, e.pool, `CREATE TRIGGER abort_delete BEFORE DELETE ON ha_usage FOR EACH ROW EXECUTE FUNCTION abort_delete()`)
		code, body := call(t, e.app(), http.MethodDelete, "/api/v1/devices/"+e.id, "")
		var tries int
		if err := e.pool.QueryRow(context.Background(), `SELECT last_value FROM delete_tries`).Scan(&tries); err != nil {
			t.Fatal(err)
		}
		left := counts(t, e.pool, e.id)["devices.id"]
		if code != c.status || tries != c.tries || (c.status == 204) != (left == 0) {
			t.Fatalf("%s ×%d: %d %s, %d tries, device rows %d", c.code, c.aborts, code, body, tries, left)
		}
		if logs := e.logs.String(); strings.Contains(logs, "aborted-xyz") ||
			(c.tries == 2) != strings.Contains(logs, "msg=device_delete_retried code="+c.code) {
			t.Fatalf("%s ×%d: log %s", c.code, c.aborts, logs)
		}
	}
}
