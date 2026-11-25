package telemetry_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/proto"

	collogsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/logs/v1"
	colmetricsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/metrics/v1"
	commonv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/common/v1"
	metricsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/metrics/v1"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
	"git.shino.boo/devilreef/cadence/server/internal/providers/otlphttp"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

const collectorAuth = "Bearer collector-secret-0123456789"

type collected struct {
	at                 time.Time
	path, auth, coding string
	logs               *collogsv1.ExportLogsServiceRequest
	metrics            *colmetricsv1.ExportMetricsServiceRequest
}

// fakeCollector is an OTLP/HTTP collector over TLS that answers each
// request with status(); it decodes what it got
type fakeCollector struct {
	t   *testing.T
	srv *httptest.Server

	mu     sync.Mutex
	got    []collected
	status func() (int, string)
	hold   chan struct{}
	// location answers a 3xx
	location string
	notify   chan struct{}
}

func newFakeCollector(t *testing.T) *fakeCollector {
	t.Helper()
	c := &fakeCollector{t: t, status: func() (int, string) { return http.StatusOK, "" }, notify: make(chan struct{}, 1024)}
	c.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got := collected{at: time.Now(), path: r.URL.Path, auth: r.Header.Get("Authorization"), coding: r.Header.Get("Content-Encoding")}
		var body []byte
		if zr, err := gzip.NewReader(bytes.NewReader(raw)); err == nil {
			body, _ = io.ReadAll(zr)
		}
		switch r.URL.Path {
		case "/otlp/v1/logs":
			got.logs = &collogsv1.ExportLogsServiceRequest{}
			if proto.Unmarshal(body, got.logs) != nil {
				got.logs = nil
			}
		case "/otlp/v1/metrics":
			got.metrics = &colmetricsv1.ExportMetricsServiceRequest{}
			if proto.Unmarshal(body, got.metrics) != nil {
				got.metrics = nil
			}
		}
		c.mu.Lock()
		c.got = append(c.got, got)
		status, hold, location := c.status, c.hold, c.location
		c.mu.Unlock()
		c.notify <- struct{}{}
		if hold != nil {
			<-hold
		}
		code, retry := status()
		if retry != "" {
			w.Header().Set("Retry-After", retry)
		}
		if code >= 300 && code < 400 {
			w.Header().Set("Location", location)
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *fakeCollector) client(t *testing.T) *otlphttp.Client {
	t.Helper()
	cl, err := otlphttp.NewClient(otlphttp.ClientConfig{
		URL: c.srv.URL + "/otlp", Authorization: collectorAuth,
		HTTP: httpx.Client(c.srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs), Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return cl
}

func (c *fakeCollector) setStatus(fn func() (int, string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = fn
}

// wait blocks until the collector got n requests in all
func (c *fakeCollector) wait(n int) []collected {
	c.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		c.mu.Lock()
		got := append([]collected(nil), c.got...)
		c.mu.Unlock()
		if len(got) >= n {
			return got
		}
		select {
		case <-c.notify:
		case <-deadline:
			c.t.Fatalf("collector got %d requests, want %d", len(got), n)
		}
	}
}

func attrMap(kvs []*commonv1.KeyValue) map[string]any {
	out := map[string]any{}
	for _, kv := range kvs {
		switch v := kv.GetValue().GetValue().(type) {
		case *commonv1.AnyValue_StringValue:
			out[kv.GetKey()] = v.StringValue
		case *commonv1.AnyValue_BoolValue:
			out[kv.GetKey()] = v.BoolValue
		case *commonv1.AnyValue_IntValue:
			out[kv.GetKey()] = v.IntValue
		case *commonv1.AnyValue_DoubleValue:
			out[kv.GetKey()] = v.DoubleValue
		}
	}
	return out
}

var (
	exportDevA = pgtype.UUID{Bytes: [16]byte{0xa}, Valid: true}
	exportDevB = pgtype.UUID{Bytes: [16]byte{0xb}, Valid: true}
)

// waitStats blocks until the export's counts satisfy ok: a collector hears a
// request before its sender counts the answer
func waitStats(t *testing.T, e *telemetry.Exporter, ok func(telemetry.ExportStats) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok(e.Stats()) {
		if time.Now().After(deadline) {
			t.Fatalf("stats %+v", e.Stats())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func newExporter(t *testing.T, out *syncBuffer) *telemetry.Exporter {
	t.Helper()
	if out == nil {
		out = &syncBuffer{}
	}
	return telemetry.NewExporter(telemetry.ExportDeps{
		Devices: func(context.Context) ([]telemetry.DeviceInfo, error) {
			return []telemetry.DeviceInfo{{ID: exportDevA.String(), Name: "kitchen", Board: "yandex-pickle"}}, nil
		},
		Log: slog.New(slog.NewJSONHandler(out, nil)),
	})
}

func TestExportForwardsLogsAndMetrics(t *testing.T) {
	col := newFakeCollector(t)
	e := newExporter(t, nil)
	e.SetTarget(col.client(t))
	defer e.Run(context.Background())()

	e.Logs([]telemetry.LogRow{
		{Time: start, Observed: start.Add(time.Second), DeviceID: exportDevA, Source: "soloist", Severity: 13, Body: "a line", Truncated: true},
		{Time: start.Add(time.Second), Observed: start.Add(time.Second), Source: "server", Severity: 9, Body: `{"msg":"x"}`},
		// a device the lookup does not know is stamped with its id alone
		{Time: start, Observed: start, DeviceID: exportDevB, Source: "kmsg", Severity: 5, Body: "b line", Continued: true},
	})
	e.Metrics([]telemetry.MetricRow{
		{DeviceID: exportDevA, Name: "system.cpu.utilization", Kind: telemetry.KindGauge, Attrs: []byte(`{"cpu":"0","n":2,"x":1.5,"up":true}`), Time: start, Value: 0.25},
		{DeviceID: exportDevA, Name: "cadence.sessions", Kind: telemetry.KindSum, Attrs: []byte(`{"trigger":"wake"}`), Time: start, Start: start.Add(-time.Hour), Value: 3},
	})
	got := col.wait(2)
	var logs *collogsv1.ExportLogsServiceRequest
	var metrics *colmetricsv1.ExportMetricsServiceRequest
	for _, g := range got {
		if g.auth != collectorAuth || g.coding != "gzip" {
			t.Fatalf("request %s: auth %q coding %q", g.path, g.auth, g.coding)
		}
		if g.logs != nil {
			logs = g.logs
		}
		if g.metrics != nil {
			metrics = g.metrics
		}
	}
	if logs == nil || metrics == nil {
		t.Fatalf("got %+v", got)
	}

	type resLog struct {
		res     map[string]any
		records []map[string]any
	}
	var resources []resLog
	for _, rl := range logs.GetResourceLogs() {
		r := resLog{res: attrMap(rl.GetResource().GetAttributes())}
		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				rec := attrMap(lr.GetAttributes())
				rec["body"] = lr.GetBody().GetStringValue()
				rec["sev"] = int(lr.GetSeverityNumber())
				rec["ts"] = lr.GetTimeUnixNano()
				rec["observed"] = lr.GetObservedTimeUnixNano()
				r.records = append(r.records, rec)
			}
		}
		resources = append(resources, r)
	}
	if len(resources) != 3 {
		t.Fatalf("resources %+v", resources)
	}
	byService := map[string]resLog{}
	for _, r := range resources {
		key, _ := r.res["service.name"].(string)
		if id, ok := r.res["device.id"].(string); ok {
			key += "/" + id
		}
		byService[key] = r
	}
	a := byService["cadence-device/"+exportDevA.String()]
	if a.res["device.name"] != "kitchen" || a.res["board"] != "yandex-pickle" || len(a.records) != 1 {
		t.Fatalf("device a %+v", a)
	}
	if rec := a.records[0]; rec["body"] != "a line" || rec["source"] != "soloist" || rec["sev"] != 13 || rec["truncated"] != true ||
		rec["ts"] != unixNano(start) || rec["observed"] != unixNano(start.Add(time.Second)) {
		t.Fatalf("record %+v", rec)
	}
	b := byService["cadence-device/"+exportDevB.String()]
	if _, ok := b.res["device.name"]; ok || len(b.records) != 1 || b.records[0]["continued"] != true {
		t.Fatalf("device b %+v", b)
	}
	s := byService["cadence-server"]
	if len(s.res) != 1 || len(s.records) != 1 || s.records[0]["source"] != "server" || s.records[0]["body"] != `{"msg":"x"}` {
		t.Fatalf("server %+v", s)
	}

	rms := metrics.GetResourceMetrics()
	if len(rms) != 1 {
		t.Fatalf("resource metrics %d", len(rms))
	}
	if res := attrMap(rms[0].GetResource().GetAttributes()); res["service.name"] != "cadence-device" || res["device.id"] != exportDevA.String() ||
		res["device.name"] != "kitchen" || res["board"] != "yandex-pickle" {
		t.Fatalf("metrics resource %v", res)
	}
	ms := map[string]*metricsv1.Metric{}
	for _, sm := range rms[0].GetScopeMetrics() {
		for _, m := range sm.GetMetrics() {
			ms[m.GetName()] = m
		}
	}
	g := ms["system.cpu.utilization"].GetGauge().GetDataPoints()
	if len(g) != 1 || g[0].GetAsDouble() != 0.25 || g[0].GetTimeUnixNano() != unixNano(start) {
		t.Fatalf("gauge %v", g)
	}
	if attrs := attrMap(g[0].GetAttributes()); attrs["cpu"] != "0" || attrs["n"] != int64(2) || attrs["x"] != 1.5 || attrs["up"] != true {
		t.Fatalf("gauge attrs %v", attrs)
	}
	sm := ms["cadence.sessions"].GetSum()
	if !sm.GetIsMonotonic() || sm.GetAggregationTemporality() != metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE ||
		len(sm.GetDataPoints()) != 1 || sm.GetDataPoints()[0].GetAsDouble() != 3 ||
		sm.GetDataPoints()[0].GetStartTimeUnixNano() != unixNano(start.Add(-time.Hour)) {
		t.Fatalf("sum %v", sm)
	}
	waitStats(t, e, func(st telemetry.ExportStats) bool {
		return st.Logs.Sent == 1 && st.Metrics.Sent == 1 && st.Logs.Dropped == 0 && st.Queued == 0
	})
}

// a renamed device stamps the next batch with its new name, not after the
// lookup's five minutes
func TestExportStampsARenameAtOnce(t *testing.T) {
	col := newFakeCollector(t)
	var mu sync.Mutex
	name := "kitchen"
	e := telemetry.NewExporter(telemetry.ExportDeps{Devices: func(context.Context) ([]telemetry.DeviceInfo, error) {
		mu.Lock()
		defer mu.Unlock()
		return []telemetry.DeviceInfo{{ID: exportDevA.String(), Name: name, Board: "sim"}}, nil
	}})
	e.SetTarget(col.client(t))
	defer e.Run(context.Background())()
	stamped := func(n int) any {
		t.Helper()
		e.Logs([]telemetry.LogRow{{Time: start, Observed: start, DeviceID: exportDevA, Source: "kmsg", Body: "x"}})
		got := col.wait(n)
		return attrMap(got[n-1].logs.GetResourceLogs()[0].GetResource().GetAttributes())["device.name"]
	}
	if got := stamped(1); got != "kitchen" {
		t.Fatalf("first %v", got)
	}
	mu.Lock()
	name = "hall"
	mu.Unlock()
	if got := stamped(2); got != "kitchen" {
		t.Fatalf("the lookup is not kept: %v", got)
	}
	e.DevicesChanged()
	if got := stamped(3); got != "hall" {
		t.Fatalf("after the change %v", got)
	}
}

func TestExportOffQueuesNothing(t *testing.T) {
	e := newExporter(t, nil)
	e.Logs([]telemetry.LogRow{{Time: start, Observed: start, Source: "server", Body: "x"}})
	if st := e.Stats(); st.Queued != 0 || st.Logs.Dropped != 0 {
		t.Fatalf("stats %+v", st)
	}
	// turned off with batches waiting: they are dropped and counted
	col := newFakeCollector(t)
	e.SetTarget(col.client(t))
	e.Logs([]telemetry.LogRow{{Time: start, Observed: start, Source: "server", Body: "x"}})
	e.Metrics([]telemetry.MetricRow{{DeviceID: exportDevA, Name: "g", Kind: telemetry.KindGauge, Attrs: []byte(`{}`), Time: start}})
	e.SetTarget(nil)
	if st := e.Stats(); st.Queued != 0 || st.Logs.Dropped != 1 || st.Metrics.Dropped != 1 {
		t.Fatalf("stats %+v", st)
	}
}

// big inserts go out in bounded batches
func TestExportSplitsBigInserts(t *testing.T) {
	col := newFakeCollector(t)
	e := newExporter(t, nil)
	e.SetTarget(col.client(t))
	rows := make([]telemetry.LogRow, 2500)
	for i := range rows {
		rows[i] = telemetry.LogRow{Time: start, Observed: start, Source: "server", Body: "x"}
	}
	e.Logs(rows)
	if st := e.Stats(); st.Queued != 3 {
		t.Fatalf("2500 lines in %d batches", st.Queued)
	}
	big := make([]telemetry.LogRow, 300)
	for i := range big {
		big[i] = telemetry.LogRow{Time: start, Observed: start, Source: "server", Body: strings.Repeat("y", 4096)}
	}
	e.Logs(big)
	if st := e.Stats(); st.Queued != 3+3 {
		t.Fatalf("1.2 MiB of bodies in %d batches", st.Queued-3)
	}
	// points as well, by their attributes
	points := make([]telemetry.MetricRow, 300)
	for i := range points {
		points[i] = telemetry.MetricRow{DeviceID: exportDevA, Name: "g", Kind: telemetry.KindGauge, Time: start,
			Attrs: []byte(`{"k":"` + strings.Repeat("z", 4096) + `"}`)}
	}
	e.Metrics(points)
	if st := e.Stats(); st.Queued != 6+3 {
		t.Fatalf("1.2 MiB of attributes in %d batches", st.Queued-6)
	}
	defer e.Run(context.Background())()
	n := 0
	pointsGot := 0
	for _, g := range col.wait(9) {
		for _, rm := range g.metrics.GetResourceMetrics() {
			for _, sm := range rm.GetScopeMetrics() {
				for _, m := range sm.GetMetrics() {
					pointsGot += len(m.GetGauge().GetDataPoints())
				}
			}
		}
		for _, rl := range g.logs.GetResourceLogs() {
			for _, sl := range rl.GetScopeLogs() {
				n += len(sl.GetLogRecords())
			}
		}
	}
	if n != 2800 || pointsGot != 300 {
		t.Fatalf("collector got %d lines, %d points", n, pointsGot)
	}
}

// a collector that hangs never holds ingest up: past 64 waiting batches the
// oldest are dropped and counted
func TestExportNeverBlocksAndDropsPast64(t *testing.T) {
	col := newFakeCollector(t)
	release := make(chan struct{})
	col.mu.Lock()
	col.hold = release
	col.mu.Unlock()
	defer close(release)
	e := newExporter(t, nil)
	e.SetTarget(col.client(t))
	stop := e.Run(context.Background())
	defer stop()
	row := func(body string) []telemetry.LogRow {
		return []telemetry.LogRow{{Time: start, Observed: start, Source: "server", Body: body}}
	}
	e.Logs(row("in flight"))
	col.wait(1)
	began := time.Now()
	for range 100 {
		e.Logs(row("queued"))
		e.Metrics([]telemetry.MetricRow{{DeviceID: exportDevA, Name: "g", Kind: telemetry.KindGauge, Attrs: []byte(`{}`), Time: start}})
	}
	if took := time.Since(began); took > time.Second {
		t.Fatalf("200 batches took %v", took)
	}
	st := e.Stats()
	if st.Queued != 64 || st.Logs.Dropped != 68 || st.Metrics.Dropped != 68 {
		t.Fatalf("stats %+v", st)
	}
}

func TestExportBacksOffAndRecovers(t *testing.T) {
	col := newFakeCollector(t)
	var mu sync.Mutex
	failing := 4
	col.setStatus(func() (int, string) {
		mu.Lock()
		defer mu.Unlock()
		if failing > 0 {
			failing--
			return http.StatusServiceUnavailable, ""
		}
		return http.StatusOK, ""
	})
	out := &syncBuffer{}
	e := newExporter(t, out)
	telemetry.SetExportBackoff(e, 40*time.Millisecond, 160*time.Millisecond)
	e.SetTarget(col.client(t))
	defer e.Run(context.Background())()
	e.Logs([]telemetry.LogRow{{Time: start, Observed: start, Source: "server", Body: "first"}})
	e.Logs([]telemetry.LogRow{{Time: start, Observed: start, Source: "server", Body: "second"}})
	got := col.wait(6)
	// 40, 80, 160, 160 ms between the failed tries of the first batch
	for i, min := range []time.Duration{40, 80, 160, 160} {
		if gap := got[i+1].at.Sub(got[i].at); gap < min*time.Millisecond {
			t.Fatalf("try %d after %v, want %v at least", i+1, gap, min*time.Millisecond)
		}
	}
	for i, want := range []string{"first", "first", "first", "first", "first", "second"} {
		if body := got[i].logs.GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()[0].GetBody().GetStringValue(); body != want {
			t.Fatalf("request %d carried %q", i, body)
		}
	}
	// the second goes at once once the collector answers
	if gap := got[5].at.Sub(got[4].at); gap > 100*time.Millisecond {
		t.Fatalf("second after %v", gap)
	}
	waitStats(t, e, func(st telemetry.ExportStats) bool {
		return st.Logs.Sent == 2 && st.Failures == 4 && st.Logs.Dropped == 0
	})
	logged := out.String()
	if strings.Count(logged, `"msg":"otlp_export_failed"`) != 1 || strings.Count(logged, `"msg":"otlp_export_recovered"`) != 1 ||
		strings.Contains(logged, "collector-secret") || strings.Contains(logged, col.srv.URL) {
		t.Fatalf("log %s", logged)
	}
}

// a refused batch is dropped, the next goes; Retry-After is honoured
func TestExportDropsRefusedAndHonoursRetryAfter(t *testing.T) {
	col := newFakeCollector(t)
	var mu sync.Mutex
	answers := []struct {
		code  int
		retry string
	}{{http.StatusBadRequest, ""}, {http.StatusTooManyRequests, "1"}, {http.StatusOK, ""}}
	col.setStatus(func() (int, string) {
		mu.Lock()
		defer mu.Unlock()
		a := answers[0]
		if len(answers) > 1 {
			answers = answers[1:]
		}
		return a.code, a.retry
	})
	e := newExporter(t, nil)
	telemetry.SetExportBackoff(e, 10*time.Millisecond, 20*time.Millisecond)
	e.SetTarget(col.client(t))
	defer e.Run(context.Background())()
	e.Logs([]telemetry.LogRow{{Time: start, Observed: start, Source: "server", Body: "refused"}})
	e.Logs([]telemetry.LogRow{{Time: start, Observed: start, Source: "server", Body: "later"}})
	got := col.wait(3)
	body := func(i int) string {
		return got[i].logs.GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()[0].GetBody().GetStringValue()
	}
	if body(0) != "refused" || body(1) != "later" || body(2) != "later" {
		t.Fatalf("bodies %q %q %q", body(0), body(1), body(2))
	}
	if gap := got[2].at.Sub(got[1].at); gap < time.Second {
		t.Fatalf("retry after %v", gap)
	}
	waitStats(t, e, func(st telemetry.ExportStats) bool {
		return st.Logs.Dropped == 1 && st.Logs.Sent == 1 && st.Failures == 2
	})
}

// a redirect is refused: the header never reaches its target, the batch waits
func TestExportRefusesRedirect(t *testing.T) {
	other := newFakeCollector(t)
	col := newFakeCollector(t)
	col.setStatus(func() (int, string) { return http.StatusTemporaryRedirect, "" })
	col.mu.Lock()
	col.location = other.srv.URL + "/otlp/v1/logs"
	col.mu.Unlock()
	e := newExporter(t, nil)
	telemetry.SetExportBackoff(e, 10*time.Millisecond, 20*time.Millisecond)
	e.SetTarget(col.client(t))
	defer e.Run(context.Background())()
	e.Logs([]telemetry.LogRow{{Time: start, Observed: start, Source: "server", Body: "x"}})
	col.wait(3)
	other.mu.Lock()
	n := len(other.got)
	other.mu.Unlock()
	if n != 0 {
		t.Fatal("the redirect target was asked")
	}
	waitStats(t, e, func(st telemetry.ExportStats) bool {
		return st.Failures >= 3 && st.Logs.Dropped == 0 && st.Logs.Sent == 0
	})
}
