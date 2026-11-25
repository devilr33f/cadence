package airplay_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/airplay"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

type sink struct {
	mu   sync.Mutex
	sent []*audiodv1.AirplayConfig
}

func (s *sink) send(env *audiodv1.Envelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := env.GetAirplayConfig(); c != nil {
		s.sent = append(s.sent, c)
	}
	return nil
}

func (s *sink) configs() []*audiodv1.AirplayConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*audiodv1.AirplayConfig(nil), s.sent...)
}

func newAirplay(t *testing.T) (*airplay.Airplay, capability.Provider, *strings.Builder) {
	t.Helper()
	logs := &strings.Builder{}
	a := airplay.New(airplay.Options{Dir: "/data/airplay", ScratchDir: "/run/airplay"}, slog.New(slog.NewTextHandler(logs, nil)))
	return a, a.Capability(), logs
}
func apply(p capability.Provider, cfg string) error {
	return p.Apply(context.Background(), 1, json.RawMessage(cfg))
}

func status(a *airplay.Airplay, st audiodv1.AirplayState, code string) {
	a.AirplayEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_AirplayStatus{AirplayStatus: &audiodv1.AirplayStatus{State: st, Code: code}}})
}

func latest(t *testing.T, p capability.Provider) string {
	t.Helper()
	var got string
	for {
		select {
		case v := <-p.State():
			got = string(v)
		case <-time.After(20 * time.Millisecond):
			return got
		}
	}
}

func TestDescriptor(t *testing.T) {
	_, p, _ := newAirplay(t)
	if d := p.Descriptor(); d.ID != "media.airplay" || d.Version != 1 || len(d.Secrets) != 0 {
		t.Fatalf("descriptor %+v", d)
	}
}

// stored: the config audiod gets on its next Ready
func stored(a *airplay.Airplay) *audiodv1.AirplayConfig {
	s := &sink{}
	a.AirplayReady(s.send)
	return s.configs()[0]
}

func TestConfigValidation(t *testing.T) {
	valid := []struct {
		cfg     string
		enabled bool
		name    string
	}{
		{`{}`, false, "Cadence"}, {`{"enabled":true}`, true, "Cadence"}, {`{"name":"Den"}`, false, "Den"},
		{`{"enabled":false,"name":"a b.c_d-e"}`, false, "a b.c_d-e"},
		{`{"name":"` + strings.Repeat("x", 32) + `"}`, false, strings.Repeat("x", 32)},
	}
	invalid := []string{
		``, `[]`, `null`, `{"x":1}`, `{"enabled":"yes"}`, `{"enabled":null}`, `{"name":null}`, `{"name":""}`, `{"name":" a"}`, `{"name":"a "}`,
		`{"name":"a/b"}`, `{"name":"é"}`, `{"name":"` + strings.Repeat("x", 33) + `"}`, `{} {}`,
	}
	for _, v := range valid {
		a, p, _ := newAirplay(t)
		if err := apply(p, v.cfg); err != nil {
			t.Errorf("%s refused: %v", v.cfg, err)
		}
		if c := stored(a); c.GetEnabled() != v.enabled || c.GetName() != v.name {
			t.Errorf("%s stored %v", v.cfg, c)
		}
	}
	for _, cfg := range invalid {
		a, p, _ := newAirplay(t)
		if err := apply(p, `{"enabled":true,"name":"Den"}`); err != nil {
			t.Fatal(err)
		}
		var ae *capability.ApplyError
		if err := apply(p, cfg); !errors.As(err, &ae) || ae.Code != "invalid" {
			t.Errorf("%s: err %v", cfg, err)
		}
		// a refused config leaves the one before it
		if c := stored(a); !c.GetEnabled() || c.GetName() != "Den" {
			t.Errorf("%s stored %v", cfg, c)
		}
	}
}

// the config reaches audiod after every Ready and on change, never with audiod down
func TestConfigFollowsReadyAndChange(t *testing.T) {
	a, p, _ := newAirplay(t)
	if err := apply(p, `{"enabled":true,"name":"Den"}`); err != nil {
		t.Fatal(err)
	}
	s := &sink{}
	a.AirplayReady(s.send)
	if err := apply(p, `{"enabled":true,"name":"Hall"}`); err != nil {
		t.Fatal(err)
	}
	a.AirplayGone()
	if err := apply(p, `{"enabled":false}`); err != nil {
		t.Fatal(err)
	}
	if got := s.configs(); len(got) != 2 {
		t.Fatalf("configs %v", got)
	}
	a.AirplayReady(s.send)
	got := s.configs()
	if len(got) != 3 {
		t.Fatalf("configs %v", got)
	}
	for i, want := range []struct {
		enabled bool
		name    string
	}{{true, "Den"}, {true, "Hall"}, {false, "Cadence"}} {
		if c := got[i]; c.GetEnabled() != want.enabled || c.GetName() != want.name || c.GetDir() != "/data/airplay" || c.GetScratchDir() != "/run/airplay" {
			t.Fatalf("config %d = %v", i, c)
		}
	}
}

