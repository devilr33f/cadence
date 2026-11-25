package midi_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	midi "git.shino.boo/devilreef/cadence/device/boards/yandex-midi"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/metrics"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp/otlptest"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

func withMetrics(t *testing.T) (midi.Option, *metrics.App, *otlptest.Server) {
	t.Helper()
	srv := otlptest.New(t)
	client, err := otlp.NewClient(srv.URL, "cdd_tok", srv.Pool())
	if err != nil {
		t.Fatal(err)
	}
	app := metrics.NewApp(time.Now())
	return midi.WithMetrics(telemetry.MetricsEnv{Client: client, App: app}), app, srv
}

func wakeHits(app *metrics.App) uint64 {
	for _, p := range app.Points() {
		if p.Name == "cadence.wake_hits" {
			return p.Count
		}
	}
	return 0
}

// the system.metrics capability comes with a client and reads the board's /proc; the board's input counts into the
// app's counts
func TestMetricsCapability(t *testing.T) {
	plain, _ := newBoard(t, midiTree(t))
	if slices.Contains(ids(plain), "system.metrics") {
		t.Fatal("metrics without a client")
	}
	tr := midiTree(t)
	tr.write("proc/meminfo", "MemTotal: 2048 kB\nMemAvailable: 1024 kB\n")
	tr.write("proc/uptime", "100.00 50.00\n")
	tr.write("proc/1/stat", "1 (init) S 0 1 1 0 -1 0 0 0 0 0 300 200 0 0 20 0 1 0 1 1000 10 0 0 0 0 0 0 0 0 0 0 0 0 17\n")
	tr.write("proc/1/statm", "100 10 5 1 0 50 0\n")
	opt, app, srv := withMetrics(t)
	b, _ := newBoard(t, tr, opt)
	if got := ids(b); got[len(got)-1] != "system.metrics" {
		t.Fatalf("capabilities = %v", got)
	}
	if s := state(t, find(t, b, "system.metrics")); !strings.Contains(s, `"topRss":[{"pid":1,"comm":"init"`) {
		t.Fatalf("state = %s", s)
	}
	if r := srv.Next(t); r.Path != otlp.MetricsPath {
		t.Fatalf("request to %s", r.Path)
	}
	b.Input().InputEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_WakeEvent{WakeEvent: &audiodv1.WakeEvent{Engine: "stock"}}})
	if n := wakeHits(app); n != 1 {
		t.Fatalf("wake hits %d", n)
	}
}
