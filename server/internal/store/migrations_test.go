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

// migration 00034, a mode's max_tokens: seeded rows start at the default, the
// check refuses what the api would not send, and the column steps down and up.
// The files come from the directory beside the test, which is what the package
// embeds when it builds.
func TestModeMaxTokensMigration(t *testing.T) {
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
	var tokens int
	if err := pool.QueryRow(ctx, `SELECT max_tokens FROM agent_modes WHERE name='fast'`).Scan(&tokens); err != nil || tokens != 0 {
		t.Fatalf("seeded max_tokens %d err %v, want the default 0", tokens, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_modes SET max_tokens=63 WHERE name='fast'`); err == nil {
		t.Fatal("max_tokens 63 accepted")
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_modes SET max_tokens=32768 WHERE name='fast'`); err != nil {
		t.Fatal(err)
	}
	// down drops the column, up adds it back at the default
	if _, err := p.ApplyVersion(ctx, 34, false); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT max_tokens FROM agent_modes WHERE name='fast'`).Scan(&tokens); err == nil {
		t.Fatalf("column still there: %d", tokens)
	}
	if _, err := p.ApplyVersion(ctx, 34, true); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT max_tokens FROM agent_modes WHERE name='fast'`).Scan(&tokens); err != nil || tokens != 0 {
		t.Fatalf("max_tokens after the round trip %d err %v, want the default 0", tokens, err)
	}
}
