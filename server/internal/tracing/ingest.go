package tracing

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/protobuf/proto"

	coltracev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/trace/v1"
	commonv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/common/v1"
	resourcev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/resource/v1"
	tracev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/trace/v1"
)

// ingest bounds
const (
	// spans one request may store; the rest are rejected
	maxIngestSpans = 2048
	// a span further than this from the receive time is a wrong clock
	ingestSkew = 24 * time.Hour
)

// ErrDecode is a request body that is no OTLP trace request.
var ErrDecode = errors.New("tracing: malformed trace request")

// DeviceRef stamps a device's spans, as the telemetry export stamps its
// lines: service.name cadence-device, device.id, device.name and board.
type DeviceRef struct{ ID, Name, Board string }

// IngestResult counts a request's spans: stored, and rejected (malformed,
// past the cap, a wrong clock, or a full trace).
type IngestResult struct{ Stored, Rejected int }

// Ingest takes a device's OTLP/HTTP trace request (an
// ExportTraceServiceRequest, decompressed): each span through the guard,
// string values through scrub (the device's secrets), its resource replaced
// by dev's stamp, then into the store, where it merges with the server's
// spans of the same trace, and on to the export. While tracing is off the
// spans are dropped, and the request still succeeds.
func (p *Provider) Ingest(dev DeviceRef, body []byte, scrub func(string) string, received time.Time) (IngestResult, error) {
	var req coltracev1.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		return IngestResult{}, ErrDecode
	}
	if scrub == nil {
		scrub = func(s string) string { return s }
	}
	in := ingester{dev: dev, scrub: scrub, received: received}
	var views []SpanView
	for _, rs := range req.GetResourceSpans() {
		out := in.resource(rs, &views)
		if out != nil && enabled.Load() {
			p.export.Enqueue(out)
		}
	}
	if !enabled.Load() {
		return IngestResult{Rejected: in.rejected}, nil
	}
	stored := p.store.add(views)
	return IngestResult{Stored: stored, Rejected: in.rejected + len(views) - stored}, nil
}

type ingester struct {
	dev      DeviceRef
	scrub    func(string) string
	received time.Time
	taken    int
	rejected int
}

// resource is rs's valid spans, guarded and stamped, for the export; their
// views are appended to views.
func (in *ingester) resource(rs *tracev1.ResourceSpans, views *[]SpanView) *tracev1.ResourceSpans {
	attrs := []*commonv1.KeyValue{kv("service.name", strValue(serviceDevice)), kv("device.id", strValue(in.dev.ID))}
	if in.dev.Name != "" {
		attrs = append(attrs, kv("device.name", strValue(in.dev.Name)))
	}
	if in.dev.Board != "" {
		attrs = append(attrs, kv("board", strValue(in.dev.Board)))
	}
	for _, a := range rs.GetResource().GetAttributes() {
		if a.GetKey() == "service.version" && a.GetValue().GetStringValue() != "" {
			attrs = append(attrs, kv("service.version", strValue(cut(in.scrub(a.GetValue().GetStringValue()), 64))))
		}
	}
	out := &tracev1.ResourceSpans{Resource: &resourcev1.Resource{Attributes: attrs}}
	for _, ss := range rs.GetScopeSpans() {
		scope := cut(ss.GetScope().GetName(), maxName)
		var kept []*tracev1.Span
		for _, s := range ss.GetSpans() {
			if in.taken >= maxIngestSpans || !in.valid(s) {
				in.rejected++
				continue
			}
			in.taken++
			s = in.clean(s)
			kept = append(kept, s)
			*views = append(*views, in.view(s, scope))
		}
		if len(kept) > 0 {
			out.ScopeSpans = append(out.ScopeSpans, &tracev1.ScopeSpans{Scope: &commonv1.InstrumentationScope{Name: scope}, Spans: kept})
		}
	}
	if len(out.ScopeSpans) == 0 {
		return nil
	}
	return out
}

var (
	zeroTrace = make([]byte, 16)
	zeroSpan  = make([]byte, 8)
)

