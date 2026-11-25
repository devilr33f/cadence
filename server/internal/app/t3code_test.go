package app

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code/t3codetest"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/subscriptions"
)

type t3Rig struct {
	mu      sync.Mutex
	names   []string
	clients map[string]*t3code.Client
	kinds   map[string]providers.Kind
	src     *t3code.Source
	reg     *subscriptions.Registry
	out     *bytes.Buffer
	deps    t3Deps
}

func newT3Rig(t *testing.T) *t3Rig {
	t.Helper()
	r := &t3Rig{clients: map[string]*t3code.Client{}, kinds: map[string]providers.Kind{}, reg: subscriptions.NewRegistry(), out: &bytes.Buffer{}}
	r.src = t3code.NewSource(t3code.SourceDeps{})
	r.deps = t3Deps{
		Global: func(context.Context) (settings.Values, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			v := settings.Defaults()
			v.T3Code = append([]string(nil), r.names...)
			return v, nil
		},
		Ref: func(_ context.Context, name string) (providers.Ref, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			k, ok := r.kinds[name]
			if !ok {
				return providers.Ref{}, providers.ErrNotFound
			}
			return providers.Ref{ID: name, Kind: k}, nil
		},
		Build: func(_ context.Context, name string) (*t3code.Client, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.clients[name], nil
		},
		Source: r.src, Registry: r.reg, Log: slog.New(slog.NewJSONHandler(&lockedBuf{b: r.out}, nil)), Every: time.Hour,
		Disabled: newMCPDisabled(),
	}
	return r
}

type lockedBuf struct {
	mu sync.Mutex
	b  *bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (r *t3Rig) set(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn()
}

func fakeT3Client(t *testing.T) *t3code.Client {
	t.Helper()
	srv := httptest.NewServer(t3codetest.New(t3codetest.Config{Token: "tok"}))
	t.Cleanup(srv.Close)
	c, err := t3code.NewClient(t3code.ClientConfig{BaseURL: srv.URL, Token: "tok", HTTP: srv.Client(), Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// the t3code setting applies live: the module registers on the hub the first
// time it names an instance, an instance starts and stops with the setting,
// one of the wrong kind is left out with its code, once
func TestT3CodeFollowsTheSetting(t *testing.T) {
	r := newT3Rig(t)
	c := fakeT3Client(t)
	poke, stop := runT3Code(context.Background(), r.deps)
	defer stop()
	time.Sleep(50 * time.Millisecond)
	if _, ok := r.reg.Get("t3code"); ok {
		t.Fatal("registered while off")
	}
	r.set(func() {
		r.names = []string{"work", "notes"}
		r.kinds["work"], r.kinds["notes"] = providers.KindT3Code, providers.KindMCP
		r.clients["work"] = c
	})
	poke()
	eventually(t, "running", func() bool { st, ok := r.src.Status("work"); return ok && st.Synced })
	if _, ok := r.reg.Get("t3code"); !ok {
		t.Fatal("not registered")
	}
	if _, ok := r.src.Status("notes"); ok {
		t.Fatal("an mcp instance runs as t3code")
	}
	poke()
	time.Sleep(100 * time.Millisecond)
	if n := strings.Count(r.out.String(), `"t3code_disabled"`); n != 1 {
		t.Fatalf("disabled logged %d times:\n%s", n, r.out)
	}
	if !strings.Contains(r.out.String(), `"code":"instance_wrong_kind"`) {
		t.Fatalf("log %s", r.out)
	}
	r.set(func() { r.names = nil })
	poke()
	eventually(t, "stopped", func() bool { _, ok := r.src.Status("work"); return !ok })
	// named again: the same client runs again
	r.set(func() { r.names = []string{"work"} })
	poke()
	eventually(t, "running again", func() bool { st, ok := r.src.Status("work"); return ok && st.Synced })
}