func TestStateMapping(t *testing.T) {
	a, p, logs := newAirplay(t)
	if got := latest(t, p); got != `{"state":"off"}` {
		t.Fatalf("initial %s", got)
	}
	if err := apply(p, `{"enabled":true}`); err != nil {
		t.Fatal(err)
	}
	if got := latest(t, p); got != `{"state":"starting"}` {
		t.Fatalf("enabled %s", got)
	}
	a.AirplayReady((&sink{}).send)
	status(a, audiodv1.AirplayState_AIRPLAY_STATE_READY, "")
	if got := latest(t, p); got != `{"state":"ready"}` {
		t.Fatalf("ready %s", got)
	}
	// a stale off, or an unspecified one, is still starting
	status(a, audiodv1.AirplayState_AIRPLAY_STATE_OFF, "")
	if got := latest(t, p); got != `{"state":"starting"}` {
		t.Fatalf("off %s", got)
	}
	for _, code := range []string{
		"runtime_missing", "foreign_service", "ports_busy", "spawn_failed", "timeout", "no_interface", "scratch_failed",
		"invalid_name", "bus_failed", "exited", "session_lost", "wrong_protocol", "invalid_state",
	} {
		status(a, audiodv1.AirplayState_AIRPLAY_STATE_FAILED, code)
		if got, want := latest(t, p), `{"state":"failed","code":"`+code+`"}`; got != want {
			t.Fatalf("%s: %s", code, got)
		}
	}
	// a code that is no token of ours (a name, upstream text) never shows
	for _, code := range []string{"Alice's iPhone", "john", "", strings.Repeat("a", 65)} {
		status(a, audiodv1.AirplayState_AIRPLAY_STATE_READY, "")
		latest(t, p)
		status(a, audiodv1.AirplayState_AIRPLAY_STATE_FAILED, code)
		if got := latest(t, p); got != `{"state":"failed"}` {
			t.Fatalf("code %q: %s", code, got)
		}
	}
	for _, secret := range []string{"Alice", "john", "iPhone"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("log carries %q: %s", secret, logs)
		}
	}
	if err := apply(p, `{"enabled":false}`); err != nil {
		t.Fatal(err)
	}
	if got := latest(t, p); got != `{"state":"off"}` {
		t.Fatalf("disabled %s", got)
	}
	// audiod gone: its status goes with it
	if err := apply(p, `{"enabled":true}`); err != nil {
		t.Fatal(err)
	}
	status(a, audiodv1.AirplayState_AIRPLAY_STATE_READY, "")
	a.AirplayGone()
	if got := latest(t, p); got != `{"state":"starting"}` {
		t.Fatalf("gone %s", got)
	}
}

func TestNoCommands(t *testing.T) {
	_, p, _ := newAirplay(t)
	if _, err := p.Command(context.Background(), "x", nil); !errors.Is(err, capability.ErrUnsupported) {
		t.Fatalf("command: %v", err)
	}
}

// the speakers send shairport's config alone; libairplay's adds mirroring, its size, h265 and the pin, and its
// receiver's state reaches OnState
func TestLibairplayConfigAndState(t *testing.T) {
	a, _, _ := newAirplay(t)
	a.Configure(airplay.Config{Enabled: true, Mirroring: true, MaxHeight: 720, RequirePin: true})
	if c := stored(a); c.GetBackend() != audiodv1.AirplayBackend_AIRPLAY_BACKEND_UNSPECIFIED || c.GetMirroring() ||
		c.GetRequirePin() || c.GetMaxHeight() != 0 || c.GetName() != "Cadence" {
		t.Fatalf("speaker config %v", c)
	}
	var mu sync.Mutex
	var states []string
	lib := airplay.New(airplay.Options{
		ScratchDir: "/run/airplay", Backend: audiodv1.AirplayBackend_AIRPLAY_BACKEND_LIBAIRPLAY,
		OnState: func(st, code string) {
			mu.Lock()
			states = append(states, st+":"+code)
			mu.Unlock()
		},
	}, nil)
	lib.Configure(airplay.Config{Enabled: true, Name: "Зал", Mirroring: true, MaxWidth: 1280, MaxHeight: 720, H265: true, RequirePin: true})
	c := stored(lib)
	if c.GetBackend() != audiodv1.AirplayBackend_AIRPLAY_BACKEND_LIBAIRPLAY || !c.GetMirroring() || !c.GetRequirePin() ||
		!c.GetH265() || c.GetMaxWidth() != 1280 || c.GetMaxHeight() != 720 || c.GetName() != "Зал" {
		t.Fatalf("libairplay config %v", c)
	}
	status(lib, audiodv1.AirplayState_AIRPLAY_STATE_FAILED, "backend_unavailable")
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(states, " "); got != "off: starting: failed:backend_unavailable" {
		t.Fatalf("states %s", got)
	}
}
