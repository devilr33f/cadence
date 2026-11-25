package tracing

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"go.opentelemetry.io/otel/attribute"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/events"
)

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

func TestAdminRoutes(t *testing.T) {
	p := setup(t)
	ctx, root := Start(context.Background(), "agent", "agent.turn")
	_, child := Start(ctx, "agent", "llm.request")
	End(child, nil)
	End(root, nil)
	app := api.NewApp(api.Options{DB: noPing{}, Version: "t", VerifyToken: apitest.Verify,
		Admin: []func(h huma.API){func(h huma.API) { RegisterAdmin(h, p) }}})
	id := root.SpanContext().TraceID().String()

	st, body := apitest.Call(t, app, "admin", "GET", "/api/v1/traces?limit=10", "")
	var page TracePage
	if st != 200 || json.Unmarshal([]byte(body), &page) != nil || len(page.Traces) != 1 || page.Traces[0].TraceID != id || page.Seq == 0 {
		t.Fatalf("list %d %s", st, body)
	}
	st, body = apitest.Call(t, app, "admin", "GET", "/api/v1/traces/"+id, "")
	var tr TraceView
	if st != 200 || json.Unmarshal([]byte(body), &tr) != nil || len(tr.Spans) != 2 || len(tr.RootIDs) != 1 ||
		!strings.Contains(body, `"parentId"`) || !strings.Contains(body, `"durationMs"`) {
		t.Fatalf("trace %d %s", st, body)
	}
	st, body = apitest.Call(t, app, "admin", "GET", "/api/v1/traces/stats", "")
	var stats TraceStats
	if st != 200 || json.Unmarshal([]byte(body), &stats) != nil || !stats.Enabled || stats.Traces != 1 || stats.RingSize != 100 {
		t.Fatalf("stats %d %s", st, body)
	}
	if st, body = apitest.Call(t, app, "admin", "GET", "/api/v1/traces/"+strings.Repeat("0", 31)+"1", ""); st != 404 || body != `{"error":"not_found"}` {
		t.Fatalf("gone %d %s", st, body)
	}
	if st, _ = apitest.Call(t, app, "admin", "GET", "/api/v1/traces/NOT-A-TRACE", ""); st != 422 {
		t.Fatalf("bad id %d", st)
	}
	// admin's alone: span names say what the devices did
	if st, _ = apitest.Call(t, app, apitest.HABearer, "GET", "/api/v1/traces", ""); st != 403 {
		t.Fatalf("ha %d", st)
	}
}

func TestPublishToTheBusWhileWanted(t *testing.T) {
	p := setup(t)
	bus := events.NewBus(16)
	got, cancel := bus.Subscribe(nil)
	defer cancel()
	var wanted atomic.Bool
	var asked atomic.Int32
	stop := p.PublishTo(context.Background(), bus, func() bool { asked.Add(1); return wanted.Load() })
	defer stop()
	_, s := Start(context.Background(), "agent", "unwatched")
	End(s, nil)
	// both of its events were drained unpublished before the feed subscribes
	eventually(t, func() bool { return asked.Load() == 2 })
	wanted.Store(true)
	_, s = Start(context.Background(), "agent", "watched", attributeDevice("dev-9"))
	End(s, nil)
	ev := <-got
	data, ok := ev.Data.(FeedData)
	if !ok || ev.Type != events.SpanStart || data.Span.Name != "watched" || ev.DeviceID != "dev-9" || data.Seq == 0 {
		t.Fatalf("event %+v", ev)
	}
	if ev := <-got; ev.Type != events.SpanEnd {
		t.Fatalf("second %+v", ev)
	}
}

func attributeDevice(id string) attribute.KeyValue { return attribute.String(AttrDeviceID, id) }

// the api's Trace option runs before the auth gate: a refusal is traced too,
// under the operation's route template
func TestAPITraceOption(t *testing.T) {
	p := setup(t)
	app := api.NewApp(api.Options{DB: noPing{}, Version: "t", VerifyToken: apitest.Refuse, Trace: Middleware(nil),
		Admin: []func(h huma.API){func(h huma.API) { RegisterAdmin(h, p) }}})
	if st, _ := apitest.Call(t, app, "nobody", "GET", "/api/v1/traces/"+strings.Repeat("a", 32), ""); st != 401 {
		t.Fatalf("status %d", st)
	}
	page := p.Store().List(ListQuery{})
	if len(page.Traces) != 1 {
		t.Fatalf("traces %+v", page)
	}
	tr, _ := p.Store().Trace(page.Traces[0].TraceID)
	if v := tr.Spans[0]; v.Attrs[AttrHTTPStatus] != int64(401) || v.Kind != "server" || strings.Contains(v.Name, strings.Repeat("a", 32)) {
		t.Fatalf("span %+v", v)
	}
}
