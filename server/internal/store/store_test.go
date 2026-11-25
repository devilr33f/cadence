package store_test

import (
	"context"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/store"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

func TestMigrateCreatesVector(t *testing.T) {
	ctx := context.Background()
	pool, err := store.Open(ctx, storetest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for range 2 {
		if err := store.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_extension WHERE extname='vector'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("vector extensions = %d", n)
	}
}
