package metrics_test

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/metrics"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp/otlptest"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	colmetricsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/metrics/v1"
	commonv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/common/v1"
	metricsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/metrics/v1"
)

const testToken = "cdd_metrics-test-token"

func newExporter(t *testing.T) (*metrics.Exporter, *metrics.App, *otlptest.Server) {
	t.Helper()
	srv := otlptest.New(t)
	client, err := otlp.NewClient(srv.URL, testToken, srv.Pool())
	if err != nil {
		t.Fatal(err)
	}
	app := metrics.NewApp(t0)
	return metrics.NewExporter(client, app), app, srv
}

func decode(t *testing.T, r otlptest.Request) *colmetricsv1.ExportMetricsServiceRequest {
	t.Helper()
	if r.Path != otlp.MetricsPath || r.Header.Get("Authorization") != "Bearer "+testToken || r.BadGzip ||
		r.Header.Get("Content-Encoding") != "gzip" || r.Header.Get("Content-Type") != "application/x-protobuf" {
		t.Fatalf("request %s %v gzip ok %v", r.Path, r.Header, !r.BadGzip)
	}
	var m colmetricsv1.ExportMetricsServiceRequest
	if err := proto.Unmarshal(r.Body, &m); err != nil {
		t.Fatal(err)
	}
	return &m
}

func byName(t *testing.T, req *colmetricsv1.ExportMetricsServiceRequest) map[string]*metricsv1.Metric {
	t.Helper()
	rms := req.GetResourceMetrics()
	if len(rms) != 1 || len(rms[0].GetScopeMetrics()) != 1 {
		t.Fatalf("resource metrics %v", rms)
	}
	attrs := rms[0].GetResource().GetAttributes()
	if len(attrs) != 1 || attrs[0].GetKey() != "service.name" || attrs[0].GetValue().GetStringValue() != "cadenced" {
		t.Fatalf("resource %v", attrs)
	}
	sm := rms[0].GetScopeMetrics()[0]
	if sm.GetScope().GetName() != "cadence.device" {
		t.Fatalf("scope %v", sm.GetScope())
	}
	out := map[string]*metricsv1.Metric{}
	for _, m := range sm.GetMetrics() {
		if out[m.GetName()] != nil {
			t.Fatalf("%s twice", m.GetName())
		}
		out[m.GetName()] = m
	}
	return out
}

func kv(attrs []*commonv1.KeyValue) []metrics.Attr {
	var out []metrics.Attr
	for _, a := range attrs {
		out = append(out, metrics.Attr{Key: a.GetKey(), Value: a.GetValue().GetStringValue()})
	}
	return out
}

// intPoint is the value of sum m's point with attrs
func intPoint(t *testing.T, m *metricsv1.Metric, attrs ...metrics.Attr) *metricsv1.NumberDataPoint {
	t.Helper()
	s := m.GetSum()
	if s == nil || !s.GetIsMonotonic() || s.GetAggregationTemporality() != metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE {
		t.Fatalf("%s is no monotonic cumulative sum: %v", m.GetName(), m)
	}
	for _, p := range s.GetDataPoints() {
		if slices.Equal(kv(p.GetAttributes()), attrs) {
			if _, ok := p.GetValue().(*metricsv1.NumberDataPoint_AsInt); !ok {
				t.Fatalf("%s: no int", m.GetName())
			}
			return p
		}
	}
	t.Fatalf("%s: no point %v", m.GetName(), attrs)
	return nil
}

func round(at time.Time, app *metrics.App) metrics.Round {
	return metrics.Round{At: at, Points: append([]metrics.Point{
		{Name: "system.cpu.utilization", Kind: metrics.KindGauge, Value: 0.25},
		{Name: "system.cpu.utilization", Kind: metrics.KindGauge, Value: 0.5, Attrs: []metrics.Attr{attr("cpu", "0")}},
		{Name: "system.memory.used", Kind: metrics.KindGauge, Value: 1 << 20},
		{Name: "system.memory.limit", Kind: metrics.KindGauge, Value: 4 << 20},
		{Name: "system.filesystem.limit", Kind: metrics.KindGauge, Value: 8192, Attrs: []metrics.Attr{attr("mountpoint", "/data")}},
		{Name: "system.filesystem.free", Kind: metrics.KindGauge, Value: 4096, Attrs: []metrics.Attr{attr("mountpoint", "/data")}},
		{Name: "cadence.media_playing", Kind: metrics.KindGauge, Value: 1},
	}, app.Points()...)}
}

