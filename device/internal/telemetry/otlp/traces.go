package otlp

import (
	commonv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/common/v1"
	resourcev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/resource/v1"
)

const (
	TracesPath = "/v2/otlp/v1/traces"
	// TraceScope is the scope of every device span; the server shows it as "device"
	TraceScope = "cadence/device"
)

var traceScopeBytes = mustMarshal(&commonv1.InstrumentationScope{Name: TraceScope})

// TraceResource is the resource a device's spans name: service.name, and
// service.version when known; the server stamps the device itself.
func TraceResource(version string) []byte {
	attrs := []*commonv1.KeyValue{StringAttr("service.name", ServiceName)}
	if version != "" {
		attrs = append(attrs, StringAttr("service.version", version))
	}
	return mustMarshal(&resourcev1.Resource{Attributes: attrs})
}

// TracesRequest frames Spans, each already encoded, as one
// ExportTraceServiceRequest of resource (TraceResource) and one scope.
func TracesRequest(resource []byte, spans [][]byte) []byte {
	return frame(resource, traceScopeBytes, spans)
}
