package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
)

const testToken = "fake-ha-test-token"

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// rig serves the house over tls to the server's own client and home cache.
type rig struct {
	t     *testing.T
	srv   *httptest.Server
	logs  *syncBuf
	calls *syncBuf
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, logs: &syncBuf{}, calls: &syncBuf{}}
	h := newHouse(testToken, r.calls, slog.New(slog.NewJSONHandler(r.logs, nil)), time.Now)
	r.srv = httptest.NewTLSServer(h.fake)
	t.Cleanup(func() {
		h.fake.Close()
		r.srv.Close()
	})
	// the log never carries the token, a name, a state or an entity id
	t.Cleanup(func() {
		for _, s := range []string{testToken, "Hall light", "Front door", "Hall temperature", "light.hall", "lock.front",
			"sensor.hall", `"locked"`, `"unlocked"`, "21.5", "°C"} {
			if strings.Contains(r.logs.String(), s) {
				t.Errorf("log carries %q:\n%s", s, r.logs)
			}
		}
	})
	return r
}

// client runs a client of the house with token until the test ends.
func (r *rig) client(token string) (*ha.Client, *ha.Home) {
	r.t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(r.srv.Certificate())
	c, err := ha.NewClient(ha.ClientConfig{BaseURL: r.srv.URL, Token: token, Roots: roots, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		r.t.Fatal(err)
	}
	home := ha.NewHome(c, ha.HomeOptions{Now: time.Now, Log: slog.New(slog.DiscardHandler)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	r.t.Cleanup(func() {
		cancel()
		<-done
	})
	return c, home
}

// loaded waits for home's first view.
func (r *rig) loaded(home *ha.Home) ha.View {
	r.t.Helper()
	waitUntil(r.t, "the home loaded", func() bool { v, err := home.View(); return err == nil && v.Fresh })
	v, _ := home.View()
	return v
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func entity(t *testing.T, v ha.View, id string) ha.Entity {
	t.Helper()
	e, ok := v.ByID(id)
	if !ok {
		t.Fatalf("%s missing from the view", id)
	}
	return e
}

func TestHouseLoadsIntoTheServersHome(t *testing.T) {
	r := newRig(t)
	_, home := r.client(testToken)
	v := r.loaded(home)
	if len(v.Entities) != 3 || len(v.Areas) != 1 || v.Areas[0].ID != "hall" || v.Areas[0].Name != "Hall" {
		t.Fatalf("%d entities, areas %+v", len(v.Entities), v.Areas)
	}
	light := entity(t, v, "light.hall")
	if light.Name != "Hall light" || light.Area != "hall" || light.State != "on" {
		t.Fatalf("light %+v", light)
	}
	lock := entity(t, v, "lock.front")
	if lock.Name != "Front door" || lock.Area != "" || lock.State != "locked" {
		t.Fatalf("lock %+v", lock)
	}
	// its area comes from its device
	sensor := entity(t, v, "sensor.hall_temperature")
	if sensor.Name != "Hall temperature" || sensor.Area != "hall" || sensor.State != "21.5" ||
		sensor.Attrs.UnitOfMeasurement == nil || *sensor.Attrs.UnitOfMeasurement != "°C" {
		t.Fatalf("sensor %+v", sensor)
	}
	for _, s := range []struct{ domain, service string }{
		{"homeassistant", "turn_off"}, {"homeassistant", "toggle"}, {"light", "turn_on"}, {"lock", "unlock"},
	} {
		if _, ok := v.Services[s.domain][s.service]; !ok {
			t.Errorf("no %s.%s in %v", s.domain, s.service, v.Services)
		}
	}
}

func TestHouseSwitchesWhatACallReaches(t *testing.T) {
	r := newRig(t)
	c, home := r.client(testToken)
	r.loaded(home)
	ctx := context.Background()
	steps := []struct {
		domain, service, id, want string
	}{
		{"homeassistant", "turn_off", "light.hall", "off"},
		{"lock", "unlock", "lock.front", "unlocked"},
		{"light", "toggle", "light.hall", "on"},
		// a service that switches nothing is still recorded
		{"homeassistant", "turn_off", "sensor.hall_temperature", "21.5"},
	}
	for _, s := range steps {
		if err := c.Call(ctx, s.domain, s.service, nil, s.id); err != nil {
			t.Fatalf("%s.%s: %v", s.domain, s.service, err)
		}
		if e, ok := home.Wait(ctx, s.id, func(e ha.Entity) bool { return e.State == s.want }, 2*time.Second); !ok {
			t.Fatalf("%s.%s on %s: state %q, want %q", s.domain, s.service, s.id, e.State, s.want)
		}
	}
	want := "homeassistant turn_off light.hall\nlock unlock lock.front\nlight toggle light.hall\n" +
		"homeassistant turn_off sensor.hall_temperature\n"
	waitUntil(t, "the calls written", func() bool { return r.calls.String() == want })
	// a client that connects afterwards loads the states the calls left
	_, later := r.client(testToken)
	v := r.loaded(later)
	if got := []string{entity(t, v, "light.hall").State, entity(t, v, "lock.front").State}; !slices.Equal(got, []string{"on", "unlocked"}) {
		t.Fatalf("a later load sees %v", got)
	}
}

func TestHouseNeedsItsToken(t *testing.T) {
	r := newRig(t)
	c, _ := r.client("wrong")
	waitUntil(t, "the token refused", func() bool { return c.Status().LastError == ha.CodeAuthFailed })
}