// one request per round: gauges of doubles at the round's time, cumulative sums of ints from cadenced's start, every
// point of a name in one metric, and service.name the only resource attribute
func TestExportRequest(t *testing.T) {
	e, app, srv := newExporter(t)
	app.WakeHit()
	app.SessionBegan(cadencev2.Trigger_TRIGGER_FOLLOW_UP)
	at := t0.Add(time.Minute)
	if !e.Export(context.Background(), round(at, app)) {
		t.Fatal("export failed")
	}
	ms := byName(t, decode(t, srv.Next(t)))
	cpu := ms["system.cpu.utilization"].GetGauge()
	if cpu == nil || len(cpu.GetDataPoints()) != 2 {
		t.Fatalf("cpu %v", ms["system.cpu.utilization"])
	}
	for i, want := range []struct {
		attrs []metrics.Attr
		v     float64
	}{{nil, 0.25}, {[]metrics.Attr{attr("cpu", "0")}, 0.5}} {
		p := cpu.GetDataPoints()[i]
		if !slices.Equal(kv(p.GetAttributes()), want.attrs) || p.GetAsDouble() != want.v ||
			p.GetTimeUnixNano() != uint64(at.UnixNano()) || p.GetStartTimeUnixNano() != 0 {
			t.Fatalf("cpu point %d: %v", i, p)
		}
	}
	for name, unit := range map[string]string{
		"system.cpu.utilization": "1", "system.memory.used": "By", "system.memory.limit": "By", "system.filesystem.limit": "By",
		"system.filesystem.free": "By", "cadence.media_playing": "1",
	} {
		if ms[name].GetUnit() != unit || ms[name].GetGauge() == nil {
			t.Errorf("%s: unit %q, gauge %v", name, ms[name].GetUnit(), ms[name].GetGauge())
		}
	}
	if got := kv(ms["system.filesystem.free"].GetGauge().GetDataPoints()[0].GetAttributes()); !slices.Equal(got, []metrics.Attr{attr("mountpoint", "/data")}) {
		t.Fatalf("mount attrs %v", got)
	}
	wake := intPoint(t, ms["cadence.wake_hits"])
	if wake.GetAsInt() != 1 || wake.GetStartTimeUnixNano() != uint64(t0.UnixNano()) || wake.GetTimeUnixNano() != uint64(at.UnixNano()) {
		t.Fatalf("wake hits %v", wake)
	}
	if p := intPoint(t, ms["cadence.sessions"], attr("trigger", "follow_up")); p.GetAsInt() != 1 {
		t.Fatalf("follow-ups %v", p)
	}
	if len(ms["cadence.sessions"].GetSum().GetDataPoints()) != 3 {
		t.Fatalf("sessions %v", ms["cadence.sessions"])
	}
}

// a batch that fails is dropped, not kept for later, and counted; the next round still carries every count since the
// start
func TestFailedBatchDroppedAndCounted(t *testing.T) {
	e, app, srv := newExporter(t)
	srv.Script(otlptest.Response{Status: http.StatusInternalServerError}, otlptest.Response{Drop: true},
		otlptest.Response{Status: http.StatusBadRequest})
	for range 3 {
		app.WakeHit()
	}
	at := t0
	for i := range 3 {
		at = at.Add(30 * time.Second)
		if e.Export(context.Background(), round(at, app)) {
			t.Fatalf("export %d succeeded", i)
		}
		srv.Next(t)
	}
	app.WakeHit()
	app.WakeHit()
	at = at.Add(30 * time.Second)
	if !e.Export(context.Background(), round(at, app)) {
		t.Fatal("export failed")
	}
	ms := byName(t, decode(t, srv.Next(t)))
	if n := intPoint(t, ms["cadence.metrics_dropped"]).GetAsInt(); n != 3 {
		t.Fatalf("dropped %d", n)
	}
	if n := intPoint(t, ms["cadence.wake_hits"]).GetAsInt(); n != 5 {
		t.Fatalf("wake hits %d", n)
	}
}

// a server that asks to wait gets nothing until then, and a little longer, so devices it held together do not all
// come back together: the rounds meanwhile are dropped and counted
func TestRetryAfterHoldsTheRounds(t *testing.T) {
	e, app, srv := newExporter(t)
	// the middle of the stretch: 10 % past the server's wait
	metrics.SetRand(e, func() float64 { return 0.5 })
	srv.Script(otlptest.Response{Status: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"20"}}})
	at := t0
	if e.Export(context.Background(), round(at, app)) {
		t.Fatal("a 429 succeeded")
	}
	srv.Next(t)
	for _, after := range []time.Duration{10 * time.Second, 21 * time.Second} {
		if e.Export(context.Background(), round(at.Add(after), app)) {
			t.Fatalf("posted while held, %v after", after)
		}
	}
	srv.None(t, 200*time.Millisecond)
	if !e.Export(context.Background(), round(at.Add(22*time.Second), app)) {
		t.Fatal("export failed")
	}
	ms := byName(t, decode(t, srv.Next(t)))
	if n := intPoint(t, ms["cadence.metrics_dropped"]).GetAsInt(); n != 3 {
		t.Fatalf("dropped %d", n)
	}
}
