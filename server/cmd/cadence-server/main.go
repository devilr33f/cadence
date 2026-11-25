package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/app"
	"git.shino.boo/devilreef/cadence/server/internal/config"
	"git.shino.boo/devilreef/cadence/server/internal/store"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

var version = "dev"

func main() { os.Exit(run()) }

func run() int {
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		return runAdmin(os.Args[2:], os.Getenv, os.Stdin, os.Stdout, os.Stderr)
	}
	// the tee writes stderr as before and, once the app attached its store, keeps
	// each line with the devices' logs (M6b-D9)
	tee := telemetry.NewTee(os.Stderr)
	log := slog.New(tee)
	slog.SetDefault(log)
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config: "+err.Error())
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	// driver errors can carry the url, so only the stage is logged
	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("db_unavailable", "stage", "connect")
		return 1
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool); err != nil {
		log.Error("db_unavailable", "stage", "migrate")
		return 1
	}

	srv, cleanup, err := app.Build(ctx, cfg, pool, log, tee, version)
	if err != nil {
		var ce config.Error
		if errors.As(err, &ce) {
			log.Error("build_failed", "variable", ce.Variable, "reason", ce.Reason)
		} else {
			log.Error("build_failed")
		}
		return 1
	}
	defer cleanup()
	errc := make(chan error, 1)
	go func() { errc <- srv.Listen(cfg.Listen, listenConfig()) }()
	log.Info("server_listening", "listen", cfg.Listen, "version", version)

	select {
	case err := <-errc:
		if err != nil {
			log.Error("server_failed")
			return 1
		}
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.ShutdownWithContext(sctx); err != nil {
			log.Error("shutdown_failed")
			return 1
		}
	}
	return 0
}
