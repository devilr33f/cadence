package tracing

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	coltracev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/trace/v1"
	commonv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/common/v1"
	resourcev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/resource/v1"
	tracev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/trace/v1"
	"git.shino.boo/devilreef/cadence/pkg/tracectx"
)

func unhex(s string) []byte {
	b, _ := hex.DecodeString(s)
	return b
}

func resourceOf(n int) *tracev1.ResourceSpans {
	ss := &tracev1.ScopeSpans{}
	for i := range n {
		ss.Spans = append(ss.Spans, &tracev1.Span{TraceId: unhex(tid(i)), SpanId: unhex(sid(i)), Name: "x",
			StartTimeUnixNano: 1, EndTimeUnixNano: 2})
	}
	return &tracev1.ResourceSpans{ScopeSpans: []*tracev1.ScopeSpans{ss}}
}

func devSpan(trace, id, parent string, start time.Time, d time.Duration, attrs ...*commonv1.KeyValue) *tracev1.Span {
	s := &tracev1.Span{TraceId: unhex(trace), SpanId: unhex(id), Name: "voice.turn", Kind: tracev1.Span_SPAN_KIND_INTERNAL,
		StartTimeUnixNano: uint64(start.UnixNano()), EndTimeUnixNano: uint64(start.Add(d).UnixNano()), Attributes: attrs} //nolint:gosec // a test time
	if parent != "" {
		s.ParentSpanId = unhex(parent)
	}
	return s
}

func TestIngestStampsGuardsScrubsAndMerges(t *testing.T) {
	p := setup(t)
	stop := p.Run(context.Background())
	defer stop()
	c := newCollector()
	p.SetTarget(c)
	// the server's turn spans first, children of the device's voice.turn
	wake := time.Now().Add(-3 * time.Second)
	turn := "aaaaaaaaaaaaaaa1"
	trace := "0f0e0d0c0b0a09080706050403020100"
	parent := tracectx.Carrier{Traceparent: "00-" + trace + "-" + turn + "-01"}
	_, srv := Start(tracectx.Extract(context.Background(), parent), "voice", "voice.session")
	End(srv, nil)

	stage := devSpan(trace, "aaaaaaaaaaaaaaa2", turn, wake, 200*time.Millisecond,
		kv("cadence.stage", strValue("vqe")), kv("prompt", strValue("words")), kv("cadence.note", strValue("key=tok-123")))
	stage.Status = &tracev1.Status{Code: tracev1.Status_STATUS_CODE_ERROR, Message: "open /dev/snd failed: tok-123"}
	req := &coltracev1.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
		Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{
			kv("service.name", strValue("spoofed")), kv("device.id", strValue("someone-else")), kv("service.version", strValue("2.1.0")),
		}},
		ScopeSpans: []*tracev1.ScopeSpans{{Scope: &commonv1.InstrumentationScope{Name: "cadence/voice"}, Spans: []*tracev1.Span{
			devSpan(trace, turn, "", wake, 2*time.Second),
			stage,
			devSpan(trace, "aaaaaaaaaaaaaaa3", turn, wake.Add(-48*time.Hour), time.Second),             // a wrong clock
			{TraceId: unhex(trace), SpanId: make([]byte, 8), StartTimeUnixNano: 1, EndTimeUnixNano: 2}, // a zero id
		}}},
	}}}
	body, _ := proto.Marshal(req)
	scrub := func(s string) string { return strings.ReplaceAll(s, "tok-123", "[device_token]") }
	dev := DeviceRef{ID: "11111111-1111-1111-1111-111111111111", Name: "kitchen", Board: "yandex-pickle"}
	res, err := p.Ingest(dev, body, scrub, time.Now())
	if err != nil || res.Stored != 2 || res.Rejected != 2 {
		t.Fatalf("result %+v %v", res, err)
	}
	tr, ok := p.Store().Trace(trace)
	if !ok || len(tr.Spans) != 3 || len(tr.RootIDs) != 1 || tr.RootIDs[0] != turn || tr.DeviceID != dev.ID || tr.Name != "voice.turn" {
		t.Fatalf("merged %+v roots %v", tr.TraceSummary, tr.RootIDs)
	}
	var got SpanView
	for _, v := range tr.Spans {
		if v.SpanID == "aaaaaaaaaaaaaaa2" {
			got = v
		}
	}
	if got.Service != serviceDevice || got.DeviceName != "kitchen" || got.Board != "yandex-pickle" || got.Scope != "voice" ||
		got.Attrs["cadence.stage"] != "vqe" || got.Attrs["prompt"] != nil || got.Attrs["cadence.note"] != "key=[device_token]" ||
		got.StatusCode != "error" {
		t.Fatalf("device span %+v", got)
	}
	// forwarded to the export, stamped by the server whatever the device said
	_ = p.tp.ForceFlush(context.Background())
	eventually(t, func() bool { return p.Stats().Export.Sent == 3 })
	var stamped *tracev1.ResourceSpans
	for _, r := range c.requests() {
		for _, rs := range r.ResourceSpans {
			if rs.Resource.Attributes[0].Value.GetStringValue() == serviceDevice {
				stamped = rs
			}
		}
	}
	if stamped == nil {
		t.Fatal("device spans not exported")
	}
	attrs := map[string]string{}
	for _, a := range stamped.Resource.Attributes {
		attrs[a.Key] = a.Value.GetStringValue()
	}
	if attrs["device.id"] != dev.ID || attrs["device.name"] != "kitchen" || attrs["board"] != "yandex-pickle" || attrs["service.version"] != "2.1.0" {
		t.Fatalf("stamp %v", attrs)
	}
	for _, s := range stamped.ScopeSpans[0].Spans {
		for _, a := range s.Attributes {
			if a.Key == "prompt" || strings.Contains(a.Value.GetStringValue(), "tok-123") {
				t.Fatalf("exported %s=%v", a.Key, a.Value)
			}
		}
	}
	if _, err := p.Ingest(dev, []byte{0xff, 0xff}, scrub, time.Now()); !errors.Is(err, ErrDecode) {
		t.Fatalf("garbage: %v", err)
	}
}

func TestIngestWhileOffDropsQuietly(t *testing.T) {
	p := setup(t)
	p.Apply(Settings{SampleRatio: 1, RingSize: 100})
	body, _ := proto.Marshal(&coltracev1.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
		ScopeSpans: []*tracev1.ScopeSpans{{Spans: []*tracev1.Span{devSpan(tid(1), sid(1), "", time.Now(), time.Second)}}},
	}}})
	res, err := p.Ingest(DeviceRef{ID: "d"}, body, nil, time.Now())
	if err != nil || res.Stored != 0 || res.Rejected != 0 || len(p.Store().List(ListQuery{}).Traces) != 0 {
		t.Fatalf("off %+v %v", res, err)
	}
}
