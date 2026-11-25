// Package tracectx carries a trace's W3C context (traceparent, tracestate)
// across Cadence's own hops, so one voice turn is one trace from the wake word
// on the device to the server's LLM and provider calls. It is shared by the
// server and cadenced, so it depends on the OpenTelemetry API alone (no SDK, no
// grpc, no cgo).
//
// The wire form is two plain strings, Carrier. Baggage is never carried: its
// values are free text, and rule 8 keeps text off the wire's trace fields.
//
// Where the carrier rides (phase 3 adds the proto fields; this package only
// fills and reads them):
//
//   - the device's SessionStart (the wake or press that opens a turn): cadenced
//     opens the root voice.turn span at the wake's own time (audiod detected it)
//     and sends its context, so the server's turn spans are its children;
//   - RpcRequest: a screen's or device's call joins the caller's trace;
//   - the server frames that answer them: RpcResponse and RpcEvent, and the
//     reply stream's StreamStart (TTS), so the device's playback spans join
//     the turn that produced the audio;
//   - the JSON RPC (pkg/rpc) where a call has no proto frame of its own: the
//     map form (InjectMap, ExtractMap) under the keys traceparent and
//     tracestate.
//
// The device link is one long-lived websocket and is never one span: each
// frame that carries a Carrier is extracted on its own.
package tracectx

import (
	"context"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// The W3C header names, which are also the map keys.
const (
	KeyTraceparent = "traceparent"
	KeyTracestate  = "tracestate"
)

// maxTracestate is the W3C limit on a tracestate a vendor may send; a longer
// one is dropped rather than cut, as a cut entry would be malformed.
const maxTracestate = 512

// Carrier is a trace context on the wire: the traceparent, and the tracestate
// when there is one. The zero value carries nothing.
type Carrier struct {
	Traceparent string `json:"traceparent,omitempty"`
	Tracestate  string `json:"tracestate,omitempty"`
}

// tc is the propagator: TraceContext alone, never the global one, so what
// crosses Cadence's hops does not depend on how a process set otel up.
var tc = propagation.TraceContext{}

// Empty reports whether c carries no context.
func (c Carrier) Empty() bool { return c.Traceparent == "" }

// Inject is the carrier of the span in ctx; empty when ctx holds no valid
// span context (tracing off, or not sampled and not propagated).
func Inject(ctx context.Context) Carrier {
	m := propagation.MapCarrier{}
	tc.Inject(ctx, m)
	c := Carrier{Traceparent: m[KeyTraceparent], Tracestate: m[KeyTracestate]}
	if len(c.Tracestate) > maxTracestate {
		c.Tracestate = ""
	}
	return c
}

// Extract returns ctx with c's remote span context as its parent; a malformed
// or empty carrier leaves ctx as it was.
func Extract(ctx context.Context, c Carrier) context.Context {
	if c.Empty() {
		return ctx
	}
	m := propagation.MapCarrier{KeyTraceparent: c.Traceparent}
	if c.Tracestate != "" && len(c.Tracestate) <= maxTracestate {
		m[KeyTracestate] = c.Tracestate
	}
	return tc.Extract(ctx, m)
}

// InjectMap writes ctx's carrier into m under the W3C keys; nothing when
// there is none.
func InjectMap(ctx context.Context, m map[string]string) {
	c := Inject(ctx)
	if c.Empty() || m == nil {
		return
	}
	m[KeyTraceparent] = c.Traceparent
	if c.Tracestate != "" {
		m[KeyTracestate] = c.Tracestate
	}
}

// ExtractMap is Extract of the carrier m holds under the W3C keys.
func ExtractMap(ctx context.Context, m map[string]string) context.Context {
	return Extract(ctx, Carrier{Traceparent: m[KeyTraceparent], Tracestate: m[KeyTracestate]})
}

// SpanContext is c's remote span context, invalid when c carries none.
func SpanContext(c Carrier) trace.SpanContext {
	return trace.SpanContextFromContext(Extract(context.Background(), c))
}
