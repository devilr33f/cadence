package devices_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/devicename"
)

// the device owns its volume since M4c: a stored level would be pushed to a
// reconnecting device, whose audio.output config takes none
func TestMigrationDropsStoredVolumeLevels(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	r := report(desc("audio.output"), desc("media.player"), desc("audio.input"))
	e.sendReport(t, r)
	e.hub.setOnline(false)
	for _, c := range []string{"audio.output", "media.player"} {
		if _, err := e.svc.SetConfig(ctx, e.id, c, json.RawMessage(`{"level":30}`)); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile("../store/migrations/00009_device_owned_volume.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := strings.SplitN(strings.SplitN(string(raw), "-- +goose Down", 2)[0], "-- +goose Up", 2)[1]
	if _, err := e.pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	if c := e.capability(t, "audio.output"); string(c.Desired) != `{}` || c.DesiredGen != 1 {
		t.Fatalf("audio.output desired %s gen %d", c.Desired, c.DesiredGen)
	}
	if c := e.capability(t, "media.player"); string(c.Desired) != `{"level": 30}` {
		t.Fatalf("media.player desired %s", c.Desired)
	}
	if c := e.capability(t, "audio.input"); c.Desired != nil {
		t.Fatalf("audio.input desired %s", c.Desired)
	}

	e.hub.setOnline(true)
	e.svc.Hello(ctx, e.id, &cadencev2.Hello{CapabilitiesHash: r.Hash})
	for _, s := range e.hub.configSets() {
		if s.Capability == "audio.output" && s.Json != `{}` {
			t.Fatalf("pushed %s", s.Json)
		}
	}
	if len(e.hub.configSets()) != 2 {
		t.Fatalf("pushed %v", e.hub.configSets())
	}
}

// 00026 lets a deleted device's telemetry age out on its own; its down drops
// those orphans and puts the cascades back
func TestMigrationKeepsTelemetryOfDeletedDevices(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	raw, err := os.ReadFile("../store/migrations/00026_telemetry_outlives_devices.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, _ := strings.Cut(strings.SplitN(string(raw), "-- +goose Up", 2)[1], "-- +goose Down")
	exec(t, e.pool, `SELECT device_logs_ensure_partition((now() AT TIME ZONE 'UTC')::date)`)
	seed(t, e.pool, e.id)
	if err := e.svc.Delete(ctx, e.id); err != nil {
		t.Fatal(err)
	}
	fkeys := func() int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conname IN
			('device_logs_device_id_fkey', 'metric_samples_device_id_fkey') AND conparentid = 0`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := fkeys(); n != 0 {
		t.Fatalf("%d cascades left", n)
	}
	exec(t, e.pool, down)
	if c := counts(t, e.pool, e.id); c["device_logs.device_id"] != 0 || c["metric_samples.device_id"] != 0 || fkeys() != 2 {
		t.Fatalf("down: %v, %d cascades", c, fkeys())
	}
	exec(t, e.pool, up)
	if n := fkeys(); n != 0 {
		t.Fatalf("up again: %d cascades", n)
	}
}

// 00027 makes names unique in any case; its down lets two devices share one
// again, and its up holds once they no longer do
func TestNameIndexMigrationReverses(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	raw, err := os.ReadFile("../store/migrations/00027_device_names_unique.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, _ := strings.Cut(strings.SplitN(string(raw), "-- +goose Up", 2)[1], "-- +goose Down")
	twin := `INSERT INTO devices (name, model, status) VALUES ('KITCHEN', 'sim', 'pending') RETURNING id::text`
	var id string
	if err := e.pool.QueryRow(ctx, twin).Scan(&id); !devicename.Taken(err) {
		t.Fatalf("a name in another case: %v", err)
	}
	exec(t, e.pool, down)
	if err := e.pool.QueryRow(ctx, twin).Scan(&id); err != nil {
		t.Fatalf("down: %v", err)
	}
	exec(t, e.pool, `DELETE FROM devices WHERE id = $1::uuid`, id)
	exec(t, e.pool, up)
	if err := e.pool.QueryRow(ctx, twin).Scan(&id); !devicename.Taken(err) {
		t.Fatalf("up again: %v", err)
	}
}

// 00027 on a database that already holds names twinned in case (ruling
// M7bs-R7): the oldest of each keeps its name, the later ones take " 2", " 3"…
// cut to 64 characters and taken by no other device; the down renames nothing
func TestNameIndexMigrationRenamesTwins(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	raw, err := os.ReadFile("../store/migrations/00027_device_names_unique.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, _ := strings.Cut(strings.SplitN(string(raw), "-- +goose Up", 2)[1], "-- +goose Down")
	exec(t, e.pool, down)
	long := strings.Repeat("я", 63) + "z"
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	ids := map[string]string{}
	for i, d := range []struct{ key, name string }{
		{"first", "sim-live"}, {"upper", "SIM-LIVE"}, {"third", "sim-live"},
		// a name a twin's first suffix would take
		{"taken", "Sim-Live 2"},
		{"long", long}, {"long-twin", strings.ToUpper(long)},
	} {
		var id string
		if err := e.pool.QueryRow(ctx, `INSERT INTO devices (name, model, status, created_at) VALUES ($1, 'sim', 'pending', $2)
			RETURNING id::text`, d.name, at.Add(time.Duration(i)*time.Minute)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[d.key] = id
	}
	// one created at the same instant as the first, with a larger id, is the later one
	var same string
	if err := e.pool.QueryRow(ctx, `INSERT INTO devices (id, name, model, status, created_at)
		VALUES ('ffffffff-ffff-ffff-ffff-ffffffffffff', 'Sim-live', 'sim', 'pending', $1) RETURNING id::text`, at).Scan(&same); err != nil {
		t.Fatal(err)
	}
	exec(t, e.pool, up)
	name := func(id string) string {
		t.Helper()
		var n string
		if err := e.pool.QueryRow(ctx, `SELECT name FROM devices WHERE id = $1::uuid`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for id, want := range map[string]string{
		ids["first"]: "sim-live", same: "Sim-live 3", ids["upper"]: "SIM-LIVE 4", ids["third"]: "sim-live 5",
		ids["taken"]: "Sim-Live 2", ids["long"]: long, ids["long-twin"]: strings.ToUpper(long)[:len(strings.ToUpper(long))-3] + " 2",
		e.id: "kitchen",
	} {
		if got := name(id); got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
	if got := name(ids["long-twin"]); len([]rune(got)) != 64 {
		t.Errorf("long twin %d characters", len([]rune(got)))
	}
	exec(t, e.pool, down)
	if got := name(ids["upper"]); got != "SIM-LIVE 4" {
		t.Fatalf("down renamed %q", got)
	}
}
