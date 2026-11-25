package ha

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

// startSpan opens ha.command for one websocket command: its type and, for a
// service call, the service's domain; never an entity id, a name or a state
// (rule 8).
func startSpan(ctx context.Context, typ string, fields map[string]any) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{attribute.String("cadence.ha_command", typ)}
	if d, ok := fields["domain"].(string); ok && typ == "call_service" {
		attrs = append(attrs, attribute.String("cadence.ha_domain", d))
	}
	return tracing.StartKind(ctx, "ha", "ha.command", trace.SpanKindClient, attrs...)
}

// endSpan: the client's error carries its code as a field
func endSpan(span trace.Span, err error) {
	var e *Error
	switch {
	case errors.As(err, &e):
		tracing.Fail(span, e.Code)
	case err != nil:
		tracing.FailErr(span, err)
	}
	span.End()
}
