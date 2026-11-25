package otlp

import (
	"time"

	colmetricsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/metrics/v1"
	commonv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/common/v1"
	metricsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/metrics/v1"
	resourcev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/resource/v1"
)

const MetricsPath = "/v2/otlp/v1/metrics"

// MetricsRequest is one ExportMetricsServiceRequest of ms: one resource with service.name alone, one scope.
func MetricsRequest(ms []*metricsv1.Metric) *colmetricsv1.ExportMetricsServiceRequest {
	return &colmetricsv1.ExportMetricsServiceRequest{ResourceMetrics: []*metricsv1.ResourceMetrics{{
		Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{{
			Key: "service.name", Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: ServiceName}},
		}}},
		ScopeMetrics: []*metricsv1.ScopeMetrics{{Scope: &commonv1.InstrumentationScope{Name: ScopeName}, Metrics: ms}},
	}}}
}

// Gauge is a metric of values as they stand.
func Gauge(name, unit string, points []*metricsv1.NumberDataPoint) *metricsv1.Metric {
	return &metricsv1.Metric{Name: name, Unit: unit, Data: &metricsv1.Metric_Gauge{Gauge: &metricsv1.Gauge{DataPoints: points}}}
}

// MonotonicSum is a metric of counts that only grow, each cumulative since its point's start.
func MonotonicSum(name, unit string, points []*metricsv1.NumberDataPoint) *metricsv1.Metric {
	return &metricsv1.Metric{Name: name, Unit: unit, Data: &metricsv1.Metric_Sum{Sum: &metricsv1.Sum{
		DataPoints: points, IsMonotonic: true,
		AggregationTemporality: metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
	}}}
}

// DoublePoint is v at t.
func DoublePoint(t time.Time, v float64, attrs []*commonv1.KeyValue) *metricsv1.NumberDataPoint {
	return &metricsv1.NumberDataPoint{Attributes: attrs, TimeUnixNano: unixNano(t), Value: &metricsv1.NumberDataPoint_AsDouble{AsDouble: v}}
}

// IntPoint is n counted from start to t.
func IntPoint(start, t time.Time, n int64, attrs []*commonv1.KeyValue) *metricsv1.NumberDataPoint {
	return &metricsv1.NumberDataPoint{
		Attributes: attrs, StartTimeUnixNano: unixNano(start), TimeUnixNano: unixNano(t),
		Value: &metricsv1.NumberDataPoint_AsInt{AsInt: n},
	}
}

// StringAttr is an attribute of a string value.
func StringAttr(key, value string) *commonv1.KeyValue {
	return &commonv1.KeyValue{Key: key, Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: value}}}
}

// a time before the epoch (an unset clock) is the epoch
func unixNano(t time.Time) uint64 {
	return uint64(max(t.UnixNano(), 0)) //nolint:gosec // never negative
}
