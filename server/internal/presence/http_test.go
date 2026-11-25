package presence_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/presence"
)

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

var bearers = map[string]auth.Scopes{
	"tok-admin": auth.AdminScopes, "tok-presence": {auth.ScopePresence}, "tok-ha": {auth.ScopeHA},
	"tok-metrics": {auth.ScopeMetrics},
}

func (r *rig) app() *fiber.App {
	return api.NewApp(api.Options{
		DB: noPing{}, Version: "t",
		VerifyToken: func(_ context.Context, b string) (auth.Grants, error) {
			if s, ok := bearers[b]; ok {
				return auth.Grants{Scopes: s}, nil
			}
			return auth.Grants{}, auth.ErrUnauthorized
		},
		Admin: []func(huma.API){func(a huma.API) { presence.RegisterAdmin(a, r.s) }},
	})
}

func TestBusyAPI(t *testing.T) {
	r := newRig(t)
	r.run(r.s)
	app := r.app()
	call := func(bearer, method, path, body string) (int, string) {
		t.Helper()
		return apitest.Call(t, app, bearer, method, "/api/v1"+path, body)
	}
	if st, b := call("tok-presence", "GET", "/busy", ""); st != 200 || b != `{"signals":[],"devices":[]}` {
		t.Fatalf("empty: %d %s", st, b)
	}
	st, b := call("tok-presence", "PUT", "/busy/work-mac", `{"reason":"call","devices":["`+r.a+`"],"ttlSeconds":60}`)
	exp := r.clock.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	if st != 200 || b != `{"source":"work-mac","reason":"call","devices":["`+r.a+`"],"expiresAt":"`+exp+`"}` {
		t.Fatalf("put: %d %s", st, b)
	}
	if st, b := call("tok-admin", "PUT", "/busy/dashboard", `{"reason":"dnd","ttlSeconds":86400}`); st != 200 ||
		!strings.Contains(b, `"devices":null`) {
		t.Fatalf("admin put: %d %s", st, b)
	}
	st, b = call("tok-presence", "GET", "/busy", "")
	var got presence.BusyView
	if st != 200 || json.Unmarshal([]byte(b), &got) != nil || len(got.Signals) != 2 || len(got.Devices) != 2 {
		t.Fatalf("list: %d %s", st, b)
	}
	for _, d := range got.Devices {
		if want := map[string]string{r.a: "call", r.b: "dnd"}[d.DeviceID]; d.Reason != want {
			t.Fatalf("device %+v", d)
		}
	}
	if st, b := call("tok-presence", "DELETE", "/busy/work-mac", ""); st != 204 || b != "" {
		t.Fatalf("delete: %d %s", st, b)
	}
	if st, b := call("tok-presence", "DELETE", "/busy/work-mac", ""); st != 404 || b != `{"error":"not_found"}` {
		t.Fatalf("delete again: %d %s", st, b)
	}
	for _, c := range []struct{ path, body, want string }{
		{"/busy/work-mac", `{"reason":"nap","ttlSeconds":60}`, `{"error":"invalid","path":"reason"}`},
		{"/busy/work-mac", `{"reason":"call"}`, `{"error":"invalid","path":"ttlSeconds"}`},
		{"/busy/work-mac", `{"reason":"call","ttlSeconds":86401}`, `{"error":"invalid","path":"ttlSeconds"}`},
		{"/busy/work-mac", `{"reason":"call","ttlSeconds":60,"devices":[]}`, `{"error":"invalid","path":"devices"}`},
		{"/busy/work-mac", `{"reason":"call","ttlSeconds":60,"devices":["nope"]}`, `{"error":"invalid","path":"devices"}`},
		{"/busy/work-mac", `{"reason":"call","ttlSeconds":60,"extra":1}`, `{"error":"invalid","path":""}`},
		{"/busy/work-mac", `{"reason":"call","ttlSeconds":"60"}`, `{"error":"invalid","path":""}`},
		{"/busy/Work", `{"reason":"call","ttlSeconds":60}`, `{"error":"invalid","path":"source"}`},
		{"/busy/ha:sensor.phone", `{"reason":"call","ttlSeconds":60}`, `{"error":"source_reserved","path":"source"}`},
	} {
		if st, b := call("tok-presence", "PUT", c.path, c.body); st != 422 || b != c.want {
			t.Errorf("%s %s: %d %s, want %s", c.path, c.body, st, b, c.want)
		}
	}
	if st, b := call("tok-presence", "DELETE", "/busy/ha:sensor.phone", ""); st != 422 || b != `{"error":"source_reserved","path":"source"}` {
		t.Fatalf("delete ha: %d %s", st, b)
	}
	// the presence scope reaches these routes alone, and the others none of them
	for _, bearer := range []string{"tok-ha", "tok-metrics"} {
		for _, req := range [][3]string{{"GET", "/busy", ""}, {"PUT", "/busy/x", `{"reason":"call","ttlSeconds":5}`}, {"DELETE", "/busy/dashboard", ""}} {
			if st, b := call(bearer, req[0], req[1], req[2]); st != 403 || b != `{"error":"forbidden"}` {
				t.Fatalf("%s %s %s: %d %s", bearer, req[0], req[1], st, b)
			}
		}
	}
	if len(r.s.Signals()) != 1 {
		t.Fatalf("signals %+v", r.s.Signals())
	}
}
