package sim_test

import (
	"encoding/json"
	"io"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/boards/sim"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/metrics"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp/otlptest"
)

func metricsOf(b board.Board) capability.Provider {
	for _, p := range b.Capabilities() {
		if p.Descriptor().ID == "system.metrics" {
			return p
		}
	}
	return nil
}

// the system.metrics capability comes with a client, and reports from the first look at the capabilities
func TestMetricsCapability(t *testing.T) {
	plain := sim.New(func(string) string { return "1" }, t.TempDir(), nil)
	t.Cleanup(func() { _ = plain.(io.Closer).Close() })
	if metricsOf(plain) != nil {
		t.Fatal("metrics without a client")
	}
	srv := otlptest.New(t)
	client, err := otlp.NewClient(srv.URL, "cdd_tok", srv.Pool())
	if err != nil {
		t.Fatal(err)
	}
	env := telemetry.MetricsEnv{Client: client, App: metrics.NewApp(time.Now())}
	b := sim.New(func(string) string { return "1" }, t.TempDir(), nil, sim.WithMetrics(env))
	t.Cleanup(func() { _ = b.(io.Closer).Close() })
	c := metricsOf(b)
	if c == nil {
		t.Fatal("no metrics capability")
	}
	select {
	case s := <-c.State():
		var st map[string][]metrics.Proc
		if err := json.Unmarshal(s, &st); err != nil || st["topCpu"] == nil || st["topRss"] == nil {
			t.Fatalf("state %s", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no state")
	}
	if r := srv.Next(t); r.Path != otlp.MetricsPath {
		t.Fatalf("request to %s", r.Path)
	}
}
