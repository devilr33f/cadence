// Package tracing is cadenced's side of distributed tracing: one voice turn is
// one trace from the wake word audiod heard to the reply's last sample, joined
// on the server by the context its frames carry (pkg/tracectx).
//
// The spans are made through the OpenTelemetry API here, in both builds; the
// SDK that records them and the exporter that posts them to the server's
// /v2/otlp/v1/traces (with the device token, through the logs' client) are
// built only without the cadence_notrace tag. With the tag, Setup returns a
// provider that records nothing, every span is the API's non-recording one, no
// frame carries a context, and the SDK is not linked. At run time the logs
// capability's traces switch (SetEnabled) turns recording off and on.
//
// Privacy (rule 8): span names, attributes and events carry ids, codes,
// numbers and timings only, never a transcript, a title, a URL, a token or an
// error's text. The server's guard drops what slips through anyway.
package tracing

import (
	"context"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/tracectx"
)

// ScopeName is the instrumentation scope of every device span; the server
// shows it as "device".
const ScopeName = "cadence/device"

// Attribute keys, as the server's tracing package names them.
const (
	AttrSessionID = "cadence.session_id"
	AttrStreamID  = "cadence.stream_id"
	AttrMethod    = "cadence.method"
	AttrErrorType = "error.type"
)

type tracerBox struct{ t trace.Tracer }

var (
	// the recording tracer once Setup ran (never with cadence_notrace)
	current atomic.Pointer[tracerBox]
	// the runtime switch: on until a config turns it off
	enabled atomic.Bool
	off     = noop.NewTracerProvider().Tracer(ScopeName)
)

func init() { enabled.Store(true) }

// SetEnabled turns recording on or off at run time (the logs capability's
// traces); spans already open finish as they began.
func SetEnabled(on bool) { enabled.Store(on) }

// Enabled reports whether new spans record.
func Enabled() bool { return enabled.Load() && current.Load() != nil }

func tracer() trace.Tracer {
	if b := current.Load(); b != nil && enabled.Load() {
		return b.t
	}
	return off
}

// Use records through tp from now on, whatever the build and the switch (a
// test's sdk provider with a span recorder); restore puts back what was.
func Use(tp trace.TracerProvider) (restore func()) {
	prev := current.Swap(&tracerBox{t: tp.Tracer(ScopeName)})
	wasOn := enabled.Swap(true)
	return func() {
		current.Store(prev)
		enabled.Store(wasOn)
	}
}

// Start begins a span under ctx's; while tracing is off it is the API's
// non-recording span, which carries ctx's context on and records nothing.
func Start(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return tracer().Start(ctx, name, trace.WithAttributes(attrs...))
}

// StartKind is Start with a span kind (client for a call the device makes).
func StartKind(ctx context.Context, name string, kind trace.SpanKind, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return tracer().Start(ctx, name, trace.WithSpanKind(kind), trace.WithAttributes(attrs...))
}

// StartAt is Start backdated to at: work that began before cadenced heard of
// it (the wake word audiod detected).
func StartAt(ctx context.Context, name string, at time.Time, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return tracer().Start(ctx, name, trace.WithTimestamp(at), trace.WithAttributes(attrs...))
}

// End finishes span with code: "" or ok is fine, anything else (a token such
// as no_speech or send_failed) marks it failed with that code.
func End(span trace.Span, code string) {
	Code(span, code)
	span.End()
}

// Code sets span's outcome without ending it.
func Code(span trace.Span, code string) {
	if code == "" || code == "ok" {
		return
	}
	span.SetAttributes(attribute.String(AttrErrorType, code))
	span.SetStatus(codes.Error, code)
}

// Proto is the span in ctx as a cadence.v2 frame's trace context; nil when
// ctx holds none (tracing off, or not sampled).
func Proto(ctx context.Context) *cadencev2.TraceContext {
	c := tracectx.Inject(ctx)
	if c.Empty() {
		return nil
	}
	return &cadencev2.TraceContext{Traceparent: c.Traceparent, Tracestate: c.Tracestate}
}

// Audiod is Proto in audiod's own message.
func Audiod(tc *cadencev2.TraceContext) *audiodv1.TraceContext {
	if tc.GetTraceparent() == "" {
		return nil
	}
	return &audiodv1.TraceContext{Traceparent: tc.GetTraceparent(), Tracestate: tc.GetTracestate()}
}

// Extract is ctx with tc's remote span context as its parent; an empty or
// malformed tc leaves ctx as it was.
func Extract(ctx context.Context, tc *cadencev2.TraceContext) context.Context {
	return tracectx.Extract(ctx, tracectx.Carrier{Traceparent: tc.GetTraceparent(), Tracestate: tc.GetTracestate()})
}