func (in *ingester) valid(s *tracev1.Span) bool {
	if len(s.GetTraceId()) != 16 || bytes.Equal(s.GetTraceId(), zeroTrace) || len(s.GetSpanId()) != 8 ||
		bytes.Equal(s.GetSpanId(), zeroSpan) {
		return false
	}
	if p := s.GetParentSpanId(); len(p) != 0 && (len(p) != 8 || bytes.Equal(p, zeroSpan)) {
		return false
	}
	start, end := s.GetStartTimeUnixNano(), s.GetEndTimeUnixNano()
	if start == 0 || end < start || start > uint64(1<<63-1) || end > uint64(1<<63-1) {
		return false
	}
	t := time.Unix(0, int64(start)) //nolint:gosec // checked against the int64 range above
	return !t.Before(in.received.Add(-ingestSkew)) && !t.After(in.received.Add(ingestSkew))
}

// clean is s through the guard and the scrubber: what the store keeps and the
// export sends.
func (in *ingester) clean(s *tracev1.Span) *tracev1.Span {
	out := &tracev1.Span{
		TraceId: s.GetTraceId(), SpanId: s.GetSpanId(), ParentSpanId: s.GetParentSpanId(), Flags: s.GetFlags(),
		Name: cut(in.scrub(s.GetName()), maxName), Kind: s.GetKind(),
		StartTimeUnixNano: s.GetStartTimeUnixNano(), EndTimeUnixNano: s.GetEndTimeUnixNano(),
		Attributes: encodeAttrs(in.guard(s.GetAttributes())),
	}
	for i, e := range s.GetEvents() {
		if i == limitEventCount {
			break
		}
		out.Events = append(out.Events, &tracev1.Span_Event{TimeUnixNano: e.GetTimeUnixNano(), Name: cut(in.scrub(e.GetName()), maxName),
			Attributes: encodeAttrs(in.guard(e.GetAttributes()))})
	}
	for i, l := range s.GetLinks() {
		if i == limitLinkCount {
			break
		}
		if len(l.GetTraceId()) == 16 && len(l.GetSpanId()) == 8 {
			out.Links = append(out.Links, &tracev1.Span_Link{TraceId: l.GetTraceId(), SpanId: l.GetSpanId()})
		}
	}
	switch s.GetStatus().GetCode() {
	case tracev1.Status_STATUS_CODE_ERROR:
		out.Status = &tracev1.Status{Code: tracev1.Status_STATUS_CODE_ERROR, Message: statusCode(s.GetStatus().GetMessage())}
	case tracev1.Status_STATUS_CODE_OK:
		out.Status = &tracev1.Status{Code: tracev1.Status_STATUS_CODE_OK}
	}
	return out
}

// guard is a device's attributes as otel's, through the guard and the
// scrubber; nested maps and bytes are dropped.
func (in *ingester) guard(kvs []*commonv1.KeyValue) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, min(len(kvs), limitAttrCount))
	for _, a := range kvs {
		if len(out) == limitAttrCount {
			break
		}
		if v, ok := in.decodeValue(a.GetValue()); ok {
			out = append(out, attribute.KeyValue{Key: attribute.Key(cut(a.GetKey(), maxName)), Value: v})
		}
	}
	return guardKVs(out)
}

