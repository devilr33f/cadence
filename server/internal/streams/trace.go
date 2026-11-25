package streams

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"git.shino.boo/devilreef/cadence/pkg/tracectx"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

// relaySpan is one relayed fetch, stream.relay: its device, index, status
// and the bytes it moved with their rate, never the token or the upstream's
// url (rule 8). Bytes read from the upstream go to the device as they are,
// so each counts in and out.
type relaySpan struct {
	ctx     context.Context
	span    trace.Span
	once    sync.Once
	mu      sync.Mutex
	pending int64
	last    time.Time
}

func startRelaySpan(c fiber.Ctx, dev string, index int, playlist bool) (context.Context, *relaySpan) {
	// a player that sends a traceparent joins its trace; ffmpeg sends none
	parent := tracectx.Extract(context.Background(), tracectx.Carrier{
		Traceparent: c.Get(tracectx.KeyTraceparent), Tracestate: c.Get(tracectx.KeyTracestate)})
	ctx, span := tracing.StartKind(parent, "streams", "stream.relay", trace.SpanKindServer,
		attribute.String(tracing.AttrDeviceID, dev), attribute.Int("cadence.index", index),
		attribute.Bool("cadence.playlist", playlist), attribute.Bool("cadence.ranged", c.Get(fiber.HeaderRange) != ""))
	return ctx, &relaySpan{ctx: ctx, span: span, last: time.Now()}
}

// moved counts n relayed bytes, reported about once a second and at the end
func (s *relaySpan) moved(n int, last bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.pending += int64(n)
	var report int64
	if last || time.Since(s.last) >= time.Second {
		report, s.pending, s.last = s.pending, 0, time.Now()
	}
	s.mu.Unlock()
	if report > 0 {
		tracing.Progress(s.ctx, report, report)
	}
}

// end closes the span once, with the status the device got
func (s *relaySpan) end(status int) {
	s.once.Do(func() {
		s.moved(0, true)
		s.span.SetAttributes(attribute.Int(tracing.AttrHTTPStatus, status))
		if status >= fiber.StatusBadRequest {
			tracing.Fail(s.span, "status_"+strconv.Itoa(status))
		}
		s.span.End()
	})
}
