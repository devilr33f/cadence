//go:build !cadence_notrace

package tracing

import (
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	commonv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/common/v1"
	tracev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/trace/v1"
)

// otlp's span flags past the w3c trace flags: whether the parent's remoteness
// is known, and that it is remote
const (
	flagHasIsRemote = uint32(tracev1.SpanFlags_SPAN_FLAGS_CONTEXT_HAS_IS_REMOTE_MASK)
	flagIsRemote    = uint32(tracev1.SpanFlags_SPAN_FLAGS_CONTEXT_IS_REMOTE_MASK)
)

// encodeSpan is s as an OTLP span; the server reads its parent's remoteness
// from the flags.
func encodeSpan(s sdktrace.ReadOnlySpan) *tracev1.Span {
	sc := s.SpanContext()
	tid, sid := sc.TraceID(), sc.SpanID()
	out := &tracev1.Span{
		TraceId: tid[:], SpanId: sid[:], Name: s.Name(), Kind: tracev1.Span_SpanKind(s.SpanKind()), //nolint:gosec // otel's kinds are otlp's numbers
		Flags:             uint32(sc.TraceFlags()) | flagHasIsRemote,
		StartTimeUnixNano: nanos(s.StartTime()), EndTimeUnixNano: nanos(s.EndTime()),
		Attributes:             encodeAttrs(s.Attributes()),
		DroppedAttributesCount: uint32(max(s.DroppedAttributes(), 0)), //nolint:gosec // a count
		DroppedEventsCount:     uint32(max(s.DroppedEvents(), 0)),     //nolint:gosec // a count
	}
	if p := s.Parent(); p.IsValid() {
		psid := p.SpanID()
		out.ParentSpanId = psid[:]
		if p.IsRemote() {
			out.Flags |= flagIsRemote
		}
	}
	if ts := sc.TraceState().String(); ts != "" {
		out.TraceState = ts
	}
	for _, e := range s.Events() {
		out.Events = append(out.Events, &tracev1.Span_Event{TimeUnixNano: nanos(e.Time), Name: e.Name, Attributes: encodeAttrs(e.Attributes)})
	}
	for _, l := range s.Links() {
		ltid, lsid := l.SpanContext.TraceID(), l.SpanContext.SpanID()
		out.Links = append(out.Links, &tracev1.Span_Link{TraceId: ltid[:], SpanId: lsid[:]})
	}
	switch s.Status().Code {
	case codes.Error:
		out.Status = &tracev1.Status{Code: tracev1.Status_STATUS_CODE_ERROR, Message: s.Status().Description}
	case codes.Ok:
		out.Status = &tracev1.Status{Code: tracev1.Status_STATUS_CODE_OK}
	}
	return out
}

func encodeAttrs(kvs []attribute.KeyValue) []*commonv1.KeyValue {
	out := make([]*commonv1.KeyValue, 0, len(kvs))
	for _, kv := range kvs {
		if v := encodeValue(kv.Value); v != nil {
			out = append(out, &commonv1.KeyValue{Key: string(kv.Key), Value: v})
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
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: v.AsString()}}
	case attribute.BOOLSLICE, attribute.INT64SLICE, attribute.FLOAT64SLICE, attribute.STRINGSLICE:
		var vals []*commonv1.AnyValue
		switch v.Type() {
		case attribute.BOOLSLICE:
			for _, b := range v.AsBoolSlice() {
				vals = append(vals, encodeValue(attribute.BoolValue(b)))
			}
		case attribute.INT64SLICE:
			for _, n := range v.AsInt64Slice() {
				vals = append(vals, encodeValue(attribute.Int64Value(n)))
			}
		case attribute.FLOAT64SLICE:
			for _, f := range v.AsFloat64Slice() {
				vals = append(vals, encodeValue(attribute.Float64Value(f)))
			}
		default:
			for _, s := range v.AsStringSlice() {
				vals = append(vals, encodeValue(attribute.StringValue(s)))
			}
		}
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_ArrayValue{ArrayValue: &commonv1.ArrayValue{Values: vals}}}
	}
	return nil
}

// a time before the epoch (an unset clock) is the epoch
func nanos(t time.Time) uint64 {
	return uint64(max(t.UnixNano(), 0)) //nolint:gosec // never negative
}
