package telemetry_test

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	colmetricsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/metrics/v1"
	commonv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/common/v1"
	metricsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/metrics/v1"
	resourcev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/resource/v1"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

const metricsPath = "/v2/otlp/v1/metrics"

func metricsRequest(ms ...*metricsv1.Metric) *colmetricsv1.ExportMetricsServiceRequest {
	return &colmetricsv1.ExportMetricsServiceRequest{ResourceMetrics: []*metricsv1.ResourceMetrics{{
		Resource:     &resourcev1.Resource{Attributes: []*commonv1.KeyValue{kv("device.id", str("forged"))}},
		ScopeMetrics: []*metricsv1.ScopeMetrics{{Scope: &commonv1.InstrumentationScope{Name: "cadence.device"}, Metrics: ms}},
	}}}
}

func gauge(name string, ps ...*metricsv1.NumberDataPoint) *metricsv1.Metric {
	return &metricsv1.Metric{Name: name, Data: &metricsv1.Metric_Gauge{Gauge: &metricsv1.Gauge{DataPoints: ps}}}
}

func sum(name string, t metricsv1.AggregationTemporality, ps ...*metricsv1.NumberDataPoint) *metricsv1.Metric {
	return &metricsv1.Metric{Name: name, Data: &metricsv1.Metric_Sum{Sum: &metricsv1.Sum{DataPoints: ps, IsMonotonic: true, AggregationTemporality: t}}}
}

func dbl(at time.Time, v float64, attrs ...*commonv1.KeyValue) *metricsv1.NumberDataPoint {
	return &metricsv1.NumberDataPoint{TimeUnixNano: unixNano(at), Value: &metricsv1.NumberDataPoint_AsDouble{AsDouble: v}, Attributes: attrs}
}

func count(from, at time.Time, n int64, attrs ...*commonv1.KeyValue) *metricsv1.NumberDataPoint {
	p := &metricsv1.NumberDataPoint{TimeUnixNano: unixNano(at), Value: &metricsv1.NumberDataPoint_AsInt{AsInt: n}, Attributes: attrs}
	if !from.IsZero() {
		p.StartTimeUnixNano = unixNano(from)
	}
	return p
}

type storedPoint struct {
	device, name, attrs string
	kind                int16
	ts                  time.Time
	start               *time.Time
	value               float64
}

