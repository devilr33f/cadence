package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/otlphttp"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

// exportEvery reconciles even without a poke, so a failed read is tried again.
const exportEvery = time.Minute

type exportDeps struct {
	Global func(context.Context) (settings.Values, error)
	Build  func(ctx context.Context, name string) (*otlphttp.Client, error)
	Target interface {
		SetTarget(c telemetry.ExportClient)
	}
	Log *slog.Logger
	// Every zero is exportEvery
	Every time.Duration
	// Tracing hears the tracing setting at each reconcile that read the
	// settings; nil hears nothing
	Tracing func(settings.Tracing)
}

// exportState is what the reconcile last applied: the instance named, the
// client it runs, or the code it was left out with.
type exportState struct {
	name, code string
	client     *otlphttp.Client
}

// runExport keeps the export's target the otlp instance the global
// telemetryExport setting names, and applies the tracing setting: at start, at
// each poke (a settings or instance change) and every minute, without a
// restart. A failed read keeps the target and the tracing in force.
func runExport(ctx context.Context, d exportDeps) (poke, stop func()) {
	if d.Every <= 0 {
		d.Every = exportEvery
	}
	ctx, cancel := context.WithCancel(ctx)
	pokes := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(d.Every)
		defer t.Stop()
		var cur exportState
		for {
			cur = reconcileExport(ctx, d, cur)
			select {
			case <-ctx.Done():
				return
			case <-pokes:
			case <-t.C:
			}
		}
	}()
	poke = func() {
		select {
		case pokes <- struct{}{}:
		default:
		}
	}
	return poke, func() {
		cancel()
		<-done
	}
}

// reconcileExport applies the setting as it is now; each change is logged
// once, by the instance's name and a code, never its URL.
func reconcileExport(ctx context.Context, d exportDeps, cur exportState) exportState {
	v, err := d.Global(ctx)
	if err != nil {
		if ctx.Err() == nil {
			d.Log.Warn("telemetry_export_reconcile_failed", "code", "settings_unreadable")
		}
		return cur
	}
	if d.Tracing != nil {
		d.Tracing(v.Tracing)
	}
	name := v.TelemetryExport
	if name == "" {
		if cur.client != nil {
			d.Target.SetTarget(nil)
			d.Log.Info("telemetry_export_off")
		}
		return exportState{}
	}
	c, err := d.Build(ctx, name)
	switch {
	case err == nil:
		if c != cur.client {
			d.Target.SetTarget(c)
			d.Log.Info("telemetry_export_on", "instance", name)
		}
		return exportState{name: name, client: c}
	case errors.Is(err, providers.ErrNotFound), errors.Is(err, providers.ErrWrongKind),
		errors.Is(err, providers.ErrUnknownModule), errors.Is(err, providers.ErrBuildFailed):
		code := instanceCode(err)
		if cur.client != nil {
			d.Target.SetTarget(nil)
		}
		if cur.name != name || cur.code != code {
			d.Log.Warn("telemetry_export_disabled", "instance", name, "code", code)
		}
		return exportState{name: name, code: code}
	}
	if ctx.Err() == nil {
		d.Log.Warn("telemetry_export_reconcile_failed", "code", "store")
	}
	return cur
}

func otlpClient(instances *providers.Instances) func(context.Context, string) (*otlphttp.Client, error) {
	return func(ctx context.Context, name string) (*otlphttp.Client, error) {
		return providers.Get[*otlphttp.Client](ctx, instances, name, providers.KindOTLP)
	}
}
