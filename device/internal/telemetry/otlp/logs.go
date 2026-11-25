package otlp

import (
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	commonv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/common/v1"
	resourcev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/resource/v1"
)

const (
	LogsPath = "/v2/otlp/v1/logs"
	// ServiceName is the one resource attribute a device sends: the server stamps the rest
	ServiceName = "cadenced"
	ScopeName   = "cadence.device"
)

// field numbers of ExportLogsServiceRequest.resource_logs, ResourceLogs.resource and .scope_logs,
// ScopeLogs.scope and .log_records
const (
	fieldResourceLogs protowire.Number = 1
	fieldResource     protowire.Number = 1
	fieldScopeLogs    protowire.Number = 2
	fieldScope        protowire.Number = 1
	fieldLogRecords   protowire.Number = 2
)

var (
	resourceBytes = mustMarshal(&resourcev1.Resource{Attributes: []*commonv1.KeyValue{{
		Key: "service.name", Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: ServiceName}},
	}}})
	scopeBytes = mustMarshal(&commonv1.InstrumentationScope{Name: ScopeName})
)

func mustMarshal(m proto.Message) []byte {
	b, err := proto.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

// LogsRequest frames LogRecords, each already encoded, as one
// ExportLogsServiceRequest: one resource with service.name alone, one scope,
// the records in order. Framing the bytes saves decoding what the ring holds.
func LogsRequest(records [][]byte) []byte { return frame(resourceBytes, scopeBytes, records) }

// frame is one request of one resource, one scope and its records, each
// already encoded: logs and traces share the field numbers (resource_logs and
// resource_spans 1; resource 1, scope_logs and scope_spans 2; scope 1,
// log_records and spans 2).
func frame(resourceBytes, scopeBytes []byte, records [][]byte) []byte {
	scopeLogs := field(fieldScope, len(scopeBytes))
	for _, r := range records {
		scopeLogs += field(fieldLogRecords, len(r))
	}
	resourceLogs := field(fieldResource, len(resourceBytes)) + field(fieldScopeLogs, scopeLogs)
	b := make([]byte, 0, field(fieldResourceLogs, resourceLogs))
	b = header(b, fieldResourceLogs, resourceLogs)
	b = protowire.AppendBytes(protowire.AppendTag(b, fieldResource, protowire.BytesType), resourceBytes)
	b = header(b, fieldScopeLogs, scopeLogs)
	b = protowire.AppendBytes(protowire.AppendTag(b, fieldScope, protowire.BytesType), scopeBytes)
	for _, r := range records {
		b = protowire.AppendBytes(protowire.AppendTag(b, fieldLogRecords, protowire.BytesType), r)
	}
	return b
}

// field is the size of a length-delimited field with n bytes of payload.
func field(num protowire.Number, n int) int { return protowire.SizeTag(num) + protowire.SizeBytes(n) }

// header appends the tag and length of a length-delimited field whose n bytes follow.
func header(b []byte, num protowire.Number, n int) []byte {
	return protowire.AppendVarint(protowire.AppendTag(b, num, protowire.BytesType), uint64(n)) //nolint:gosec // a length
}
