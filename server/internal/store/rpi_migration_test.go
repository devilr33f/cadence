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

// migration 00046: the old screen and bluetooth model names become one rpi, on the same device rows
func TestRPIModelMigration(t *testing.T) {
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
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
	var first, second string
	if err := pool.QueryRow(ctx, `INSERT INTO devices (name, model, status) VALUES ('pi-a', 'rpi-bt', 'pending') RETURNING id::text`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO devices (name, model, status) VALUES ('pi-b', 'rpi-tv', 'pending') RETURNING id::text`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ApplyVersion(ctx, 46, false); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ApplyVersion(ctx, 46, true); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `SELECT id::text, model FROM devices WHERE id = $1 OR id = $2 ORDER BY id`, first, second)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]string{}
	for rows.Next() {
		var id, model string
		if err := rows.Scan(&id, &model); err != nil {
			t.Fatal(err)
		}
		seen[id] = model
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	if seen[first] != "rpi" || seen[second] != "rpi" {
		t.Fatalf("models = %v, ids %s and %s", seen, first, second)
	}
	if _, err := p.ApplyVersion(ctx, 46, false); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM devices WHERE (id = $1 OR id = $2) AND model = 'rpi-bt'`, first, second).Scan(&count); err != nil || count != 2 {
		t.Fatalf("down count = %d, err %v", count, err)
	}
}