func (r *rig) storedPoints() []storedPoint {
	r.t.Helper()
	rows, err := r.pool.Query(context.Background(), `SELECT device_id::text, name, kind, attrs::text, ts, start_ts, value
		FROM metric_samples ORDER BY name, attrs::text, ts`)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var out []storedPoint
	for rows.Next() {
		var p storedPoint
		if err := rows.Scan(&p.device, &p.name, &p.kind, &p.attrs, &p.ts, &p.start, &p.value); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func upDown(name string, ps ...*metricsv1.NumberDataPoint) *metricsv1.Metric {
	return &metricsv1.Metric{Name: name, Data: &metricsv1.Metric_Sum{Sum: &metricsv1.Sum{DataPoints: ps,
		AggregationTemporality: metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE}}}
}

func at(t time.Time) *time.Time { return &t }

func rejectedPoints(t *testing.T, a answer) int64 {
	t.Helper()
	var resp colmetricsv1.ExportMetricsServiceResponse
	if a.status != 200 || proto.Unmarshal(a.body, &resp) != nil {
		t.Fatalf("%d", a.status)
	}
	if ps := resp.GetPartialSuccess(); ps != nil && ps.GetErrorMessage() != "some data points were rejected" {
		t.Fatalf("message %q", ps.GetErrorMessage())
	}
	return resp.GetPartialSuccess().GetRejectedDataPoints()
}

func TestMetricsStored(t *testing.T) {
	r := newRig(t)
	const cum = metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE
	now := start.Add(-time.Second)
	a := r.send(metricsPath, tokenA, metricsRequest(
		gauge("system.cpu.utilization", dbl(now, 0.25), dbl(now, 0.5, kv("cpu", str("0")))),
		// a metric's clock may be off by 5 minutes at most, else the receive time stands in
		gauge("system.memory.usage", dbl(time.Unix(1, 0), 1024, kv("state", str("used")))),
		gauge("system.load", dbl(start.Add(-4*time.Minute), 0.5), dbl(start.Add(6*time.Minute), 0.75)),
		gauge("cadence.media_playing", dbl(now, 1)),
		// the device's clock was set after it started: its start lies past the point's time
		sum("cadence.sessions", cum, count(start.Add(time.Hour), now, 3, kv("trigger", str("wake")))),
		sum("cadence.wake_hits", cum, count(start.Add(-time.Hour), now, -2,
			kv("on", &commonv1.AnyValue{Value: &commonv1.AnyValue_BoolValue{BoolValue: true}}),
			kv("n", &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: 7}}),
			kv("f", &commonv1.AnyValue{Value: &commonv1.AnyValue_DoubleValue{DoubleValue: 1.5}}))),
		sum("cadence.no_start", cum, count(time.Time{}, now, 1)),
		// a cumulative sum that can go down is a gauge
		upDown("cadence.level", count(start.Add(-time.Hour), now, 40)),
	))
	if rejectedPoints(t, a) != 0 || len(a.body) != 0 {
		t.Fatalf("rejected some: %q", a.body)
	}
	want := []storedPoint{
		{name: "cadence.level", kind: 1, attrs: `{}`, ts: now, value: 40},
		{name: "cadence.media_playing", kind: 1, attrs: `{}`, ts: now, value: 1},
		{name: "cadence.no_start", kind: 2, attrs: `{}`, ts: now, value: 1},
		{name: "cadence.sessions", kind: 2, attrs: `{"trigger": "wake"}`, ts: now, start: at(start.Add(time.Hour)), value: 3},
		{name: "cadence.wake_hits", kind: 2, attrs: `{"f": 1.5, "n": 7, "on": true}`, ts: now, start: at(start.Add(-time.Hour)), value: -2},
		{name: "system.cpu.utilization", kind: 1, attrs: `{}`, ts: now, value: 0.25},
		{name: "system.cpu.utilization", kind: 1, attrs: `{"cpu": "0"}`, ts: now, value: 0.5},
		{name: "system.load", kind: 1, attrs: `{}`, ts: start.Add(-4 * time.Minute), value: 0.5},
		{name: "system.load", kind: 1, attrs: `{}`, ts: start, value: 0.75},
		{name: "system.memory.usage", kind: 1, attrs: `{"state": "used"}`, ts: start, value: 1024},
	}
	got := r.storedPoints()
	if len(got) != len(want) {
		t.Fatalf("stored %+v", got)
	}
	for i, w := range want {
		g := got[i]
		startOK := (g.start == nil) == (w.start == nil) && (g.start == nil || g.start.Equal(*w.start))
		if g.device != r.devA || g.name != w.name || g.kind != w.kind || g.attrs != w.attrs || !g.ts.Equal(w.ts) || !startOK || g.value != w.value {
			t.Fatalf("point %d: %+v (start %v), want %+v (start %v)", i, g, g.start, w, w.start)
		}
	}
}

// attribute strings are stored as written: html characters unescaped
func TestPointAttrsKeepHTML(t *testing.T) {
	b, _ := proto.Marshal(dbl(start, 1, kv("k", str("<b>&'"))))
	if got, ok := telemetry.PointAttrs(b); !ok || got != `{"k":"<b>&'"}` {
		t.Fatalf("attrs %q %v", got, ok)
	}
}