func (in *ingester) decodeValue(v *commonv1.AnyValue) (attribute.Value, bool) {
	switch x := v.GetValue().(type) {
	case *commonv1.AnyValue_StringValue:
		return attribute.StringValue(in.scrub(cut(x.StringValue, 4*maxValue))), true
	case *commonv1.AnyValue_BoolValue:
		return attribute.BoolValue(x.BoolValue), true
	case *commonv1.AnyValue_IntValue:
		return attribute.Int64Value(x.IntValue), true
	case *commonv1.AnyValue_DoubleValue:
		return attribute.Float64Value(x.DoubleValue), true
	case *commonv1.AnyValue_ArrayValue:
		vals := x.ArrayValue.GetValues()
		if len(vals) == 0 || len(vals) > maxSlice {
			return attribute.Value{}, false
		}
		var strs []string
		var ints []int64
		var floats []float64
		var bools []bool
		for _, e := range vals {
			switch y := e.GetValue().(type) {
			case *commonv1.AnyValue_StringValue:
				strs = append(strs, in.scrub(cut(y.StringValue, 4*maxValue)))
			case *commonv1.AnyValue_IntValue:
				ints = append(ints, y.IntValue)
			case *commonv1.AnyValue_DoubleValue:
				floats = append(floats, y.DoubleValue)
			case *commonv1.AnyValue_BoolValue:
				bools = append(bools, y.BoolValue)
			default:
				return attribute.Value{}, false
			}
		}
		// one type throughout, as otel's slices are
		switch len(vals) {
		case len(strs):
			return attribute.StringSliceValue(strs), true
		case len(ints):
			return attribute.Int64SliceValue(ints), true
		case len(floats):
			return attribute.Float64SliceValue(floats), true
		case len(bools):
			return attribute.BoolSliceValue(bools), true
		}
	}
	return attribute.Value{}, false
}

// view is a cleaned device span as the store keeps it.
func (in *ingester) view(s *tracev1.Span, scope string) SpanView {
	start := time.Unix(0, int64(s.GetStartTimeUnixNano())) //nolint:gosec // valid checked the range
	end := time.Unix(0, int64(s.GetEndTimeUnixNano()))     //nolint:gosec // valid checked the range
	v := SpanView{
		TraceID: hex.EncodeToString(s.GetTraceId()), SpanID: hex.EncodeToString(s.GetSpanId()),
		Name: s.GetName(), Scope: strings.TrimPrefix(scope, scopePrefix), Kind: kindName(s.GetKind()),
		Service: serviceDevice, DeviceID: in.dev.ID, DeviceName: in.dev.Name, Board: in.dev.Board,
		Start: start, End: &end, DurationMs: ms(end.Sub(start)), Attrs: decodedMap(s.GetAttributes()),
	}
	if p := s.GetParentSpanId(); len(p) == 8 {
		v.ParentID = hex.EncodeToString(p)
		v.RemoteParent = s.GetFlags()&uint32(tracev1.SpanFlags_SPAN_FLAGS_CONTEXT_IS_REMOTE_MASK) != 0
	}
	switch s.GetStatus().GetCode() {
	case tracev1.Status_STATUS_CODE_ERROR:
		v.Status, v.StatusCode = StatusError, s.GetStatus().GetMessage()
	case tracev1.Status_STATUS_CODE_OK:
		v.Status = StatusOK
	}
	for _, e := range s.GetEvents() {
		v.Events = append(v.Events, SpanEventView{Name: e.GetName(), At: time.Unix(0, int64(e.GetTimeUnixNano())), //nolint:gosec // a time, wrapped past 2262 at worst
			Attrs: decodedMap(e.GetAttributes())})
	}
	for _, l := range s.GetLinks() {
		v.Links = append(v.Links, SpanLinkView{TraceID: hex.EncodeToString(l.GetTraceId()), SpanID: hex.EncodeToString(l.GetSpanId())})
	}
	return v
}

// decodedMap is cleaned otlp attributes as the views carry them.
func decodedMap(kvs []*commonv1.KeyValue) map[string]any {
	in := ingester{scrub: func(s string) string { return s }}
	list := make([]attribute.KeyValue, 0, len(kvs))
	for _, a := range kvs {
		if v, ok := in.decodeValue(a.GetValue()); ok {
			list = append(list, attribute.KeyValue{Key: attribute.Key(a.GetKey()), Value: v})
		}
	}
	return guardAttrs(list)
}

func kindName(k tracev1.Span_SpanKind) string {
	switch k {
	case tracev1.Span_SPAN_KIND_SERVER:
		return "server"
	case tracev1.Span_SPAN_KIND_CLIENT:
		return "client"
	case tracev1.Span_SPAN_KIND_PRODUCER:
		return "producer"
	case tracev1.Span_SPAN_KIND_CONSUMER:
		return "consumer"
	}
	return "internal"
}
