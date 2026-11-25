package telemetry

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"google.golang.org/protobuf/proto"

	collogsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/logs/v1"
	colmetricsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/metrics/v1"
	commonv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/common/v1"
	logsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/logs/v1"
	metricsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/metrics/v1"
	resourcev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/resource/v1"
)

// the scope every exported line and point is under
const exportScope = "cadence"

func strValue(s string) *commonv1.AnyValue {
	return &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: s}}
}

func boolValue(b bool) *commonv1.AnyValue {
	return &commonv1.AnyValue{Value: &commonv1.AnyValue_BoolValue{BoolValue: b}}
}

func keyValue(k string, v *commonv1.AnyValue) *commonv1.KeyValue {
	return &commonv1.KeyValue{Key: k, Value: v}
}

// resource is the server's stamp (M6b-D8): the server's own lines have no
// device; a device the lookup did not know has its id alone.
func resource(device string, devices map[string]DeviceInfo) *resourcev1.Resource {
	if device == "" {
		return &resourcev1.Resource{Attributes: []*commonv1.KeyValue{keyValue("service.name", strValue("cadence-server"))}}
	}
	attrs := []*commonv1.KeyValue{keyValue("service.name", strValue("cadence-device")), keyValue("device.id", strValue(device))}
	if d, ok := devices[device]; ok {
		attrs = append(attrs, keyValue("device.name", strValue(d.Name)), keyValue("board", strValue(d.Board)))
	}
	return &resourcev1.Resource{Attributes: attrs}
}

func unixNanos(t time.Time) uint64 {
	if t.IsZero() || t.Before(time.Unix(0, 0)) {
		return 0
	}
	return uint64(t.UnixNano()) //nolint:gosec // after 1970, checked above
}

// groups keeps the order a device first shows in.
type groups[T any] struct {
	order []string
	by    map[string][]T
}

func (g *groups[T]) add(key string, v T) {
	if g.by == nil {
		g.by = map[string][]T{}
	}
	if _, ok := g.by[key]; !ok {
		g.order = append(g.order, key)
	}
	g.by[key] = append(g.by[key], v)
}

func encodeLogs(rows []LogRow, devices map[string]DeviceInfo) ([]byte, error) {
	var g groups[*logsv1.LogRecord]
	for _, r := range rows {
		attrs := []*commonv1.KeyValue{keyValue("source", strValue(r.Source))}
		if r.Truncated {
			attrs = append(attrs, keyValue("truncated", boolValue(true)))
		}
		if r.Continued {
			attrs = append(attrs, keyValue("continued", boolValue(true)))
		}
		device := ""
		if r.DeviceID.Valid {
			device = r.DeviceID.String()
		}
		g.add(device, &logsv1.LogRecord{
			TimeUnixNano: unixNanos(r.Time), ObservedTimeUnixNano: unixNanos(r.Observed),
			SeverityNumber: logsv1.SeverityNumber(r.Severity), Body: strValue(r.Body), Attributes: attrs,
		})
	}
	req := &collogsv1.ExportLogsServiceRequest{}
	for _, device := range g.order {
		req.ResourceLogs = append(req.ResourceLogs, &logsv1.ResourceLogs{
			Resource: resource(device, devices),
			ScopeLogs: []*logsv1.ScopeLogs{{
				Scope: &commonv1.InstrumentationScope{Name: exportScope}, LogRecords: g.by[device],
			}},
		})
	}
	return proto.Marshal(req)
}

// pointAttrs are the stored JSON attributes as OTLP ones: an integral number
// an int, any other a double
func pointAttrs(raw []byte) []*commonv1.KeyValue {
	attrs := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&attrs)
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]*commonv1.KeyValue, 0, len(keys))
	for _, k := range keys {
		var v *commonv1.AnyValue
		switch a := attrs[k].(type) {
		case string:
			v = strValue(a)
		case bool:
			v = boolValue(a)
		case json.Number:
			if n, err := a.Int64(); err == nil {
				v = &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: n}}
			} else if f, err := a.Float64(); err == nil {
				v = &commonv1.AnyValue{Value: &commonv1.AnyValue_DoubleValue{DoubleValue: f}}
			}
		}
		if v != nil {
			out = append(out, keyValue(k, v))
		}
	}
	return out
}

func encodeMetrics(rows []MetricRow, devices map[string]DeviceInfo) ([]byte, error) {
	type series struct {
		name string
		kind int16
	}
	byDevice := groups[MetricRow]{}
	for _, r := range rows {
		byDevice.add(r.DeviceID.String(), r)
	}
	req := &colmetricsv1.ExportMetricsServiceRequest{}
	for _, device := range byDevice.order {
		var bySeries groups[*metricsv1.NumberDataPoint]
		kinds := map[string]series{}
		for _, r := range byDevice.by[device] {
			key := strconv.Itoa(int(r.Kind)) + "/" + r.Name
			kinds[key] = series{r.Name, r.Kind}
			p := &metricsv1.NumberDataPoint{
				TimeUnixNano: unixNanos(r.Time), Value: &metricsv1.NumberDataPoint_AsDouble{AsDouble: r.Value},
				Attributes: pointAttrs(r.Attrs),
			}
			if r.Kind == KindSum {
				p.StartTimeUnixNano = unixNanos(r.Start)
			}
			bySeries.add(key, p)
		}
		var ms []*metricsv1.Metric
		for _, key := range bySeries.order {
			s, points := kinds[key], bySeries.by[key]
			m := &metricsv1.Metric{Name: s.name}
			if s.kind == KindSum {
				m.Data = &metricsv1.Metric_Sum{Sum: &metricsv1.Sum{DataPoints: points, IsMonotonic: true,
					AggregationTemporality: metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE}}
			} else {
				m.Data = &metricsv1.Metric_Gauge{Gauge: &metricsv1.Gauge{DataPoints: points}}
			}
			ms = append(ms, m)
		}
		req.ResourceMetrics = append(req.ResourceMetrics, &metricsv1.ResourceMetrics{
			Resource:     resource(device, devices),
			ScopeMetrics: []*metricsv1.ScopeMetrics{{Scope: &commonv1.InstrumentationScope{Name: exportScope}, Metrics: ms}},
		})
	}
	return proto.Marshal(req)
}
