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

	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

type applyLog chan []mcp.Server

func (a applyLog) Apply(s []mcp.Server) { a <- s }

// Close closes the log: a read after it gets nothing, and an Apply after it
// panics.
func (a applyLog) Close() { close(a) }

func (a applyLog) next(t *testing.T) []mcp.Server {
	t.Helper()
	select {
	case s := <-a:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("nothing applied")
		return nil
	}
}

func (a applyLog) none(t *testing.T) {
	t.Helper()
	select {
	case s := <-a:
		t.Fatalf("applied %v", s)
	case <-time.After(50 * time.Millisecond):
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestMCPServersFollowTheSettingAndInstances(t *testing.T) {
	var mu sync.Mutex
	var names []string
	var readErr error
	refs := map[string]providers.Ref{}
	refErrs := map[string]error{}
	clients := map[string]*mcp.Client{}
	d := mcpDeps{
		Global: func(context.Context) (settings.Values, error) {
			mu.Lock()
			defer mu.Unlock()
			return settings.Values{MCP: names}, readErr
		},
		Ref: func(_ context.Context, name string) (providers.Ref, error) {
			mu.Lock()
			defer mu.Unlock()
			if err := refErrs[name]; err != nil {
				return providers.Ref{}, err
			}
			if r, ok := refs[name]; ok {
				return r, nil
			}
			return providers.Ref{}, providers.ErrNotFound
		},
		Build: func(_ context.Context, name string) (*mcp.Client, error) {
			mu.Lock()
			defer mu.Unlock()
			if c := clients[name]; c != nil {
				return c, nil
			}
			c, err := mcp.NewClient(mcp.ClientConfig{URL: "https://mcp.example/" + name})
			clients[name] = c
			return c, err
		},
		Servers:  make(applyLog, 16),
		Every:    time.Hour,
		Disabled: newMCPDisabled(),
	}
	logs := &syncBuffer{}
	d.Log = slog.New(slog.NewTextHandler(logs, nil))
	set := func(f func()) {
		mu.Lock()
		f()
		mu.Unlock()
	}
	applied := d.Servers.(applyLog)
	poke, stop := runMCP(context.Background(), d)
	if got := applied.next(t); len(got) != 0 {
		t.Fatalf("nothing listed ran %v", got)
	}
	set(func() {
		names = []string{"a", "b"}
		refs["a"] = providers.Ref{ID: "a1", Kind: providers.KindMCP}
		refs["b"] = providers.Ref{ID: "b1", Kind: providers.KindMCP}
	})
	poke()
	got := applied.next(t)
	if len(got) != 2 || got[0].Instance != "a" || got[0].ID != "a1" || got[0].Client != clients["a"] || got[1].Instance != "b" {
		t.Fatalf("applied %+v", got)
	}
	// b deleted, then of another kind: left out, logged once each
	set(func() { delete(refs, "b") })
	for range 2 {
		poke()
		if got := applied.next(t); len(got) != 1 || got[0].Instance != "a" {
			t.Fatalf("applied %+v", got)
		}
	}
	set(func() { refs["b"] = providers.Ref{ID: "b2", Kind: providers.KindWeather} })
	poke()
	if got := applied.next(t); len(got) != 1 {
		t.Fatalf("applied %+v", got)
	}
	// the admin API shows why a listed instance does not run
	if got := d.Disabled.Code("b"); got != "instance_wrong_kind" {
		t.Fatalf("b left out with %q", got)
	}
	for _, line := range []string{"msg=mcp_disabled instance=b code=instance_not_found\n", "msg=mcp_disabled instance=b code=instance_wrong_kind\n"} {
		if strings.Count(logs.String(), line) != 1 {
			t.Fatalf("%q not once:\n%s", line, logs)
		}
	}
	// a deleted and created again: a new id
	set(func() { refs["a"] = providers.Ref{ID: "a2", Kind: providers.KindMCP} })
	poke()
	if got := applied.next(t); len(got) != 1 || got[0].ID != "a2" {
		t.Fatalf("applied %+v", got)
	}
	// b of kind mcp again runs, and shows no code
	set(func() { refs["b"] = providers.Ref{ID: "b3", Kind: providers.KindMCP} })
	poke()
	if got := applied.next(t); len(got) != 2 || d.Disabled.Code("b") != "" {
		t.Fatalf("applied %+v, b %q", got, d.Disabled.Code("b"))
	}
	// a store or settings failure keeps what runs
	set(func() { refErrs["a"] = errors.New("db down") })
	poke()
	applied.none(t)
	set(func() { refErrs["a"], readErr = nil, errors.New("db down") })
	poke()
	applied.none(t)
	for _, line := range []string{"msg=mcp_reconcile_failed code=store\n", "msg=mcp_reconcile_failed code=settings_unreadable\n"} {
		if !strings.Contains(logs.String(), line) {
			t.Fatalf("no %q:\n%s", line, logs)
		}
	}
	stop()
	if got, ok := <-applied; ok {
		t.Fatalf("stop applied %+v instead of closing", got)
	}
	if strings.Contains(logs.String(), "mcp.example") || strings.Contains(logs.String(), "db down") {
		t.Fatalf("log:\n%s", logs)
	}
}

func TestMCPInstanceCheck(t *testing.T) {
	down := errors.New("db down")
	check := mcpInstanceCheck(func(_ context.Context, name string) (providers.Ref, error) {
		switch name {
		case "mcp":
			return providers.Ref{ID: "1", Kind: providers.KindMCP}, nil
		case "weather":
			return providers.Ref{ID: "2", Kind: providers.KindWeather}, nil
		case "old":
			return providers.Ref{}, providers.ErrUnknownModule
		case "down":
			return providers.Ref{}, down
		}
		return providers.Ref{}, providers.ErrNotFound
	})
	ctx := context.Background()
	if err := check(ctx, "mcp"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"weather", "old", "missing"} {
		if err := check(ctx, name); !errors.Is(err, settings.ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := check(ctx, "down"); !errors.Is(err, down) {
		t.Fatalf("down: %v", err)
	}
	isMCP := mcpIsInstance(check)
	for name, want := range map[string]bool{"mcp": true, "weather": false, "old": false, "missing": false} {
		if got, err := isMCP(ctx, name); got != want || err != nil {
			t.Fatalf("%s: %v %v", name, got, err)
		}
	}
	if _, err := isMCP(ctx, "down"); !errors.Is(err, down) {
		t.Fatalf("down: %v", err)
	}
}

// a server the setting drops is published as removed; the servers stop
// stops at shutdown are not: the feed would tell every dashboard each one
// was removed
func TestMCPStopPublishesNoRemovedServer(t *testing.T) {
	bus := events.NewBus(0)
	feed := bus.Watch(func(e events.Event) bool { return e.Type == events.MCPUnavailable })
	defer feed.Close()
	cat := mcp.NewCatalog(mcp.CatalogConfig{Log: slog.New(slog.DiscardHandler), Bus: bus})
	var mu sync.Mutex
	names := []string{"gone", "kept"}
	d := mcpDeps{
		Global: func(context.Context) (settings.Values, error) {
			mu.Lock()
			defer mu.Unlock()
			return settings.Values{MCP: names}, nil
		},
		Ref: func(_ context.Context, name string) (providers.Ref, error) {
			return providers.Ref{ID: name + "-1", Kind: providers.KindMCP}, nil
		},
		// nothing answers there: the servers run, and fail to connect
		Build: func(context.Context, string) (*mcp.Client, error) {
			return mcp.NewClient(mcp.ClientConfig{URL: "https://127.0.0.1:1/mcp"})
		},
		Servers: cat, Every: time.Hour, Log: slog.New(slog.DiscardHandler),
	}
	running := func(name string) bool { _, _, ok := cat.Health(name); return ok }
	poke, stop := runMCP(context.Background(), d)
	waitFor(t, "both running", func() bool { return running("gone") && running("kept") })
	removed := func() []string {
		var out []string
		for {
			select {
			case e := <-feed.C:
				if u, ok := e.Data.(mcp.LiveUnavailable); ok && u.Code == "removed" {
					out = append(out, u.Instance)
				}
			case <-time.After(200 * time.Millisecond):
				return out
			}
		}
	}
	mu.Lock()
	names = []string{"kept"}
	mu.Unlock()
	poke()
	waitFor(t, "gone stopped", func() bool { return !running("gone") })
	if got := removed(); len(got) != 1 || got[0] != "gone" {
		t.Fatalf("removed %v, want [gone]", got)
	}
	stop()
	if running("kept") {
		t.Fatal("kept still runs after stop")
	}
	if got := removed(); len(got) != 0 {
		t.Fatalf("stop published removed for %v", got)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
