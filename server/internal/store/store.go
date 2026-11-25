// Package store owns the postgres pool and the embedded migrations.
package store

import (
	"context"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open pings so an unreachable database fails at boot.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.Tracer = queryTracer{}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	fsys, err := migrationsFS()
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, fsys)
	if err != nil {
		return fmt.Errorf("provider: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("up: %w", err)
	}
	return nil
}
