package telemetry_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

func (r *adminRig) scrape() map[string]*dto.MetricFamily {
	r.t.Helper()
	st, h, body := r.get("/api/v1/metrics", adminToken)
	if st != 200 || !strings.HasPrefix(h.Get("Content-Type"), "text/plain") {
		r.t.Fatalf("%d %s %s", st, h.Get("Content-Type"), body)
	}
	p := expfmt.NewTextParser(model.LegacyValidation)
	fams, err := p.TextToMetricFamilies(strings.NewReader(body))
	if err != nil {
		r.t.Fatalf("%v\n%s", err, body)
	}
	return fams
}

func labels(m *dto.Metric) map[string]string {
	out := map[string]string{}
	for _, l := range m.GetLabel() {
		out[l.GetName()] = l.GetValue()
	}
	return out
}

// sample is the value of the family's one metric whose labels include want
func sample(t *testing.T, fams map[string]*dto.MetricFamily, name string, want map[string]string) (float64, map[string]string) {
	t.Helper()
	f := fams[name]
	if f == nil {
		t.Fatalf("no family %s", name)
	}
	var found []*dto.Metric
	for _, m := range f.GetMetric() {
		got := labels(m)
		ok := true
		for k, v := range want {
			if got[k] != v {
				ok = false
			}
		}
		if ok {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s %v: %d metrics", name, want, len(found))
	}
	m := found[0]
	switch f.GetType() {
	case dto.MetricType_COUNTER:
		return m.GetCounter().GetValue(), labels(m)
	case dto.MetricType_GAUGE:
		return m.GetGauge().GetValue(), labels(m)
	}
	t.Fatalf("%s is a %v", name, f.GetType())
	return 0, nil
}

func TestPrometheusDeviceSeries(t *testing.T) {
	r := newAdminRig(t)
	now := start
	long := strings.Repeat("é", 100) // 200 bytes
	r.insertPoints(
		point(r.devA, "system.cpu.utilization", telemetry.KindGauge, `{"cpu":"0"}`, now.Add(-5*time.Minute), 0.1),
		point(r.devA, "system.cpu.utilization", telemetry.KindGauge, `{"cpu":"0"}`, now.Add(-time.Minute), 0.25),
		point(r.devA, "system.cpu.utilization", telemetry.KindGauge, `{"cpu":"1"}`, now.Add(-time.Minute), 0.5),
		point(r.devA, "cadence.sessions", telemetry.KindSum, `{"trigger":"wake"}`, now.Add(-time.Minute), 3),
		point(r.devA, "cadence.wake-hits/x", telemetry.KindSum, `{}`, now.Add(-time.Minute), 7),
		point(r.devA, "cadence.sessions_total", telemetry.KindSum, `{}`, now.Add(-time.Minute), 1),
		// sanitized label names, our labels kept, values cut to 128 bytes on a rune
		point(r.devA, "system.filesystem.free", telemetry.KindGauge,
			`{"mount.point":"/data","device_id":"forged","9lives":true,"__name__":"x","n":1.5,"long":"`+long+`"}`, now.Add(-time.Minute), 2048),
		// a gauge older than 10 min is gone, whatever its device
		point(r.devB, "system.memory.used", telemetry.KindGauge, `{}`, now.Add(-11*time.Minute), 1024),
		point(r.devB, "system.memory.limit", telemetry.KindGauge, `{}`, now.Add(-9*time.Minute), 4096),
	)
	fams := r.scrape()

	devA := map[string]string{"device_id": r.devA.String(), "device_name": "kitchen", "board": "yandex-pickle"}
	with := func(extra ...string) map[string]string {
		out := map[string]string{}
		for k, v := range devA {
			out[k] = v
		}
		for i := 0; i < len(extra); i += 2 {
			out[extra[i]] = extra[i+1]
		}
		return out
	}
	if v, _ := sample(t, fams, "cadence_system_cpu_utilization", with("cpu", "0")); v != 0.25 {
		t.Fatalf("latest sample %v", v)
	}
	if v, _ := sample(t, fams, "cadence_system_cpu_utilization", with("cpu", "1")); v != 0.5 {
		t.Fatalf("cpu 1 %v", v)
	}
	if f := fams["cadence_system_cpu_utilization"]; f.GetType() != dto.MetricType_GAUGE {
		t.Fatalf("type %v", f.GetType())
	}
	// a name of cadence's own is not prefixed again
	if v, l := sample(t, fams, "cadence_sessions_total", with("trigger", "wake")); v != 3 || len(l) != 4 {
		t.Fatalf("sum %v %v", v, l)
	}
	if f := fams["cadence_sessions_total"]; f.GetType() != dto.MetricType_COUNTER {
		t.Fatalf("type %v", f.GetType())
	}
	if v, _ := sample(t, fams, "cadence_wake_hits_x_total", devA); v != 7 {
		t.Fatalf("sanitized name %v", v)
	}
	// a sum named _total gets no second one: its family is the one above
	if v, _ := sample(t, fams, "cadence_sessions_total", with("trigger", "")); v != 1 {
		t.Fatalf("_total sum %v", v)
	}
	_, l := sample(t, fams, "cadence_system_filesystem_free", devA)
	if l["mount_point"] != "/data" || l["device_id"] != r.devA.String() || l["_9lives"] != "true" || l["n"] != "1.5" ||
		l["long"] != strings.Repeat("é", 64) || len(l) != 7 {
		t.Fatalf("labels %v", l)
	}
	if _, ok := fams["cadence_system_memory_used"]; ok {
		t.Fatal("a stale series was served")
	}
	if v, l := sample(t, fams, "cadence_system_memory_limit", map[string]string{"device_name": "office"}); v != 4096 || l["board"] != "sim" {
		t.Fatalf("device b %v %v", v, l)
	}
	// the server's own process
	for _, name := range []string{"go_goroutines", "cadence_server_scrape_store_up"} {
		if _, ok := fams[name]; !ok {
			t.Fatalf("no %s", name)
		}
	}
	if v, _ := sample(t, fams, "cadence_server_scrape_store_up", nil); v != 1 {
		t.Fatalf("store up %v", v)
	}
	for _, reason := range []string{"reserved", "duplicate", "invalid"} {
		if v, _ := sample(t, fams, "cadence_server_scrape_series_omitted", map[string]string{"reason": reason}); v != 0 {
			t.Fatalf("omitted %s %v", reason, v)
		}
	}

	if st, _, _ := r.get("/api/v1/metrics", ""); st != 401 {
		t.Fatalf("no token: %d", st)
	}
	if st, _, _ := r.get("/api/v1/metrics", "cdn_wrong"); st != 401 {
		t.Fatalf("wrong token: %d", st)
	}
}

// the exposition takes an admin token or one of scope metrics (M7a-D6); ha
// is refused, and the metrics scope reaches no telemetry operation
func TestPrometheusScopes(t *testing.T) {
	r := newAdminRig(t)
	for tok, want := range map[string]int{adminToken: 200, metricsToken: 200, haToken: 403, "": 401} {
		st, h, body := r.get("/api/v1/metrics", tok)
		if st != want {
			t.Errorf("token %q: %d %s", tok, st, body)
		}
		if st == 200 && !strings.HasPrefix(h.Get("Content-Type"), "text/plain") {
			t.Errorf("token %q: content type %s", tok, h.Get("Content-Type"))
		}
		if st == 403 && body != `{"error":"forbidden"}` {
			t.Errorf("token %q: body %s", tok, body)
		}
	}
	id := r.devA.String()
	for _, path := range []string{"/api/v1/logs", "/api/v1/devices/" + id + "/metrics?name=x", "/api/v1/devices/" + id + "/processes"} {
		for _, tok := range []string{metricsToken, haToken} {
			if st, _, body := r.get(path, tok); st != 403 {
				t.Errorf("%s with %s: %d %s", path, tok, st, body)
			}
		}
	}
}

// a device cannot break the scrape for the others, nor pass for the server
func TestPrometheusHostileSeries(t *testing.T) {
	r := newAdminRig(t)
	now := start.Add(-time.Minute)
	r.insertPoints(
		// would be the server's own ingest counter
		point(r.devA, "otlp.ingest.accepted", telemetry.KindSum, `{"signal":"logs"}`, now, 1e9),
		point(r.devA, "server_log_dropped", telemetry.KindSum, `{}`, now, 5),
		// named as cadence's own, it gets no second prefix: still the server's
		point(r.devA, "cadence.otlp.ingest.rejected", telemetry.KindSum, `{"signal":"logs"}`, now, 1e9),
		// a sum and a gauge whose names meet at _total, on one device and across
		// two either way round: a gauge never ends in _total, so neither loses
		point(r.devA, "x", telemetry.KindSum, `{}`, now, 1),
		point(r.devA, "x_total", telemetry.KindGauge, `{}`, now, 2),
		point(r.devA, "z_total", telemetry.KindGauge, `{}`, now, 3),
		point(r.devB, "z", telemetry.KindSum, `{}`, now, 4),
		point(r.devA, "w", telemetry.KindSum, `{}`, now, 5),
		point(r.devB, "w_total", telemetry.KindGauge, `{}`, now, 6),
		// two attributes that sanitize alike: one series, the newer
		point(r.devA, "y", telemetry.KindGauge, `{"a.b":"1"}`, now.Add(-time.Second), 1),
		point(r.devA, "y", telemetry.KindGauge, `{"a-b":"1"}`, now, 2),
		point(r.devB, "y", telemetry.KindGauge, `{"a.b":"1"}`, now, 3),
	)
	fams := r.scrape()
	for _, name := range []string{"cadence_otlp_ingest_accepted_total", "cadence_otlp_ingest_rejected_total"} {
		if v, _ := sample(t, fams, name, map[string]string{"signal": "logs"}); v != 0 {
			t.Fatalf("a device's value passed for the server's %s: %v", name, v)
		}
	}
	for _, c := range []struct {
		name, device string
		typ          dto.MetricType
		want         float64
	}{
		{"cadence_x_total", "kitchen", dto.MetricType_COUNTER, 1}, {"cadence_x_total_gauge", "kitchen", dto.MetricType_GAUGE, 2},
		{"cadence_z_total_gauge", "kitchen", dto.MetricType_GAUGE, 3}, {"cadence_z_total", "office", dto.MetricType_COUNTER, 4},
		{"cadence_w_total", "kitchen", dto.MetricType_COUNTER, 5}, {"cadence_w_total_gauge", "office", dto.MetricType_GAUGE, 6},
	} {
		if v, _ := sample(t, fams, c.name, map[string]string{"device_name": c.device}); v != c.want || fams[c.name].GetType() != c.typ {
			t.Errorf("%s of %s: %v %v", c.name, c.device, v, fams[c.name].GetType())
		}
	}
	if v, _ := sample(t, fams, "cadence_y", map[string]string{"device_name": "kitchen"}); v != 2 {
		t.Fatalf("y %v", v)
	}
	if v, _ := sample(t, fams, "cadence_y", map[string]string{"device_name": "office"}); v != 3 {
		t.Fatalf("y of b %v", v)
	}
	for reason, want := range map[string]float64{"reserved": 3, "duplicate": 1} {
		if v, _ := sample(t, fams, "cadence_server_scrape_series_omitted", map[string]string{"reason": reason}); v != want {
			t.Errorf("omitted %s %v, want %v", reason, v, want)
		}
	}
}

func TestPrometheusSeriesCappedPerDevice(t *testing.T) {
	r := newAdminRig(t)
	rows := make([]telemetry.MetricRow, 1001)
	for i := range rows {
		rows[i] = point(r.devA, fmt.Sprintf("many.n%04d", i), telemetry.KindGauge, `{}`, start.Add(-time.Minute), 1)
	}
	r.insertPoints(append(rows, point(r.devB, "few", telemetry.KindGauge, `{}`, start.Add(-time.Minute), 1))...)
	fams := r.scrape()
	n := 0
	for name := range fams {
		if strings.HasPrefix(name, "cadence_many_") {
			n++
		}
	}
	if n != 1000 {
		t.Fatalf("%d series of the device", n)
	}
	if _, ok := fams["cadence_many_n1000"]; ok {
		t.Fatal("the last by name was kept")
	}
	if _, ok := fams["cadence_few"]; !ok {
		t.Fatal("the other device lost its series")
	}
	if v, _ := sample(t, fams, "cadence_server_scrape_devices_capped", nil); v != 1 {
		t.Fatalf("capped %v", v)
	}
}

// with the store down the server's own metrics still serve, and say so
func TestPrometheusStoreDown(t *testing.T) {
	r := newAdminRig(t)
	r.mu.Lock()
	r.series = func(context.Context, time.Time, int) ([]telemetry.Series, error) { return nil, errDown }
	r.mu.Unlock()
	fams := r.scrape()
	if v, _ := sample(t, fams, "cadence_server_scrape_store_up", nil); v != 0 {
		t.Fatalf("store up %v", v)
	}
	if _, ok := fams["go_goroutines"]; !ok {
		t.Fatal("no process metrics")
	}
	if out := r.out.String(); !strings.Contains(out, `"msg":"metrics_scrape_failed"`) || strings.Contains(out, "hunter2") {
		t.Fatalf("log %s", out)
	}
}

func TestPrometheusServerCounters(t *testing.T) {
	ing := newRig(t)
	ing.send(logsPath, tokenA, logsRequest(
		record(start, 9, "cadenced", "one"), record(start, 9, "cadenced", "two"), record(start, 9, "", "no source"),
	))
	ing.post(logsPath, []byte("x"), map[string]string{"Content-Type": "text/plain", "Authorization": "Bearer " + tokenA})
	ing.send(metricsPath, tokenA, metricsRequest(gauge("g", dbl(start, 1), dbl(start, 2))))
	for range 9 {
		ing.post(logsPath, sized(1<<20), gzipped(tokenB))
	}
	self := telemetry.NewSelfLog(nil, nil)
	r := newAdminRig(t, func(_ *adminRig, d *telemetry.PromDeps) {
		d.Ingest, d.SelfDropped = ing.in, self.Dropped
		d.Export = telemetry.NewExporter(telemetry.ExportDeps{})
	})
	fams := r.scrape()
	for _, c := range []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{"cadence_otlp_ingest_accepted_total", map[string]string{"signal": "logs"}, 2 + 8},
		{"cadence_otlp_ingest_rejected_total", map[string]string{"signal": "logs"}, 1},
		{"cadence_otlp_ingest_accepted_total", map[string]string{"signal": "metrics"}, 2},
		{"cadence_otlp_ingest_rejected_total", map[string]string{"signal": "metrics"}, 0},
		{"cadence_otlp_ingest_rate_limited_total", map[string]string{"signal": "logs"}, 1},
		{"cadence_otlp_ingest_busy_total", map[string]string{"signal": "logs"}, 0},
		{"cadence_otlp_ingest_failed_total", map[string]string{"signal": "logs", "code": "content_type"}, 1},
		{"cadence_otlp_ingest_failed_total", map[string]string{"signal": "metrics", "code": "content_type"}, 0},
		{"cadence_otlp_export_dropped_total", map[string]string{"signal": "logs"}, 0},
		{"cadence_otlp_export_sent_total", map[string]string{"signal": "metrics"}, 0},
		{"cadence_otlp_export_failures_total", nil, 0},
		{"cadence_otlp_export_queued", nil, 0},
		{"cadence_server_log_dropped_total", nil, 0},
	} {
		if v, _ := sample(t, fams, c.name, c.labels); v != c.want {
			t.Errorf("%s %v: %v, want %v", c.name, c.labels, v, c.want)
		}
	}
}
