package app

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

// shutdownWait bounds the tracer provider's last flush into the export queue
const shutdownWait = 2 * time.Second

// buildTracing sets the tracer provider up with the default tracing setting;
// the export's reconcile applies the stored one as it reads it.
func buildTracing(version string, log *slog.Logger) *tracing.Provider {
	return tracing.Setup(tracing.Options{Version: version, Settings: tracingSettings(settings.Defaults().Tracing), Log: log})
}

func tracingSettings(t settings.Tracing) tracing.Settings {
	return tracing.Settings{Enabled: t.Enabled, SampleRatio: t.SampleRatio, RingSize: t.RingSize}
}

// untracedPath leaves the trace routes and the live feed out of the /api
// spans: watching the traces would otherwise fill them with itself.
func untracedPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/traces") || path == "/api/v1/live"
}

// exportTargets sets one collector for the telemetry export and the traces'
// alike: the telemetryExport instance takes all three signals.
type exportTargets struct {
	telemetry interface{ SetTarget(telemetry.ExportClient) }
	traces    *tracing.Provider
}

func (t exportTargets) SetTarget(c telemetry.ExportClient) {
	t.telemetry.SetTarget(c)
	if t.traces != nil {
		// a nil client stays nil across the two interfaces: the export goes off
		t.traces.SetTarget(c)
	}
}

// runTracing starts the trace export's loop and the live feed's publisher;
// stop flushes the provider and ends both.
func runTracing(ctx context.Context, p *tracing.Provider, bus events.Publisher, wanted func() bool) (stop func()) {
	stopExport := p.Run(ctx)
	stopFeed := p.PublishTo(ctx, bus, wanted)
	return func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownWait)
		defer cancel()
		_ = p.Shutdown(sctx)
		stopFeed()
		stopExport()
	}
}
