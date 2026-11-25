package gateway

import (
	"context"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/tracectx"
)

// FrameContext is ctx with the trace context a device's frame carries as its
// remote parent: a SessionStart's (the device's voice.turn span) or an
// RpcRequest's (the caller's client span). Any other frame, or one without a
// context or with a malformed one, leaves ctx as it was. The link is never one
// span: each frame is extracted on its own.
func FrameContext(ctx context.Context, env *cadencev2.Envelope) context.Context {
	switch b := env.GetBody().(type) {
	case *cadencev2.Envelope_SessionStart:
		return tracectx.Extract(ctx, Carrier(b.SessionStart.GetTrace()))
	case *cadencev2.Envelope_RpcRequest:
		return tracectx.Extract(ctx, Carrier(b.RpcRequest.GetTrace()))
	}
	return ctx
}

// Carrier is tc as pkg/tracectx's carrier; nil carries nothing.
func Carrier(tc *cadencev2.TraceContext) tracectx.Carrier {
	return tracectx.Carrier{Traceparent: tc.GetTraceparent(), Tracestate: tc.GetTracestate()}
}

// TraceOf is the span in ctx as a frame's trace context, for the server's
// frames that answer or follow a device's (RpcResponse, RpcEvent, a reply's
// StreamStart); nil when ctx holds none (tracing off, or not sampled).
func TraceOf(ctx context.Context) *cadencev2.TraceContext {
	c := tracectx.Inject(ctx)
	if c.Empty() {
		return nil
	}
	return &cadencev2.TraceContext{Traceparent: c.Traceparent, Tracestate: c.Tracestate}
}
