package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	// alarms ring by the owner's IANA zone, and a device may have no zoneinfo of its own
	_ "time/tzdata"

	"git.shino.boo/devilreef/cadence/device/boards"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/config"
	"git.shino.boo/devilreef/cadence/device/internal/core"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
)

var version = "dev"

const (
	exitUsage   = 2
	exitRefused = 3

	waitConfigEvery = time.Second
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "enroll" {
		return runEnroll(args[1:], stderr)
	}
	if len(args) > 0 && args[0] == "claim" {
		return runClaim(args[1:], stderr)
	}
	fs := flag.NewFlagSet("cadenced", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "", "path to the config file (required)")
	wait := fs.Bool("wait-config", false, "wait for the config file to appear, as before enroll")
	claimServer := fs.String("claim-server", "", "with --wait-config: claim device-first on this server while the config is missing (a screen board)")
	claimBoard := fs.String("claim-board", "", "the screen board a claim is for")
	claimData := fs.String("claim-data-dir", "", "the data directory a claim's config names")
	claimCA := fs.String("claim-ca-file", "", "extra CA bundle for the claim's server")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *path == "" {
		_, _ = fmt.Fprintln(stderr, "cadenced: --config is required")
		return exitUsage
	}
	if *wait && *claimServer != "" {
		if _, err := os.Stat(*path); errors.Is(err, os.ErrNotExist) {
			cargs := []string{"--server", *claimServer, "--board", *claimBoard, "--config", *path, "--data-dir", *claimData}
			if *claimCA != "" {
				cargs = append(cargs, "--ca-file", *claimCA)
			}
			if c := runClaim(cargs, stderr); c != 0 {
				return c
			}
		}
	}
	lg := newLogging(stderr)
	log := lg.log
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if *wait && !waitConfig(ctx, *path, log) {
		return 0
	}
	cfg, unknown, err := config.Load(*path)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cadenced:", err)
		return exitUsage
	}
	if unknown > 0 {
		log.Warn("config_unknown_keys", "count", unknown)
	}

	var opts []core.SyncOption
	var env telemetry.LogsEnv
	var menv telemetry.MetricsEnv
	p, secrets, client, stopLogs := startLogs(ctx, cfg, lg.local)
	defer stopLogs()
	if p != nil {
		lg.tee.Attach(p)
		env = telemetry.LogsEnv{Sink: p, Local: lg.local}
		menv = metricsEnv(client, p)
		opts = append(opts, core.WithSecrets(secrets), core.WithCounts(menv.App))
	}
	stopTraces := startTraces(ctx, client)
	defer stopTraces()
	err = core.Run(ctx, cfg, boards.All(cfg, log, env, menv), log, version, opts...)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, board.ErrUnknownBoard), errors.Is(err, board.ErrFingerprint):
		return exitRefused
	default:
		_, _ = fmt.Fprintln(stderr, "cadenced:", err)
		return 1
	}
}

// waitConfig polls until the file exists and reports false when ctx ends first.
// Any other stat error is left for Load to report.
func waitConfig(ctx context.Context, path string, log *slog.Logger) bool {
	logged := false
	for {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			return true
		}
		if !logged {
			log.Info("config_waiting")
			logged = true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(waitConfigEvery):
		}
	}
}
