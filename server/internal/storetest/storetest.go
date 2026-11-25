// Package storetest starts a throwaway postgres for tests.
package storetest

import (
	"context"
	"fmt"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"git.shino.boo/devilreef/cadence/server/internal/store"
)

const pgImage = "pgvector/pgvector:pg17@sha256:7d0a41464797aeb9862dfdc0bbf1526b82cfd324cdd8036df19e3545f4a9ccb8"

// URL starts a container and returns its connection string, unmigrated.
func URL(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, pgImage,
		postgres.WithDatabase("cadence"), postgres.WithUsername("cadence"), postgres.WithPassword("pw"),
		postgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	url, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	return url
}

var (
	shared  string // admin url of the package container, when Main started one
	dbCount atomic.Int64
)

func withDB(raw, name string) string {
	u, _ := url.Parse(raw)
	u.Path = "/" + name
	return u.String()
}

// Main runs a package's tests on one container with a migrated template
// database, so Pool is a CREATE DATABASE away; use it as
// func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }.
//
// A test that panics, or a run that is interrupted, ends the binary before
// its cleanup, and make test runs without ryuk, so that package's container
// stays behind: `docker ps --filter label=org.testcontainers=true` lists it
// among every live run's, other worktrees' too, so remove only one whose own
// go test has ended. A URL or Pool container outside Main stays the same way.
func Main(m *testing.M) int {
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, pgImage,
		postgres.WithDatabase("cadence_tpl"), postgres.WithUsername("cadence"), postgres.WithPassword("pw"),
		postgres.BasicWaitStrategies(),
	)
	if ctr != nil {
		defer func() { _ = testcontainers.TerminateContainer(ctr) }()
	}
	if err == nil {
		var raw string
		if raw, err = ctr.ConnectionString(ctx, "sslmode=disable"); err == nil {
			var pool *pgxpool.Pool
			if pool, err = store.Open(ctx, raw); err == nil {
				err = store.Migrate(ctx, pool)
				pool.Close()
				shared = withDB(raw, "postgres")
			}
		}
	}
	if err != nil {
		fmt.Println("storetest:", err)
		return 1
	}
	return m.Run()
}

func sharedPool(t *testing.T) *pgxpool.Pool {
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, shared)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("t%d", dbCount.Add(1))
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name+" TEMPLATE cadence_tpl"); err != nil {
		t.Fatal(err)
	}
	pool, err := store.Open(ctx, withDB(shared, name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if a, err := pgxpool.New(ctx, shared); err == nil {
			_, _ = a.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
			a.Close()
		}
	})
	return pool
}

// Pool returns a migrated pool: a copy of the template database under Main,
// else a fresh container.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if shared != "" {
		return sharedPool(t)
	}
	ctx := context.Background()
	pool, err := store.Open(ctx, URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := store.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}
