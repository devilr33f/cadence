package tracing

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	commonv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/common/v1"
	resourcev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/resource/v1"
	tracev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/trace/v1"
)

// spanExporter is the sdk's view of the export: the batcher hands it the
// server's finished spans, which it encodes through the guard and queues. It
// never posts itself, so the batcher never waits on the collector.
type spanExporter struct {
	export  *Exporter
	version string
}

func (x spanExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	if !x.export.on() {
		return nil
	}
	if rs := encodeLocal(spans, x.version); rs != nil {
		x.export.Enqueue(rs)
	}
	return nil
}

func (x spanExporter) Shutdown(context.Context) error { return nil }

// on reports whether anything would be sent: a target set and tracing on.
func (e *Exporter) on() bool {
	if !e.enabled.Load() {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.target != nil
}

// encodeLocal is the server's spans as one resource, grouped by scope.
func encodeLocal(spans []sdktrace.ReadOnlySpan, version string) *tracev1.ResourceSpans {
	byScope := map[string]*tracev1.ScopeSpans{}
	rs := &tracev1.ResourceSpans{Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{
		kv("service.name", strValue(serviceServer)), kv("service.version", strValue(version)),
	}}}
	for _, s := range spans {
		scope := s.InstrumentationScope().Name
		if !ours(scope) {
			continue
		}
		ss := byScope[scope]
		if ss == nil {
			ss = &tracev1.ScopeSpans{Scope: &commonv1.InstrumentationScope{Name: scope}}
			byScope[scope] = ss
			rs.ScopeSpans = append(rs.ScopeSpans, ss)
		}
		ss.Spans = append(ss.Spans, encodeSpan(s))
	}
	if len(rs.ScopeSpans) == 0 {
		return nil
	}
	return rs
}

func encodeSpan(s sdktrace.ReadOnlySpan) *tracev1.Span {
	sc := s.SpanContext()
	tid, sid := sc.TraceID(), sc.SpanID()
	out := &tracev1.Span{
		TraceId: tid[:], SpanId: sid[:], TraceState: sc.TraceState().String(), Name: cut(s.Name(), maxName),
		Kind:              tracev1.Span_SpanKind(s.SpanKind()), //nolint:gosec // otel's kinds are otlp's numbers
		StartTimeUnixNano: nanos(s.StartTime()), EndTimeUnixNano: nanos(s.EndTime()),
		Attributes: encodeAttrs(guardKVs(s.Attributes())),
		Flags:      uint32(sc.TraceFlags()),
	}
	if p := s.Parent(); p.IsValid() {
		pid := p.SpanID()
		out.ParentSpanId = pid[:]
		if p.IsRemote() {
			out.Flags |= uint32(tracev1.SpanFlags_SPAN_FLAGS_CONTEXT_HAS_IS_REMOTE_MASK | tracev1.SpanFlags_SPAN_FLAGS_CONTEXT_IS_REMOTE_MASK)
		} else {
			out.Flags |= uint32(tracev1.SpanFlags_SPAN_FLAGS_CONTEXT_HAS_IS_REMOTE_MASK)
		}
	}
	for _, e := range s.Events() {
		out.Events = append(out.Events, &tracev1.Span_Event{TimeUnixNano: nanos(e.Time), Name: cut(e.Name, maxName),
			Attributes: encodeAttrs(guardKVs(e.Attributes))})
	}
	for _, l := range s.Links() {
		lt, ls := l.SpanContext.TraceID(), l.SpanContext.SpanID()
		out.Links = append(out.Links, &tracev1.Span_Link{TraceId: lt[:], SpanId: ls[:]})
	}
	switch st := s.Status(); st.Code {
	case codes.Error:
		out.Status = &tracev1.Status{Code: tracev1.Status_STATUS_CODE_ERROR, Message: statusCode(st.Description)}
	case codes.Ok:
		out.Status = &tracev1.Status{Code: tracev1.Status_STATUS_CODE_OK}
	}
	return out
}

func encodeAttrs(kvs []attribute.KeyValue) []*commonv1.KeyValue {
	if len(kvs) == 0 {
		return nil
	}
	out := make([]*commonv1.KeyValue, 0, len(kvs))
	for _, a := range kvs {
		if v := encodeValue(a.Value); v != nil {
			out = append(out, kv(string(a.Key), v))
		}
	}
	return out
}

func encodeValue(v attribute.Value) *commonv1.AnyValue {
	switch v.Type() {
	case attribute.BOOL:
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_BoolValue{BoolValue: v.AsBool()}}
	case attribute.INT64:
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: v.AsInt64()}}
	case attribute.FLOAT64:
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_DoubleValue{DoubleValue: v.AsFloat64()}}
	case attribute.STRING:
		return strValue(v.AsString())
	case attribute.BOOLSLICE, attribute.INT64SLICE, attribute.FLOAT64SLICE, attribute.STRINGSLICE:
		arr := &commonv1.ArrayValue{}
		for _, e := range sliceValues(v) {
			arr.Values = append(arr.Values, encodeValue(e))
		}
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_ArrayValue{ArrayValue: arr}}
	}
	return nil
}

func sliceValues(v attribute.Value) []attribute.Value {
	var out []attribute.Value
	switch v.Type() {
	case attribute.BOOLSLICE:
		for _, b := range v.AsBoolSlice() {
			out = append(out, attribute.BoolValue(b))
		}
	case attribute.INT64SLICE:
		for _, n := range v.AsInt64Slice() {
			out = append(out, attribute.Int64Value(n))
		}
	case attribute.FLOAT64SLICE:
		for _, f := range v.AsFloat64Slice() {
			out = append(out, attribute.Float64Value(f))
		}
	case attribute.STRINGSLICE:
		for _, s := range v.AsStringSlice() {
			out = append(out, attribute.StringValue(s))
		}
	}
	return out
}

func strValue(s string) *commonv1.AnyValue {
	return &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: s}}
}

func kv(k string, v *commonv1.AnyValue) *commonv1.KeyValue {
	return &commonv1.KeyValue{Key: k, Value: v}
}

func nanos(t time.Time) uint64 {
	if t.IsZero() || t.Before(time.Unix(0, 0)) {
		return 0
	}
	return uint64(t.UnixNano()) //nolint:gosec // after 1970, checked above
}
