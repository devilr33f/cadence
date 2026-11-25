package telemetry_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/metrics"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp/otlptest"
	colmetricsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/metrics/v1"
)

// a fake /proc on disk, with one process whose command line holds a secret
func procDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, s := range map[string]string{
		"stat":       "cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 100 0 100 700 100 0 0 0 0 0\n",
		"loadavg":    "0.10 0.20 0.30 1/50 99\n",
		"meminfo":    "MemTotal: 2048 kB\nMemAvailable: 1024 kB\n",
		"uptime":     "100.00 50.00\n",
		"1/stat":     "1 (init) S 0 1 1 0 -1 4194560 100 0 0 0 300 200 0 0 20 0 1 0 1 1000000 10 0 0 0 0 0 0 0 0 0 0 0 0 17 0 0 0\n",
		"1/statm":    "100 10 5 1 0 50 0\n",
		"1/cmdline":  "/sbin/init\x00--key=sk_never_read\x00",
		"1/environ":  "TOKEN=sk_never_read\x00",
		"self/stat":  "not a process",
		"self/statm": "",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type metricsRig struct {
	m   *telemetry.Metrics
	srv *otlptest.Server
	app *metrics.App
}

func newMetrics(t *testing.T, playing func() bool) metricsRig {
	t.Helper()
	srv := otlptest.New(t)
	client, err := otlp.NewClient(srv.URL, "cdd_tok", srv.Pool())
	if err != nil {
		t.Fatal(err)
	}
	app := metrics.NewApp(time.Now())
	m := telemetry.NewMetrics(telemetry.MetricsOptions{
		Env: telemetry.MetricsEnv{Client: client, App: app}, Profile: board.TelemetryProfile{}, Proc: procDir(t),
		MediaPlaying: playing,
	})
	return metricsRig{m: m, srv: srv, app: app}
}

func (r metricsRig) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { r.m.Run(ctx) })
	t.Cleanup(func() { cancel(); wg.Wait() })
}

func decodeMetrics(t *testing.T, r otlptest.Request) map[string]bool {
	t.Helper()
	if r.Path != otlp.MetricsPath || r.Header.Get("Authorization") != "Bearer cdd_tok" {
		t.Fatalf("request %s %v", r.Path, r.Header)
	}
	var req colmetricsv1.ExportMetricsServiceRequest
	if err := proto.Unmarshal(r.Body, &req); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, m := range req.GetResourceMetrics()[0].GetScopeMetrics()[0].GetMetrics() {
		names[m.GetName()] = true
	}
	return names
}

func TestMetricsDescriptor(t *testing.T) {
	r := newMetrics(t, nil)
	d := r.m.Descriptor()
	if d.ID != "system.metrics" || d.Version != 1 || d.ConfigSchema != telemetry.SystemMetricsConfigSchema ||
		d.StateSchema != telemetry.SystemMetricsStateSchema || len(d.Commands) != 0 || len(d.Secrets) != 0 {
		t.Fatalf("descriptor %+v", d)
	}
	if _, err := r.m.Command(context.Background(), "x", nil); !errors.Is(err, capability.ErrUnsupported) {
		t.Fatalf("command: %v", err)
	}
}

// a whole config each time: intervalS from 10 to 300 s, 30 when left out; anything else is refused
func TestMetricsConfig(t *testing.T) {
	r := newMetrics(t, nil)
	for doc, valid := range map[string]bool{
		`{}`: true, `{"intervalS":10}`: true, `{"intervalS":300}`: true, ` {"intervalS":30.0} `: true,
		`{"intervalS":9}`: false, `{"intervalS":301}`: false, `{"intervalS":30.5}`: false, `{"intervalS":"30"}`: false,
		`{"intervalS":null}`: false, `{"other":1}`: false, `null`: false, `[]`: false, `{} {}`: false, ``: false,
		`{"intervalS":1e400}`: false,
	} {
		err := r.m.Apply(context.Background(), 1, json.RawMessage(doc))
		var ae *capability.ApplyError
		if valid && err != nil || !valid && (!errors.As(err, &ae) || ae.Code != "invalid") {
			t.Errorf("%s: %v", doc, err)
		}
	}
}

// a round at once, then one each interval: the request has the system's metrics and cadenced's counts, the state the
// top processes, and neither the command line nor the environment of any
func TestMetricsExportAndState(t *testing.T) {
	r := newMetrics(t, func() bool { return true })
	r.app.WakeHit()
	r.run(t)
	names := decodeMetrics(t, r.srv.Next(t))
	for _, want := range []string{"system.memory.limit", "system.cpu.load_average.1m", "cadence.media_playing", "cadence.wake_hits"} {
		if !names[want] {
			t.Errorf("no %s in %v", want, names)
		}
	}
	var st string
	select {
	case b := <-r.m.State():
		st = string(b)
	case <-time.After(5 * time.Second):
		t.Fatal("no state")
	}
	check(t, compile(t, telemetry.SystemMetricsStateSchema), st, true)
	if !strings.Contains(st, `"topRss":[{"pid":1,"comm":"init","cpu":5,"rss":`) || strings.Contains(st, "sk_never_read") {
		t.Fatalf("state %s", st)
	}
}

// no media gauge without media
func TestMetricsWithoutMedia(t *testing.T) {
	r := newMetrics(t, nil)
	r.run(t)
	if names := decodeMetrics(t, r.srv.Next(t)); names["cadence.media_playing"] || !names["system.memory.limit"] {
		t.Fatalf("names %v", names)
	}
}

// a new interval takes effect at once: the next round is due from the last one
func TestMetricsIntervalAppliedLive(t *testing.T) {
	r := newMetrics(t, nil)
	// 300 is 3 s, 10 is 100 ms
	telemetry.SetMetricsUnit(r.m, 10*time.Millisecond)
	if err := r.m.Apply(context.Background(), 1, json.RawMessage(`{"intervalS":300}`)); err != nil {
		t.Fatal(err)
	}
	r.run(t)
	r.srv.Next(t)
	r.srv.None(t, 500*time.Millisecond)
	if err := r.m.Apply(context.Background(), 2, json.RawMessage(`{"intervalS":10}`)); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	for range 3 {
		r.srv.Next(t)
	}
	// three rounds 100 ms apart, where the old interval had its next round 2.5 s away
	if d := time.Since(began); d > 1500*time.Millisecond {
		t.Fatalf("three rounds took %v", d)
	}
}
