package tracing

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// storeProcessor feeds the store. It is synchronous: the store is a bounded
// in-memory structure, and a batching queue would only delay what the live
// view exists to show.
type storeProcessor struct{ store *Store }

func (p *storeProcessor) OnStart(_ context.Context, s sdktrace.ReadWriteSpan) {
	if !ours(s.InstrumentationScope().Name) {
		// libraries speak the otel api too (testcontainers' docker client):
		// their spans are not about what Cadence does for the owner
		p.store.countForeign()
		return
	}
	p.store.start(localView(s))
}

func (p *storeProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	if !ours(s.InstrumentationScope().Name) {
		return
	}
	p.store.end(localView(s))
}

func (p *storeProcessor) Shutdown(context.Context) error   { return nil }
func (p *storeProcessor) ForceFlush(context.Context) error { return nil }

func ours(scope string) bool { return strings.HasPrefix(scope, scopePrefix) }

// localView is a server span as the store keeps it, through the guard.
func localView(s sdktrace.ReadOnlySpan) SpanView {
	sc := s.SpanContext()
	v := SpanView{
		TraceID: sc.TraceID().String(), SpanID: sc.SpanID().String(),
		Name: cut(s.Name(), maxName), Scope: strings.TrimPrefix(s.InstrumentationScope().Name, scopePrefix),
		Kind: strings.ToLower(s.SpanKind().String()), Service: serviceServer, Start: s.StartTime(),
		Attrs: guardAttrs(s.Attributes()),
	}
	if p := s.Parent(); p.IsValid() {
		v.ParentID, v.RemoteParent = p.SpanID().String(), p.IsRemote()
	}
	switch st := s.Status(); st.Code {
	case codes.Error:
		v.Status, v.StatusCode = StatusError, statusCode(st.Description)
	case codes.Ok:
		v.Status = StatusOK
	}
	if end := s.EndTime(); !end.IsZero() {
		v.End = &end
		v.DurationMs = ms(end.Sub(v.Start))
	} else {
		v.Live = true
	}
	for _, l := range s.Links() {
		v.Links = append(v.Links, SpanLinkView{TraceID: l.SpanContext.TraceID().String(), SpanID: l.SpanContext.SpanID().String()})
	}
	for _, e := range s.Events() {
		v.Events = append(v.Events, SpanEventView{Name: cut(e.Name, maxName), At: e.Time, Attrs: guardAttrs(e.Attributes)})
	}
	return v
}
