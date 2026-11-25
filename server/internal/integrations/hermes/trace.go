package hermes

import (
	"errors"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

// endSpan closes a request's span with its status and code: Hermes' error
// carries its code as a field, which tracing.ErrorCode cannot read.
func endSpan(span trace.Span, o outcome, err error) {
	if o.status != 0 {
		span.SetAttributes(attribute.Int(tracing.AttrHTTPStatus, o.status))
	}
	var e *Error
	switch {
	case errors.As(err, &e):
		tracing.Fail(span, e.Code)
	case err != nil:
		tracing.FailErr(span, err)
	case o.status == 429 || o.status >= 500:
		tracing.Fail(span, "status_"+strconv.Itoa(o.status))
	}
	span.End()
}
