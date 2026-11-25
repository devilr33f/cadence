package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/providers/otlphttp"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

type telemetryDeps struct {
	pool       *pgxpool.Pool
	authDevice func(ctx context.Context, token string) (string, error)
	caps       *devices.Service
	global     func(ctx context.Context) (settings.Values, error)
	tee        *telemetry.Tee
	// bus hears every stored line as a log_line event while tailing says a
	// feed subscribed to logs, and the devices' vitals; nil publishes none
	bus     telemetry.Publisher
	tailing func() bool
	// otlp builds the instance the telemetryExport setting names; nil exports nothing
	otlp func(ctx context.Context, name string) (*otlphttp.Client, error)
	// traces takes the devices' spans and the export's target and the tracing
	// setting with the telemetry's; nil mounts no trace ingest
	traces *tracing.Provider
	log    *slog.Logger
}

// telemetryParts are the OTLP ingest, the loops that keep its stores, and
// what reads and forwards them.
type telemetryParts struct {
	ingest      *telemetry.Ingest
	logs        *telemetry.LogStore
	maintenance *telemetry.Maintenance
	// self stores the server's own lines, nil without a tee; its Dropped
	// counts the lines it lost
	self     *telemetry.SelfLog
	tee      *telemetry.Tee
	prom     *telemetry.Prom
	exporter *telemetry.Exporter
	// vitals keeps each device's latest vitals and publishes them on bus
	vitals *telemetry.VitalsTracker
	admin  telemetry.AdminDeps
	export exportDeps
}

func buildTelemetry(d telemetryDeps) telemetryParts {
	q := db.New(d.pool)
	logs, metrics, reader := telemetry.NewLogStore(q), telemetry.NewMetricStore(q), telemetry.NewReader(d.pool)
	p := telemetryParts{
		ingest: telemetry.NewIngest(ingestDeps(d, logs, metrics, reader)),
		logs:   logs,
		maintenance: telemetry.NewMaintenance(telemetry.MaintenanceDeps{
			Logs: logs, Metrics: metrics, Retention: logRetention(d.global), Now: time.Now, Log: d.log,
		}),
		tee:      d.tee,
		exporter: telemetry.NewExporter(telemetry.ExportDeps{Devices: reader.Devices, Log: d.log}),
		vitals:   telemetry.NewVitals(telemetry.VitalsDeps{Bus: d.bus, Now: time.Now}),
	}
	p.admin = telemetry.AdminDeps{Reader: reader, State: d.caps.State, Vitals: p.vitals.Latest, Now: time.Now, Log: d.log}
	if d.tee != nil {
		p.self = telemetry.NewSelfLog(logs, d.tee.Local())
	}
	if d.bus != nil && d.tailing != nil {
		logs.OnInsert(telemetry.PublishLogs(d.bus, d.tailing))
	}
	logs.OnInsert(p.exporter.Logs)
	metrics.OnInsert(p.exporter.Metrics)
	metrics.OnInsert(p.vitals.Observe)
	promDeps := telemetry.PromDeps{Series: reader.Latest, Ingest: p.ingest, Export: p.exporter, Now: time.Now, Log: d.log}
	if p.self != nil {
		promDeps.SelfDropped = p.self.Dropped
	}
	p.prom = telemetry.NewProm(promDeps)
	if d.otlp != nil {
		p.export = exportDeps{Global: d.global, Build: d.otlp, Target: exportTargets{telemetry: p.exporter, traces: d.traces}, Log: d.log}
		if d.traces != nil {
			p.export.Tracing = func(t settings.Tracing) { d.traces.Apply(tracingSettings(t)) }
		}
	}
	return p
}

func ingestDeps(d telemetryDeps, logs *telemetry.LogStore, metrics *telemetry.MetricStore, reader *telemetry.Reader) telemetry.IngestDeps {
	deps := telemetry.IngestDeps{
		AuthDevice: d.authDevice, Secrets: d.caps.ScrubSecrets, Logs: logs, Metrics: metrics, Now: time.Now, Log: d.log,
	}
	if d.traces != nil {
		deps.Traces, deps.Device = d.traces, reader.Device
	}
	return deps
}

// run starts the loops; poke applies a change of the telemetryExport setting
// or of an instance; stop ends them, the self log's last, so the other parts'
// final lines are stored.
func (p telemetryParts) run(ctx context.Context) (poke, stop func()) {
	stopMaintenance := p.maintenance.Run(ctx)
	stopExporter := p.exporter.Run(ctx)
	poke, stopExport := func() {}, func() {}
	if p.export.Build != nil {
		poke, stopExport = runExport(ctx, p.export)
	}
	stopLoops := func() {
		stopExport()
		stopExporter()
		stopMaintenance()
	}
	if p.self == nil {
		return poke, stopLoops
	}
	// not ended by ctx: the lines of the shutdown itself are stored too
	stopSelf := p.self.Run(context.WithoutCancel(ctx))
	p.tee.Attach(p.self)
	return poke, func() {
		stopLoops()
		p.tee.Detach()
		stopSelf()
	}
}

func logRetention(global func(ctx context.Context) (settings.Values, error)) func(ctx context.Context) (int, error) {
	return func(ctx context.Context) (int, error) {
		v, err := global(ctx)
		return v.LogRetentionDays, err
	}
}
