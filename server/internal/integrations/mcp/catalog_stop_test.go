package mcp_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp/mcptest"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// stalled is a database that answers nothing until the caller's ctx ends.
type stalled struct{ entered chan struct{} }

func (s stalled) wait(ctx context.Context) error {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}

func (s stalled) Exec(ctx context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, s.wait(ctx)
}

func (s stalled) Query(ctx context.Context, _ string, _ ...any) (pgx.Rows, error) {
	return nil, s.wait(ctx)
}

func (s stalled) QueryRow(ctx context.Context, _ string, _ ...any) pgx.Row {
	return failedRow{s.wait(ctx)}
}

type failedRow struct{ err error }

func (r failedRow) Scan(...any) error { return r.err }

func stalledCatalog(t *testing.T) (*mcp.Catalog, stalled) {
	t.Helper()
	d := stalled{entered: make(chan struct{}, 16)}
	c := mcp.NewCatalog(mcp.CatalogConfig{Store: mcp.NewStore(db.New(d)), Log: slog.New(slog.DiscardHandler)})
	t.Cleanup(c.Close)
	return c, d
}

// stops fails the test unless stop returns within 3 s.
func stops(t *testing.T, what string, stop func()) time.Duration {
	t.Helper()
	start := time.Now()
	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("the stop waited on %s", what)
	}
	return time.Since(start)
}

// a stalled database never holds a stop: the listing in it ends with the
// supervisor's context, and the re-list behind it with the refresher's
func TestStopNeverWaitsForAStalledDatabase(t *testing.T) {
	cat, d := stalledCatalog(t)
	f := fake(t, mcptest.Options{})
	cat.Apply([]mcp.Server{{Instance: "srv", ID: "srv-1", Client: client(t, f)}})
	select {
	case <-d.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the listing never reached the store")
	}
	n := f.Count("tools/list")
	cat.Refresh("srv")
	eventually(t, "the re-list", func() bool { return f.Count("tools/list") > n })
	// time for the re-list to reach the listing lock the stalled one holds
	time.Sleep(100 * time.Millisecond)
	stops(t, "a stalled store", cat.Close)
}

// a listing that waits for the lock another listing holds, stalled in the
// database, ends with its own server's context
func TestStopNeverWaitsForAnotherListing(t *testing.T) {
	cat, _ := stalledCatalog(t)
	unlock := mcp.LockListing(cat)
	defer unlock()
	f := fake(t, mcptest.Options{})
	cat.Apply([]mcp.Server{{Instance: "srv", ID: "srv-1", Client: client(t, f)}})
	eventually(t, "the listing", func() bool { return f.Count("tools/list") > 0 })
	time.Sleep(100 * time.Millisecond)
	stops(t, "the listing lock", func() { cat.Apply(nil) })
	// an owner's change waits for the lock as long as its request does
	ctx, cancel := context.WithTimeout(bg(), 50*time.Millisecond)
	defer cancel()
	on := true
	if _, err := cat.Override(ctx, "srv", "echo", mcp.Patch{AnyTurn: &on}); err == nil {
		t.Fatal("an override past its deadline")
	}
	if err := cat.Acknowledge(ctx, "srv", nil, true); err == nil {
		t.Fatal("an acknowledge past its deadline")
	}
}

// servers stop side by side at shutdown: two legacy sessions whose DELETE
// the server holds take one second's grace together, not one each
func TestServersStopTogether(t *testing.T) {
	r := newCatalog(t)
	fa, fb := fake(t, mcptest.Options{Mode: mcptest.Legacy}), fake(t, mcptest.Options{Mode: mcptest.Legacy})
	r.cat.Apply([]mcp.Server{r.server("a", fa), r.server("b", fb)})
	r.listed("a", "echo")
	r.listed("b", "echo")
	fa.Hold(true)
	fb.Hold(true)
	defer fa.Hold(false)
	defer fb.Hold(false)
	if took := stops(t, "two held sessions", r.cat.Close); took < 900*time.Millisecond || took > 1700*time.Millisecond {
		t.Fatalf("stopped in %v", took)
	}
}
