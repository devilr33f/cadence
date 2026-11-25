package otlp_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp"
	collogsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/logs/v1"
	commonv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/common/v1"
	logsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/logs/v1"
)

func str(s string) *commonv1.AnyValue {
	return &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: s}}
}

// the framed request decodes as one resource with service.name alone, one
// cadence.device scope, and the records as they were encoded, in order
func TestLogsRequestFraming(t *testing.T) {
	recs := []*logsv1.LogRecord{
		{TimeUnixNano: 1, SeverityNumber: logsv1.SeverityNumber_SEVERITY_NUMBER_INFO, Body: str("one")},
		{TimeUnixNano: 2, SeverityNumber: logsv1.SeverityNumber_SEVERITY_NUMBER_ERROR, Body: str("two")},
	}
	var encoded [][]byte
	for _, r := range recs {
		b, err := proto.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, b)
	}
	var req collogsv1.ExportLogsServiceRequest
	if err := proto.Unmarshal(otlp.LogsRequest(encoded), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.GetResourceLogs()) != 1 {
		t.Fatalf("resource logs = %d", len(req.GetResourceLogs()))
	}
	rl := req.GetResourceLogs()[0]
	attrs := rl.GetResource().GetAttributes()
	if len(attrs) != 1 || attrs[0].GetKey() != "service.name" || attrs[0].GetValue().GetStringValue() != "cadenced" {
		t.Fatalf("resource = %v", rl.GetResource())
	}
	if len(rl.GetScopeLogs()) != 1 || rl.GetScopeLogs()[0].GetScope().GetName() != "cadence.device" {
		t.Fatalf("scope logs = %v", rl.GetScopeLogs())
	}
	got := rl.GetScopeLogs()[0].GetLogRecords()
	if len(got) != len(recs) {
		t.Fatalf("records = %d", len(got))
	}
	for i := range recs {
		if !proto.Equal(got[i], recs[i]) {
			t.Fatalf("record %d = %v, want %v", i, got[i], recs[i])
		}
	}
	// no records is still a well-formed request
	if err := proto.Unmarshal(otlp.LogsRequest(nil), &req); err != nil {
		t.Fatal(err)
	}
}
