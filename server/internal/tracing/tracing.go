package tracing

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// scopePrefix namespaces Cadence's instrumentation scopes; the store keeps
// only spans under it.
const scopePrefix = "cadence/"

// the services a span is recorded by, as the telemetry export names them
const (
	serviceServer = "cadence-server"
	serviceDevice = "cadence-device"
)

// sdk span limits: the guard cuts values again, these keep the sdk from
// holding more than the guard would keep
const (
	limitAttrValueLen = maxValue
	limitAttrCount    = 64
	limitEventCount   = 64
	limitAttrPerEvent = 16
	limitLinkCount    = 8
	limitAttrPerLink  = 4
)

// hot state, read on every instrumented call
var (
	enabled atomic.Bool
	sampler = newDynamicSampler(1)
	current atomic.Pointer[Store]
)

// noopSpan is what Start hands back while tracing is off: callers may still
// end it, set attributes on it and read its invalid context.
var noopSpan = trace.SpanFromContext(context.Background())

// Settings are the tracing setting's values, applied live.
type Settings struct {
	Enabled     bool
	SampleRatio float64
	RingSize    int
}

// Options configure Setup.
type Options struct {
	// Version is the server's, on the resource.
	Version string
	// Settings apply at once; the app applies the stored ones as it reads them.
	Settings Settings
	// Budget bounds the retained spans' estimated memory; <= 0 is 32 MiB.
	Budget int64
	Log    *slog.Logger
}

// Provider is the tracing plane: the sdk provider, its store and its export.
type Provider struct {
	store  *Store
	tp     *sdktrace.TracerProvider
	export *Exporter
	log    *slog.Logger
}

// Setup builds the store, the export and the sdk TracerProvider that feeds
// both, and installs the provider, the W3C propagator (TraceContext and
// Baggage) and an error handler that logs codes as otel's globals. Nothing
// here needs a collector: without an export target spans live in the store
// alone.
func Setup(o Options) *Provider {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	store := NewStore(o.Settings.RingSize, o.Budget)
	current.Store(store)
	export := NewExporter(o.Log)
	res := resource.NewSchemaless(
		attribute.String("service.name", serviceServer),
		attribute.String("service.version", o.Version),
	)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sampler)),
		sdktrace.WithRawSpanLimits(sdktrace.SpanLimits{
			AttributeValueLengthLimit: limitAttrValueLen, AttributeCountLimit: limitAttrCount,
			EventCountLimit: limitEventCount, AttributePerEventCountLimit: limitAttrPerEvent,
			LinkCountLimit: limitLinkCount, AttributePerLinkCountLimit: limitAttrPerLink,
		}),
		sdktrace.WithSpanProcessor(&storeProcessor{store: store}),
		// batched: a request is never held up by the collector; a full queue drops
		sdktrace.WithBatcher(spanExporter{export: export, version: o.Version},
			sdktrace.WithMaxQueueSize(4096), sdktrace.WithBatchTimeout(2*time.Second)),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	log := o.Log
	// an sdk error's text can carry the collector's url: its code alone
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) { log.Warn("otel_error", "code", "sdk") }))
	p := &Provider{store: store, tp: tp, export: export, log: o.Log}
	p.Apply(o.Settings)
	return p
}

// Store is the provider's store.
func (p *Provider) Store() *Store { return p.store }

// Apply makes s the settings in force: on or off, the sampled share of new
// traces and the ring's size, without a restart.
func (p *Provider) Apply(s Settings) {
	enabled.Store(s.Enabled)
	sampler.setRatio(s.SampleRatio)
	p.store.SetRingSize(s.RingSize)
	p.export.enabled.Store(s.Enabled)
}

// SetTarget makes c the collector traces go to; nil turns the export off.
func (p *Provider) SetTarget(c ExportClient) { p.export.SetTarget(c) }

// Run starts the export's loop; stop ends it.
func (p *Provider) Run(ctx context.Context) (stop func()) { return p.export.Run(ctx) }

// Stats are the store's and the export's counts.
func (p *Provider) Stats() TraceStats {
	s := p.store.stats()
	s.Enabled, s.SampleRatio = enabled.Load(), sampler.ratio()
	s.Export = p.export.Stats()
	return s
}

// Shutdown ends the provider, handing what the batcher holds to the export
// queue; spans started after it are not recorded.
func (p *Provider) Shutdown(ctx context.Context) error {
	err := p.tp.Shutdown(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		p.log.Warn("tracing_shutdown_failed", "code", "timeout")
	}
	return err
}

// Enabled reports whether spans are recorded.
func Enabled() bool { return enabled.Load() }

// Tracer is the tracer of one subsystem, its scope "cadence/"+scope: the
// store keeps only those, so everything recorded comes through here.
func Tracer(scope string) trace.Tracer { return otel.Tracer(scopePrefix + scope) }

// Start begins a span in scope, or returns ctx and a non-recording span while
// tracing is off. End closes it.
func Start(ctx context.Context, scope, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	if !enabled.Load() {
		return ctx, noopSpan
	}
	return Tracer(scope).Start(ctx, name, trace.WithAttributes(attrs...))
}

// StartKind is Start with a span kind (server, client…).
func StartKind(ctx context.Context, scope, name string, kind trace.SpanKind, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	if !enabled.Load() {
		return ctx, noopSpan
	}
	return Tracer(scope).Start(ctx, name, trace.WithSpanKind(kind), trace.WithAttributes(attrs...))
}

// StartAt is Start backdated to start: work that began before Cadence's
// code saw it, such as the wake word audiod detected before cadenced heard
// of it. Stage timings known later go on the span as events
// (span.AddEvent(name, trace.WithTimestamp(at))).
func StartAt(ctx context.Context, scope, name string, start time.Time, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	if !enabled.Load() {
		return ctx, noopSpan
	}
	return Tracer(scope).Start(ctx, name, trace.WithTimestamp(start), trace.WithAttributes(attrs...))
}

// Background starts a new root span for work that outlives the request that
// caused it: the context keeps ctx's values without its cancellation, and
// the new trace links back to the span that spawned it.
func Background(ctx context.Context, scope, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	detached := context.WithoutCancel(ctx)
	if !enabled.Load() {
		return detached, noopSpan
	}
	opts := []trace.SpanStartOption{trace.WithNewRoot(), trace.WithAttributes(attrs...)}
	if link := trace.LinkFromContext(ctx); link.SpanContext.IsValid() {
		opts = append(opts, trace.WithLinks(link))
	}
	return Tracer(scope).Start(detached, name, opts...)
}

// Root is Background that keeps ctx's cancellation: a job that is a trace
// of its own (an event turn, a compaction) but still ends with its owner's
// loop or the service that runs it.
func Root(ctx context.Context, scope, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	_, span := Background(ctx, scope, name, attrs...)
	return trace.ContextWithSpan(ctx, span), span
}

// End finishes span. A failure is recorded by FailErr: its class (Classify),
// never its text, which may hold a URL or a key. A nil span is fine.
func End(span trace.Span, err error) {
	if span == nil {
		return
	}
	if err != nil {
		FailErr(span, err)
	}
	span.End()
}

// Fail marks span failed with code, a token such as timeout or status_503.
func Fail(span trace.Span, code string) {
	code = statusCode(code)
	span.SetAttributes(attribute.String(AttrErrorType, code))
	span.SetStatus(codes.Error, code)
}

// ErrorCode is err's code: Classify's type (its own Code() or ErrorCode()
// when it has one, else its class: timeout, canceled, conn_refused…, else
// error).
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	return statusCode(Classify(err).Type)
}
