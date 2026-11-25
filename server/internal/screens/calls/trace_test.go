package calls_test

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"

	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
	"git.shino.boo/devilreef/cadence/server/internal/tracing/tracingtest"
)

// a call is a span named by its method, the method's own work under it, with
// its status and the events it streamed, never its arguments
func TestCallSpan(t *testing.T) {
	p := tracingtest.Setup(t)
	r, s := newRouter(t)
	traceIDs := make(chan trace.TraceID, 1)
	r.Register("catalog.search", func(ctx context.Context, c calls.Call) calls.Reply {
		_, child := tracing.Start(ctx, "catalog", "catalog.query")
		child.End()
		traceIDs <- child.SpanContext().TraceID()
		_ = c.Event(map[string]int{"n": 1})
		_ = c.Event(map[string]int{"n": 2})
		return calls.OK(nil)
	})
	r.Handle("dev-1", req("1", "catalog.search", true))
	s.wait(t, 3)
	var id trace.TraceID
	select {
	case id = <-traceIDs:
	case <-time.After(5 * time.Second):
		t.Fatal("method never ran")
	}
	spans := tracingtest.Wait(t, p, id.String(), "catalog.search", "catalog.query")
	call := tracingtest.Find(t, spans, "catalog.search")
	if call.Kind != "server" || call.Attrs["cadence.status"] != "ok" || call.Attrs["cadence.events"] != int64(2) ||
		call.Attrs[tracing.AttrDeviceID] != "dev-1" || call.Attrs[tracing.AttrMethod] != "catalog.search" {
		t.Fatalf("call %+v", call)
	}
	if q := tracingtest.Find(t, spans, "catalog.query"); q.ParentID != call.SpanID {
		t.Fatalf("method work outside its call %+v", q)
	}
	for k := range call.Attrs {
		if k == "q" || k == "args" {
			t.Fatalf("arguments on the span %v", call.Attrs)
		}
	}
}
