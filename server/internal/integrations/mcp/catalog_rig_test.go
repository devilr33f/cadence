package mcp_test

import (
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp/mcptest"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

// fakeClock runs a timer's func on Advance, on the caller's goroutine.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	c    *fakeClock
	at   time.Time
	f    func()
	done bool
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := !t.done
	t.done = true
	return was
}

func newClock() *fakeClock { return &fakeClock{now: time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) mcp.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	live := c.timers[:0]
	for _, t := range c.timers {
		switch {
		case t.done:
		case !t.at.After(c.now):
			t.done = true
			due = append(due, t)
		default:
			live = append(live, t)
		}
	}
	c.timers = live
	c.mu.Unlock()
	slices.SortStableFunc(due, func(a, b *fakeTimer) int { return a.at.Compare(b.at) })
	for _, t := range due {
		t.f()
	}
}

type catRig struct {
	t     *testing.T
	pool  *pgxpool.Pool
	store *mcp.Store
	clock *fakeClock
	logs  *logBuffer
	bus   *events.Bus
	cat   *mcp.Catalog
}

func newCatalog(t *testing.T) *catRig {
	t.Helper()
	pool := storetest.Pool(t)
	r := &catRig{t: t, pool: pool, clock: newClock(), logs: &logBuffer{}, bus: events.NewBus(0)}
	r.store = mcp.NewStore(db.New(pool))
	r.cat = r.open()
	return r
}

// open is another catalog on the same database and bus, as after a restart.
func (r *catRig) open() *mcp.Catalog {
	c := mcp.NewCatalog(mcp.CatalogConfig{
		Store: r.store, Log: slog.New(slog.NewTextHandler(r.logs, nil)), Clock: r.clock, Bus: r.bus,
	})
	r.t.Cleanup(c.Close)
	return c
}

// server is instance name on f, its client trusting f, fast timings.
func (r *catRig) server(name string, f *mcptest.Server, opts ...clientOpt) mcp.Server {
	return mcp.Server{Instance: name, ID: name + "-1", Client: client(r.t, f, opts...)}
}

func withPrefix(p string) clientOpt { return func(c *mcp.ClientConfig) { c.ToolPrefix = p } }

func (r *catRig) entries() map[string]mcp.Entry {
	out := map[string]mcp.Entry{}
	for _, e := range r.cat.Entries() {
		out[e.Instance+"/"+e.Remote] = e
	}
	return out
}

func (r *catRig) entry(instance, remote string) mcp.Entry {
	r.t.Helper()
	e, ok := r.entries()[instance+"/"+remote]
	if !ok {
		r.t.Fatalf("no entry %s/%s in %v", instance, remote, r.cat.Entries())
	}
	return e
}

func offeredNames(c *mcp.Catalog) []string {
	var out []string
	for _, e := range c.Offered() {
		out = append(out, e.Local)
	}
	return out
}

// listed waits until instance has an entry for remote.
func (r *catRig) listed(instance, remote string) mcp.Entry {
	r.t.Helper()
	eventually(r.t, instance+" lists "+remote, func() bool {
		_, ok := r.entries()[instance+"/"+remote]
		return ok
	})
	return r.entry(instance, remote)
}

// private fails the test when the catalog's log holds any of words, the
// credential or a fake's endpoint or remote text.
func (r *catRig) private(words ...string) {
	r.t.Helper()
	s := r.logs.String()
	for _, w := range append(words, auth, mcptest.Leak, mcptest.Path, "Echoes.", "Waits.", "https://") {
		if strings.Contains(s, w) {
			r.t.Fatalf("log holds %q:\n%s", w, s)
		}
	}
}