func TestMetricsRejected(t *testing.T) {
	r := newRig(t)
	const cum = metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE
	at := start
	many := make([]*commonv1.KeyValue, 65)
	for i := range many {
		many[i] = kv(fmt.Sprintf("k%d", i), str("v"))
	}
	noValue := dbl(at, 1)
	noValue.Value = nil
	flagged := dbl(at, 1)
	flagged.Flags = 1
	hist := &metricsv1.Metric{Name: "h", Data: &metricsv1.Metric_Histogram{Histogram: &metricsv1.Histogram{
		DataPoints: []*metricsv1.HistogramDataPoint{{}, {}},
	}}}
	a := r.send(metricsPath, tokenA, metricsRequest(
		gauge("ok", dbl(at, 1, many[:64]...)),
		gauge("too many attrs", dbl(at, 1, many...)),
		gauge("long key", dbl(at, 1, kv(strings.Repeat("k", 129), str("v")))),
		gauge("long value", dbl(at, 1, kv("k", str(strings.Repeat("v", 129))))),
		gauge("longest", dbl(at, 1, kv(strings.Repeat("k", 128), str(strings.Repeat("v", 128))))),
		gauge("empty key", dbl(at, 1, kv("", str("v")))),
		gauge("nul", dbl(at, 1, kv("k", str("a\x00b")))),
		gauge("tab", dbl(at, 1, kv("k", str("a\tb")))),
		gauge("control key", dbl(at, 1, kv("k\x1f", str("v")))),
		gauge("array", dbl(at, 1, kv("k", &commonv1.AnyValue{Value: &commonv1.AnyValue_ArrayValue{ArrayValue: &commonv1.ArrayValue{}}}))),
		gauge("nan", dbl(at, math.NaN()), dbl(at, math.Inf(1))),
		gauge("no value", noValue, flagged),
		gauge(""+strings.Repeat("n", 256), dbl(at, 1)),
		sum("delta", metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA, count(at, at, 1)),
		sum("unspecified", metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_UNSPECIFIED, count(at, at, 1)),
		sum("cumulative", cum, count(at, at, 1)),
		hist,
		&metricsv1.Metric{Name: "no data"},
	))
	if n := rejectedPoints(t, a); n != 17 {
		t.Fatalf("rejected %d", n)
	}
	var names []string
	for _, p := range r.storedPoints() {
		names = append(names, p.name)
	}
	if strings.Join(names, ",") != "cumulative,longest,ok" {
		t.Fatalf("stored %v", names)
	}
}

// at most 2000 points of a request are stored; the rest are rejected
func TestMetricsPointCap(t *testing.T) {
	r := newRig(t)
	ps := make([]*metricsv1.NumberDataPoint, 2001)
	for i := range ps {
		ps[i] = dbl(start, float64(i), kv("i", str(fmt.Sprint(i))))
	}
	bad := dbl(start, math.NaN())
	a := r.send(metricsPath, tokenA, metricsRequest(gauge("first", bad), gauge("many", ps...)))
	if n := rejectedPoints(t, a); n != 2 {
		t.Fatalf("rejected %d", n)
	}
	var stored, last int
	_ = r.pool.QueryRow(t.Context(), `SELECT count(*), max(value)::int FROM metric_samples`).Scan(&stored, &last)
	if stored != 2000 || last != 1999 {
		t.Fatalf("stored %d up to %d", stored, last)
	}
}

func TestMetricsAuthAndLimits(t *testing.T) {
	r := newRig(t)
	b, _ := proto.Marshal(metricsRequest(gauge("g", dbl(start, 1))))
	if a := r.post(metricsPath, b, headers("cdd_unknown-token-0000000000000")); a.status != 401 || len(a.body) != 0 {
		t.Fatalf("auth: %d", a.status)
	}
	h := headers(tokenA)
	h["Content-Type"] = "application/json"
	if a := r.post(metricsPath, b, h); a.status != 415 {
		t.Fatalf("type: %d", a.status)
	}
	if a := r.post(metricsPath, []byte{0x0a, 0x02, 0x12}, headers(tokenA)); a.status != 400 {
		t.Fatalf("malformed: %d", a.status)
	}
	// logs and metrics draw on one bucket per device
	if a := r.post(metricsPath, gz(b), gzipped(tokenA)); a.status != 200 {
		t.Fatalf("small metrics: %d", a.status)
	}
	for range 8 {
		r.post(logsPath, sized(1<<20), gzipped(tokenA))
	}
	if a := r.post(metricsPath, gz(b), gzipped(tokenA)); a.status != 429 {
		t.Fatalf("shared bucket: %d", a.status)
	}
	if a := r.post(metricsPath, gz(b), gzipped(tokenB)); a.status != 200 {
		t.Fatalf("other device: %d", a.status)
	}
	if strings.Contains(r.logs.String(), "forged") {
		t.Fatal("an attribute reached the log")
	}
}
