package leds

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
)

func newProvider(t *testing.T, path string) (capability.Provider, *Renderer, *fakeSink, *clock, *bytes.Buffer) {
	t.Helper()
	sink, clk := newSink(round), &clock{t0}
	r, _ := newRenderer(t, sink, clk)
	var logs bytes.Buffer
	return NewCapability(r, path, slog.New(slog.NewTextHandler(&logs, nil))), r, sink, clk, &logs
}

func readState(t *testing.T, p capability.Provider) map[string]bool {
	t.Helper()
	select {
	case raw := <-p.State():
		var m map[string]bool
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no state")
		return nil
	}
}

func TestDescriptor(t *testing.T) {
	p, _, _, _, _ := newProvider(t, filepath.Join(t.TempDir(), "leds.json"))
	d := p.Descriptor()
	if d.ID != "display.lights" || d.Version != 1 || len(d.Commands) != 0 || len(d.Secrets) != 0 {
		t.Fatalf("descriptor = %+v", d)
	}
	for _, s := range []string{d.ConfigSchema, d.StateSchema} {
		if !json.Valid([]byte(s)) {
			t.Fatalf("schema is not json: %s", s)
		}
	}
	if _, err := p.Command(context.Background(), "x", nil); !errors.Is(err, capability.ErrUnsupported) {
		t.Fatalf("command err = %v", err)
	}
}

func TestApplySwitchesPersistsAndReportsState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "leds.json")
	p, r, sink, clk, _ := newProvider(t, path)
	r.SetState(Listening)
	if err := p.Apply(context.Background(), 1, json.RawMessage(`{"lights":false,"clock":true}`)); err != nil {
		t.Fatal(err)
	}
	r.step(clk.now)
	if ringPeak(sink.last()) != 0 {
		t.Fatal("lights off must blank the ring")
	}
	if st := readState(t, p); st["lights"] || !st["clock"] {
		t.Fatalf("state = %v", st)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), `"lights":false`) {
		t.Fatalf("persisted %q, %v", raw, err)
	}

	// a restart reads the file before the server's config arrives
	_, r2, sink2, clk2, _ := newProvider(t, path)
	r2.SetState(Listening)
	r2.step(clk2.now)
	if ringPeak(sink2.last()) != 0 {
		t.Fatal("persisted switches apply at boot")
	}
}

func TestApplyOmittedKeysKeepWhatIsPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leds.json")
	p, r, sink, clk, _ := newProvider(t, path)
	r.SetState(Listening)
	apply := func(cfg string) {
		t.Helper()
		if err := p.Apply(context.Background(), 1, json.RawMessage(cfg)); err != nil {
			t.Fatal(err)
		}
	}
	apply(`{"lights":false,"clock":false}`)
	// the server's empty config at connect must not turn the owner's switches back on
	apply(`{}`)
	r.step(clk.now)
	if ringPeak(sink.last()) != 0 {
		t.Fatal("an empty config reset the lights switch")
	}
	if st := readState(t, p); st["lights"] || st["clock"] {
		t.Fatalf("state = %v", st)
	}
	// one key changes only itself
	apply(`{"clock":true}`)
	if st := readState(t, p); st["lights"] || !st["clock"] {
		t.Fatalf("state = %v", st)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"lights":false`) || !strings.Contains(string(raw), `"clock":true`) {
		t.Fatalf("persisted %q", raw)
	}
	// and across a restart
	_, r2, sink2, clk2, _ := newProvider(t, path)
	r2.SetState(Listening)
	r2.step(clk2.now)
	if ringPeak(sink2.last()) != 0 || sink2.last().segments[Segment{0, 0}] == 0 {
		t.Fatal("restart lost the switches")
	}
}

func TestApplyRefusesBadConfigWithACode(t *testing.T) {
	p, _, _, _, _ := newProvider(t, filepath.Join(t.TempDir(), "leds.json"))
	for _, cfg := range []string{``, `[]`, `{"lights":"yes"}`, `{"lights":null}`, `{"x":1}`, `{"lights":true} {}`} {
		var ae *capability.ApplyError
		if err := p.Apply(context.Background(), 1, json.RawMessage(cfg)); !errors.As(err, &ae) || ae.Code != "invalid" {
			t.Fatalf("config %q: err = %v", cfg, err)
		}
	}
}

func TestInvalidSettingsFileFallsBackToOnAndLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leds.json")
	if err := os.WriteFile(path, []byte(`{"lights":false,"extra":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, r, sink, clk, logs := newProvider(t, path)
	r.SetState(Listening)
	r.step(clk.now)
	if ringPeak(sink.last()) == 0 {
		t.Fatal("invalid settings mean defaults: everything on")
	}
	if strings.Count(logs.String(), "settings_invalid") != 1 || strings.Contains(logs.String(), "extra") {
		t.Fatalf("log = %q", logs.String())
	}
}

func TestMissingSettingsFileIsDefaultsWithoutAComplaint(t *testing.T) {
	_, _, _, _, logs := newProvider(t, filepath.Join(t.TempDir(), "nope", "leds.json"))
	if logs.Len() != 0 {
		t.Fatalf("log = %q", logs.String())
	}
}

func TestStateIsAlsoOfferedAtStart(t *testing.T) {
	p, _, _, _, _ := newProvider(t, filepath.Join(t.TempDir(), "leds.json"))
	if st := readState(t, p); !st["lights"] || !st["clock"] {
		t.Fatalf("initial state = %v", st)
	}
}
