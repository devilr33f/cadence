package calls

import (
	"context"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

// callSpan is one call's span, named by its method (a registered name, never
// the device's text), with its status and the events it streamed before its
// answer; never its arguments or results (rule 8).
type callSpan struct {
	span   trace.Span
	events atomic.Int64
}

func startSpan(ctx context.Context, c Call) (context.Context, *callSpan) {
	ctx, span := tracing.StartKind(ctx, "rpc", c.Method, trace.SpanKindServer,
		attribute.String(tracing.AttrDeviceID, c.DeviceID), attribute.String(tracing.AttrMethod, c.Method),
		attribute.Bool("cadence.subscribe", c.Subscribe))
	return ctx, &callSpan{span: span}
}

func (s *callSpan) event() { s.events.Add(1) }

func (s *callSpan) end(rep Reply, sent bool) {
	status := rep.Status
	if status == "" {
		status = rpc.StatusOK
	}
	s.span.SetAttributes(attribute.String("cadence.status", status), attribute.Int64("cadence.events", s.events.Load()),
		attribute.Bool("cadence.follows", rep.Follow != nil))
	switch {
	case !sent:
		tracing.Fail(s.span, "unsent")
	case status == rpc.StatusInternal || status == rpc.StatusUnavailable:
		tracing.Fail(s.span, status)
	}
	s.span.End()
}
