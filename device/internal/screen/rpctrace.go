package screen

import (
	"context"
	"regexp"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"git.shino.boo/devilreef/cadence/device/internal/tracing"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
)

// a call's span is bound under its prefix and id until its answer, a subscribed call's until its last event;
// rpcSpanPrefix is both prefixes' own
const (
	rpcSpanPrefix  = "rpc"
	unaryPrefix    = "rpc:"
	streamedPrefix = "rpcs:"
)

// a method's name as pkg/rpc spells them; anything else is named other, as the span's name must stay a code
var methodName = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,63}$`)

// rpcStart opens the client span of a screen's call, "rpc <method>": a child of the screen's own span if its request
// carries one, else a root. The request carries it on to the server, whose rpc span is its child. Its arguments never
// reach the span (rule 8).
func rpcStart(r *cadencev2.RpcRequest) {
	method := r.GetMethod()
	if !methodName.MatchString(method) {
		method = "other"
	}
	ctx := tracing.Extract(context.Background(), r.GetTrace())
	ctx, span := tracing.StartKind(ctx, "rpc "+method, trace.SpanKindClient,
		attribute.String(tracing.AttrMethod, method), attribute.Bool("cadence.rpc.subscribe", r.GetSubscribe()))
	if tc := tracing.Proto(ctx); tc != nil {
		r.Trace = tc
	}
	tracing.Bind(rpcKey(r.GetId(), r.GetSubscribe()), span, nil)
}

func rpcKey(id string, subscribe bool) string {
	if subscribe {
		return streamedPrefix + id
	}
	return unaryPrefix + id
}

// rpcUnsent ends the span of a call that never left
func rpcUnsent(r *cadencev2.RpcRequest) {
	tracing.Release(rpcKey(r.GetId(), r.GetSubscribe()), rpc.StatusUnavailable)
}

// rpcAnswered ends a call's span at its answer, unless the answer opens a stream: then its last event does
func rpcAnswered(a *cadencev2.RpcResponse) {
	status := attribute.String("cadence.rpc.status", a.GetStatus())
	if tracing.Release(unaryPrefix+a.GetId(), a.GetStatus(), status) {
		return
	}
	span := tracing.Bound(streamedPrefix + a.GetId())
	if span == nil {
		return
	}
	if a.GetStatus() != rpc.StatusOK {
		tracing.Release(streamedPrefix+a.GetId(), a.GetStatus(), status)
		return
	}
	span.SetAttributes(status)
	span.AddEvent("answered")
}

// rpcEvent ends a subscribed call's span at its last event
func rpcEvent(e *cadencev2.RpcEvent) {
	if e.GetLast() {
		tracing.Release(streamedPrefix+e.GetId(), rpc.StatusOK)
	}
}
