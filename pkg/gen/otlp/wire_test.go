package otlp_test

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	collogsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/logs/v1"
	colmetricsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/metrics/v1"
	commonv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/common/v1"
	logsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/logs/v1"
	metricsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/metrics/v1"
	resourcev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/resource/v1"
)

// field returns the payload of the first length-delimited field num in b.
func field(t *testing.T, b []byte, num protowire.Number) []byte {
	t.Helper()
	for len(b) > 0 {
		n, typ, l := protowire.ConsumeTag(b)
		if l < 0 {
			t.Fatal("bad tag")
		}
		b = b[l:]
		if n == num && typ == protowire.BytesType {
			v, _ := protowire.ConsumeBytes(b)
			return v
		}
		l = protowire.ConsumeFieldValue(n, typ, b)
		if l < 0 {
			t.Fatal("bad value")
		}
		b = b[l:]
	}
	t.Fatalf("field %d missing", num)
	return nil
}

func TestLogsRequestFieldNumbers(t *testing.T) {
	req := &collogsv1.ExportLogsServiceRequest{ResourceLogs: []*logsv1.ResourceLogs{{
		Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{{
			Key: "device.id", Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "d1"}},
		}}},
		ScopeLogs: []*logsv1.ScopeLogs{{LogRecords: []*logsv1.LogRecord{{
			TimeUnixNano: 1, SeverityNumber: logsv1.SeverityNumber_SEVERITY_NUMBER_INFO, SeverityText: "INFO",
			Body: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "hello"}},
		}}}},
	}}}
	raw, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	// ExportLogsServiceRequest.resource_logs = 1, ResourceLogs.resource = 1 and scope_logs = 2,
	// ScopeLogs.log_records = 2, LogRecord.time_unix_nano = 1 (fixed64), severity_number = 2, body = 5
	rl := field(t, raw, 1)
	if len(field(t, rl, 1)) == 0 {
		t.Fatal("resource empty")
	}
	rec := field(t, field(t, rl, 2), 2)
	body := field(t, rec, 5)
	if got := string(field(t, body, 1)); got != "hello" { // AnyValue.string_value = 1
		t.Fatalf("body = %q", got)
	}
	var back collogsv1.ExportLogsServiceRequest
	if err := proto.Unmarshal(raw, &back); err != nil || !proto.Equal(&back, req) {
		t.Fatalf("round trip: %v", err)
	}
}

func TestMetricsRequestFieldNumbers(t *testing.T) {
	req := &colmetricsv1.ExportMetricsServiceRequest{ResourceMetrics: []*metricsv1.ResourceMetrics{{
		ScopeMetrics: []*metricsv1.ScopeMetrics{{Metrics: []*metricsv1.Metric{{
			Name: "cpu", Data: &metricsv1.Metric_Gauge{Gauge: &metricsv1.Gauge{}},
		}}}},
	}}}
	raw, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	// ResourceMetrics.scope_metrics = 2, ScopeMetrics.metrics = 2, Metric.name = 1, Metric.gauge = 5
	m := field(t, field(t, field(t, raw, 1), 2), 2)
	if string(field(t, m, 1)) != "cpu" {
		t.Fatal("name")
	}
	field(t, m, 5)
}

func TestResponsesDecode(t *testing.T) {
	// ExportLogsServiceResponse.partial_success = 1 { rejected_log_records = 1, error_message = 2 }
	raw, err := proto.Marshal(&collogsv1.ExportLogsServiceResponse{
		PartialSuccess: &collogsv1.ExportLogsPartialSuccess{RejectedLogRecords: 3},
	})
	if err != nil || len(field(t, raw, 1)) == 0 {
		t.Fatalf("logs response: %v", err)
	}
	raw, err = proto.Marshal(&colmetricsv1.ExportMetricsServiceResponse{
		PartialSuccess: &colmetricsv1.ExportMetricsPartialSuccess{RejectedDataPoints: 2},
	})
	if err != nil || len(field(t, raw, 1)) == 0 {
		t.Fatalf("metrics response: %v", err)
	}
}
