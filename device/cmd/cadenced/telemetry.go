package main

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/config"
	"git.shino.boo/devilreef/cadence/device/internal/link"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/metrics"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/sources"
	"git.shino.boo/devilreef/cadence/device/internal/tracing"
)

// logging is cadenced's two loggers on one writer (stderr, which the boot
// wrapper keeps as log/cadenced.log): log, through the tee, which enters the
// pipeline once one is attached, and local, the local log alone, for the
// pipeline's own trouble and audiod's lines.
type logging struct {
	log   *slog.Logger
	tee   *sources.Tee
	local *slog.Logger
}

func newLogging(w io.Writer) logging {
	out := &lockedWriter{w: w}
	tee := sources.NewTee(out)
	return logging{log: slog.New(tee), tee: tee, local: slog.New(slog.NewJSONHandler(out, nil))}
}

// lockedWriter keeps the two loggers' lines whole on their one writer
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// startLogs starts the log pipeline of an enrolled device, shipping to its
// server, and returns it with the scrubber the capability secrets go to,
// which already knows the device token, and the client it ships through,
// which the metrics share; stop ends the exporter. An offline device, or one
// whose ca file is unusable, gets none: core then refuses the link and logs
// why. local is the local log alone: a pipeline that logged into itself would
// loop.
func startLogs(ctx context.Context, cfg config.Config, local *slog.Logger) (
	p *logs.Pipeline, secrets *logs.Scrubber, client *otlp.Client, stop func(),
) {
	none := func() {}
	if cfg.ServerURL == "" {
		return nil, nil, nil, none
	}
	roots, err := link.RootCAs(cfg.CAFile)
	if err != nil {
		return nil, nil, nil, none
	}
	client, err = otlp.NewClient(cfg.ServerURL, cfg.DeviceToken, roots)
	if err != nil {
		return nil, nil, nil, none
	}
	secrets = &logs.Scrubber{}
	secrets.Set("device_token", cfg.DeviceToken)
	p = logs.New(client, secrets, local)
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() { p.Run(ctx) })
	return p, secrets, client, func() {
		cancel()
		wg.Wait()
	}
}

// metricsEnv reports the system metrics through the client the logs ship
// through, with cadenced's counts, the pipeline's among them; the counts
// start now.
func metricsEnv(client *otlp.Client, p *logs.Pipeline) telemetry.MetricsEnv {
	app := metrics.NewApp(time.Now())
	app.ObserveLogs(p.Stats)
	return telemetry.MetricsEnv{Client: client, App: app}
}

// startTraces records the device's spans and ships them through the client
// the logs ship through (none without one, and none in a cadence_notrace
// build); stop ends the shipping and the recording.
func startTraces(ctx context.Context, client *otlp.Client) (stop func()) {
	if client == nil {
		return func() {}
	}
	p := tracing.Setup(client, version)
	stopRun := p.Run(ctx)
	return func() {
		stopRun()
		sctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = p.Shutdown(sctx)
	}
}
