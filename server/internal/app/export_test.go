package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/otlphttp"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

type fakeExportTarget struct {
	mu  sync.Mutex
	set []telemetry.ExportClient
}

func (f *fakeExportTarget) SetTarget(c telemetry.ExportClient) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set = append(f.set, c)
}

func (f *fakeExportTarget) calls() []telemetry.ExportClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]telemetry.ExportClient(nil), f.set...)
}

type exportRig struct {
	mu       sync.Mutex
	name     string
	global   error
	built    map[string]*otlphttp.Client
	buildErr error
	target   *fakeExportTarget
	out      *bytes.Buffer
	deps     exportDeps
}

func newExportRig(t *testing.T) *exportRig {
	t.Helper()
	r := &exportRig{built: map[string]*otlphttp.Client{}, target: &fakeExportTarget{}, out: &bytes.Buffer{}}
	r.deps = exportDeps{
		Global: func(context.Context) (settings.Values, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			v := settings.Defaults()
			v.TelemetryExport = r.name
			return v, r.global
		},
		Build: func(_ context.Context, name string) (*otlphttp.Client, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.buildErr != nil {
				return nil, r.buildErr
			}
			if c, ok := r.built[name]; ok {
				return c, nil
			}
			return nil, providers.ErrNotFound
		},
		Target: r.target, Log: slog.New(slog.NewJSONHandler(r.out, nil)),
	}
	return r
}

func (r *exportRig) set(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn()
}

func otlpTestClient(t *testing.T, url string) *otlphttp.Client {
	t.Helper()
	c, err := otlphttp.NewClient(otlphttp.ClientConfig{URL: url, Authorization: "Bearer export-secret-0123"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestExportFollowsTheSettingAndInstance(t *testing.T) {
	r := newExportRig(t)
	ctx := context.Background()
	var cur exportState
	step := func() { cur = reconcileExport(ctx, r.deps, cur) }

	step()
	if n := len(r.target.calls()); n != 0 {
		t.Fatalf("unset: %d calls", n)
	}
	c1 := otlpTestClient(t, "https://otel.example/one")
	r.set(func() { r.name, r.built["otel"] = "otel", c1 })
	step()
	step()
	if calls := r.target.calls(); len(calls) != 1 || calls[0] != c1 {
		t.Fatalf("on: %v", calls)
	}
	// a store that cannot answer keeps what runs
	r.set(func() { r.buildErr = errors.New("db down: password=hunter2") })
	step()
	r.set(func() { r.buildErr, r.global = nil, errors.New("db down") })
	step()
	if len(r.target.calls()) != 1 {
		t.Fatal("a failed read changed the target")
	}
	// the instance deleted: off, logged once
	r.set(func() { r.global = nil; delete(r.built, "otel") })
	step()
	step()
	if calls := r.target.calls(); len(calls) != 2 || calls[1] != nil {
		t.Fatalf("deleted: %v", calls)
	}
	// created again: the new client
	c2 := otlpTestClient(t, "https://otel.example/two")
	r.set(func() { r.built["otel"] = c2 })
	step()
	if calls := r.target.calls(); len(calls) != 3 || calls[2] != c2 {
		t.Fatalf("created again: %v", calls)
	}
	// the setting cleared
	r.set(func() { r.name = "" })
	step()
	step()
	if calls := r.target.calls(); len(calls) != 4 || calls[3] != nil {
		t.Fatalf("cleared: %v", calls)
	}
	logged := r.out.String()
	for event, n := range map[string]int{
		`"msg":"telemetry_export_on","instance":"otel"`: 2, `"msg":"telemetry_export_off"`: 1,
		`"msg":"telemetry_export_disabled","instance":"otel","code":"instance_not_found"`: 1,
		`"code":"store"`: 1, `"code":"settings_unreadable"`: 1,
	} {
		if got := strings.Count(logged, event); got != n {
			t.Errorf("%s logged %d times, want %d\n%s", event, got, n, logged)
		}
	}
	for _, secret := range []string{"hunter2", "otel.example", "export-secret"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("log carries %q: %s", secret, logged)
		}
	}
}

func TestExportAppliesAtAPoke(t *testing.T) {
	r := newExportRig(t)
	r.deps.Every = time.Hour
	poke, stop := runExport(context.Background(), r.deps)
	defer stop()
	c := otlpTestClient(t, "https://otel.example")
	r.set(func() { r.name, r.built["otel"] = "otel", c })
	poke()
	deadline := time.Now().Add(5 * time.Second)
	for len(r.target.calls()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("a poke applied nothing")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if calls := r.target.calls(); calls[0] != c {
		t.Fatalf("calls %v", calls)
	}
}

// the tracing setting rides the export's reconcile, and the instance takes
// traces with logs and metrics
func TestExportCarriesTracing(t *testing.T) {
	r := newExportRig(t)
	p := tracing.Setup(tracing.Options{Settings: tracing.Settings{Enabled: true, SampleRatio: 1, RingSize: 100}})
	defer func() { _ = p.Shutdown(context.Background()) }()
	r.deps.Target = exportTargets{telemetry: r.target, traces: p}
	r.deps.Tracing = func(s settings.Tracing) { p.Apply(tracingSettings(s)) }
	r.deps.Global = func(context.Context) (settings.Values, error) {
		v := settings.Defaults()
		v.TelemetryExport = "otel"
		v.Tracing = settings.Tracing{SampleRatio: 0.5, RingSize: 20}
		return v, nil
	}
	c := otlpTestClient(t, "https://otel.example")
	r.set(func() { r.built["otel"] = c })
	cur := reconcileExport(context.Background(), r.deps, exportState{})
	if st := p.Stats(); st.Enabled || st.SampleRatio != 0.5 || st.RingSize != 20 || !st.Export.On {
		t.Fatalf("tracing %+v", st)
	}
	r.set(func() { delete(r.built, "otel") })
	reconcileExport(context.Background(), r.deps, cur)
	if p.Stats().Export.On {
		t.Fatal("traces still exported with the instance gone")
	}
	if !untracedPath("/api/v1/traces/abc") || !untracedPath("/api/v1/live") || untracedPath("/api/v1/devices") {
		t.Fatal("untraced paths")
	}
}
