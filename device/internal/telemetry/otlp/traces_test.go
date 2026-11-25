package otlp_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp"
	coltracev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/trace/v1"
	tracev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/trace/v1"
)

// the framed request decodes as one resource with the service's name and version, one cadence/device scope, and the
// spans as they were encoded, in order
func TestTracesRequestFraming(t *testing.T) {
	spans := []*tracev1.Span{{Name: "voice.turn", StartTimeUnixNano: 1}, {Name: "voice.playback", StartTimeUnixNano: 2}}
	var encoded [][]byte
	for _, s := range spans {
		b, err := proto.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, b)
	}
	var req coltracev1.ExportTraceServiceRequest
	if err := proto.Unmarshal(otlp.TracesRequest(otlp.TraceResource("1.2.3"), encoded), &req); err != nil {
		t.Fatal(err)
	}
	rs := req.GetResourceSpans()
	if len(rs) != 1 || len(rs[0].GetScopeSpans()) != 1 {
		t.Fatalf("shape %v", &req)
	}
	attrs := rs[0].GetResource().GetAttributes()
	if len(attrs) != 2 || attrs[0].GetValue().GetStringValue() != otlp.ServiceName || attrs[1].GetKey() != "service.version" ||
		attrs[1].GetValue().GetStringValue() != "1.2.3" {
		t.Fatalf("resource %v", attrs)
	}
	ss := rs[0].GetScopeSpans()[0]
	if ss.GetScope().GetName() != otlp.TraceScope || len(ss.GetSpans()) != 2 || ss.GetSpans()[1].GetName() != "voice.playback" {
		t.Fatalf("scope %v", ss)
	}
}
