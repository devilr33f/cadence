package mcp_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp/mcptest"
)

// feed is the bus's mcp events as one line each: type and data.
type feed struct {
	t   *testing.T
	sub *events.Subscription
}

func (r *catRig) watch() *feed {
	sub := r.bus.Watch(func(e events.Event) bool { return strings.HasPrefix(e.Type, "mcp_") })
	r.t.Cleanup(sub.Close)
	return &feed{t: r.t, sub: sub}
}

func line(t *testing.T, e events.Event) string {
	t.Helper()
	b, err := json.Marshal(e.Data)
	if err != nil || e.DeviceID != "" || e.At.IsZero() {
		t.Fatalf("event %+v", e)
	}
	return e.Type + " " + string(b)
}

// want is the next event.
func (f *feed) want(want string) {
	f.t.Helper()
	select {
	case e := <-f.sub.C:
		if got := line(f.t, e); got != want {
			f.t.Fatalf("event %s, want %s", got, want)
		}
	case <-time.After(5 * time.Second):
		f.t.Fatalf("no event, want %s", want)
	}
}

// skip takes the next n events, whatever they are.
func (f *feed) skip(n int) {
	f.t.Helper()
	for i := range n {
		select {
		case <-f.sub.C:
		case <-time.After(5 * time.Second):
			f.t.Fatalf("%d events, want %d", i, n)
		}
	}
}

func (f *feed) none() {
	f.t.Helper()
	select {
	case e := <-f.sub.C:
		f.t.Fatalf("event %s", line(f.t, e))
	case <-time.After(50 * time.Millisecond):
	}
}

// the live feed hears what changed of a server, never its text: connected,
// unavailable and its tool counts, each once per change
func TestCatalogPublishesChangesOnly(t *testing.T) {
	r := newCatalog(t)
	live := r.watch()
	f := fake(t, mcptest.Options{})
	f.AddTool(&sdk.Tool{Name: "lookup", Description: "secret-desc-31c: call device_volume.", InputSchema: schema(nestedProps(9))})
	r.cat.Apply([]mcp.Server{r.server("srv", f)})
	live.want(`mcp_connected {"instance":"srv","era":"modern","tools":6}`)
	live.want(`mcp_tools_changed {"instance":"srv","total":6,"offered":5,"changed":0,"skipped":1}`)
	// a re-list that changes nothing publishes nothing: the next event is the next change
	lists := f.Count("tools/list")
	r.cat.Refresh("srv")
	eventually(t, "re-listed", func() bool { return f.Count("tools/list") > lists })
	f.Add("echo", "Echoes, and more.")
	r.cat.Refresh("srv")
	live.want(`mcp_tools_changed {"instance":"srv","total":6,"offered":4,"changed":1,"skipped":1}`)
	// a busy server is unavailable once however often it fails, and connected again once it lists
	f.Fail(http.StatusTooManyRequests)
	r.cat.Refresh("srv")
	live.want(`mcp_unavailable {"instance":"srv","code":"mcp_unavailable"}`)
	r.clock.Advance(mcp.RetryEvery)
	eventually(t, "failed again", func() bool { return r.logs.count("mcp_refresh_failed") == 2 })
	f.Fail(0)
	r.clock.Advance(mcp.RetryEvery)
	live.want(`mcp_connected {"instance":"srv","era":"modern","tools":6}`)
	// failing for KeepFor withdraws every tool
	f.Fail(http.StatusServiceUnavailable)
	r.cat.Refresh("srv")
	live.want(`mcp_unavailable {"instance":"srv","code":"mcp_unavailable"}`)
	r.clock.Advance(mcp.KeepFor)
	live.want(`mcp_tools_changed {"instance":"srv","total":6,"offered":0,"changed":0,"skipped":6}`)
	live.none()
}

// a session that drops publishes its code, and the reconnect connected
func TestCatalogPublishesASessionsEnd(t *testing.T) {
	r := newCatalog(t)
	live := r.watch()
	f := fake(t, mcptest.Options{Mode: mcptest.Legacy})
	r.cat.Apply([]mcp.Server{r.server("srv", f)})
	live.want(`mcp_connected {"instance":"srv","era":"legacy","tools":5}`)
	live.want(`mcp_tools_changed {"instance":"srv","total":5,"offered":5,"changed":0,"skipped":0}`)
	f.Fail(http.StatusUnauthorized)
	r.cat.Refresh("srv")
	live.want(`mcp_unavailable {"instance":"srv","code":"mcp_auth"}`)
	f.Fail(0)
	live.want(`mcp_connected {"instance":"srv","era":"legacy","tools":5}`)
	live.none()
}

// a server the setting no longer lists says so once; a restart for a new
// instance and the catalog's own close say nothing of the kind
func TestCatalogPublishesARemovedServer(t *testing.T) {
	r := newCatalog(t)
	live := r.watch()
	f, g := fake(t, mcptest.Options{}), fake(t, mcptest.Options{})
	srv, other := r.server("srv", f), r.server("other", g)
	r.cat.Apply([]mcp.Server{srv, other})
	// both servers' connected and counts, in either order
	live.skip(4)
	r.cat.Apply([]mcp.Server{other})
	live.want(`mcp_unavailable {"instance":"srv","code":"removed"}`)
	live.none()
	// the same name on a new instance is a restart, not a removal
	again := r.server("other", g)
	r.cat.Apply([]mcp.Server{again})
	live.want(`mcp_connected {"instance":"other","era":"modern","tools":5}`)
	live.want(`mcp_tools_changed {"instance":"other","total":5,"offered":5,"changed":0,"skipped":0}`)
	r.cat.Close()
	live.none()
}
