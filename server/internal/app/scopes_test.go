package app

import (
	"context"
	"encoding/json"
	"maps"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

// haOperations are exactly the operations an ha token may call (M7a-D6):
// reading devices, their media, cover art (M8-D4) and vitals, the commands
// haCommands names and announcements (M8-D3), and the catalog reads and the
// play the Home Assistant media browser reaches, and the multiroom groups' reads with a device's join and
// leave (Home Assistant's grouping). Widening it is a deliberate
// diff here.
var haOperations = []string{
	"announce-to-device", "browse-catalog", "get-catalog-alternatives", "get-catalog-image", "get-catalog-item",
	"get-catalog-options", "get-device", "get-device-artwork", "get-device-catalog-session", "get-device-media",
	"get-device-vitals", "get-group", "join-device-group", "leave-device-group", "list-catalog-sources", "list-devices",
	"list-groups", "play-catalog", "search-catalog", "select-device-catalog", "send-device-command",
}

// presenceOperations are exactly the operations a presence token may call:
// busy mode's signals, nothing else.
var presenceOperations = []string{"delete-busy", "get-busy", "put-busy"}

// haCommands are exactly the capability commands an ha token may send
// (Ruling M7a-R4, M8-D5), and a ring's stop and snooze, a timer and a
// reminder without a level (owner, 2026-11-16); any other needs admin.
var haCommands = map[string][]string{
	"alarms":       {"stop_ring", "snooze", "set_timer", "set_reminder"},
	"audio.output": {"set_level", "change_level"},
	"input.remote": {"key"},
	"media.player": {"pause", "resume", "stop", "seek", "next", "previous"},
	"media.url":    {"play"},
	"media.video":  {"play"},
}

// plainScopes are the scopes besides admin each plain admin route takes: the
// live feed's ha, the Prometheus exposition's metrics.
// the artifact upload carries bytes, not a huma body; admin scope alone (an owner publishes builds)
var plainScopes = map[string][]auth.Scope{"live": {auth.ScopeHA}, "metrics": {auth.ScopeMetrics}, "upload-device-artifact": nil}

// openOperations answer without credentials, so they need no scope.
var openOperations = []string{"create-session", "get-session"}

func TestHACommandAllowList(t *testing.T) {
	if got := commands.HACommands(); !maps.EqualFunc(got, haCommands, slices.Equal) {
		t.Fatalf("ha commands %v, want %v", got, haCommands)
	}
}

// ownerLogin takes one session cookie, the owner's.
type ownerLogin struct{ grantingLogin }

func (ownerLogin) Validate(_ context.Context, token string) (auth.Session, error) {
	if token == "owner-cookie" {
		return auth.Session{ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	return auth.Session{}, auth.ErrUnauthorized
}

var matrixTokens = map[string]auth.Scopes{
	"tok-admin": auth.AdminScopes, "tok-ha": {auth.ScopeHA}, "tok-metrics": {auth.ScopeMetrics},
	"tok-ha-metrics": {auth.ScopeHA, auth.ScopeMetrics}, "tok-all": {auth.ScopeAdmin, auth.ScopeHA, auth.ScopeMetrics, auth.ScopePresence},
	"tok-presence": {auth.ScopePresence},
}

// every operation and plain route, for every kind of caller, through the real
// router: the scope gate lets the caller through (a stub then answers 299
// where the handler would run) or refuses it with a bare 403
func TestScopeMatrix(t *testing.T) {
	passed := func(a huma.API) {
		a.UseMiddleware(func(ctx huma.Context, _ func(huma.Context)) { ctx.SetStatus(299) })
	}
	plain := adminPlain(events.NewFeed(events.NewBus(0), nil), telemetry.NewProm(telemetry.PromDeps{}),
		api.PlainRoute{Op: "upload-device-artifact", Method: fiber.MethodPost, Path: "/device-artifacts"})
	for i := range plain {
		plain[i].Handler = func(c fiber.Ctx) error { return c.SendStatus(299) }
	}
	app := api.NewApp(api.Options{
		DB: nopPinger{}, Version: "t", Sessions: ownerLogin{},
		VerifyToken: func(_ context.Context, bearer string) (auth.Grants, error) {
			if s, ok := matrixTokens[bearer]; ok {
				return auth.Grants{Scopes: s}, nil
			}
			return auth.Grants{}, auth.ErrUnauthorized
		},
		Admin: append([]func(huma.API){passed}, adminOps(adminDeps{})...),
		Plain: plain,
	})
	_, doc := generated(t)
	param := regexp.MustCompile(`\{[^}]+\}`)
	type route struct {
		id, method, path string
		// for a plain route, its pinned scopes
		plain []auth.Scope
	}
	routes := []route{{id: "get-openapi", method: "GET", path: "/openapi.json"}}
	for path, ops := range doc.Paths {
		for method, op := range ops {
			routes = append(routes, route{id: op.OperationID, method: strings.ToUpper(method), path: param.ReplaceAllString(path, "x")})
		}
	}
	if len(plain) != len(plainScopes) {
		t.Fatalf("%d plain routes, want %d", len(plain), len(plainScopes))
	}
	for _, p := range plain {
		want, ok := plainScopes[p.Op]
		if !ok || !slices.Equal(p.Scopes, want) {
			t.Fatalf("plain %s: scopes %v, want %v", p.Op, p.Scopes, want)
		}
		routes = append(routes, route{id: p.Op, method: p.Method, path: p.Path, plain: want})
	}
	for _, caller := range []string{"cookie", "tok-admin", "tok-all", "tok-ha", "tok-ha-metrics", "tok-metrics", "tok-presence"} {
		var allowedHA, allowedPresence []string
		for _, r := range routes {
			req := httptest.NewRequest(r.method, "/api/v1"+r.path, strings.NewReader(`{}`))
			req.Header.Set(api.CSRFHeader, "1")
			scopes := auth.AdminScopes
			if caller == "cookie" {
				req.Header.Set("Cookie", api.SessionCookie+"=owner-cookie")
			} else {
				req.Header.Set("Authorization", "Bearer "+caller)
				scopes = matrixTokens[caller]
			}
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&body)
			_ = resp.Body.Close()
			open := slices.Contains(openOperations, r.id)
			allowed := open || scopes.Allows(auth.ScopeAdmin) || (scopes.Allows(auth.ScopeHA) && slices.Contains(haOperations, r.id)) ||
				(scopes.Allows(auth.ScopePresence) && slices.Contains(presenceOperations, r.id))
			if r.plain != nil {
				allowed = scopes.Allows(auth.ScopeAdmin) || slices.ContainsFunc(r.plain, scopes.Allows)
			}
			switch {
			case allowed && resp.StatusCode != 299:
				t.Errorf("%s %s (%s): %d %v, want through", caller, r.id, r.path, resp.StatusCode, body)
			case !allowed && (resp.StatusCode != 403 || len(body) != 1 || body["error"] != "forbidden"):
				t.Errorf("%s %s (%s): %d %v, want 403 forbidden", caller, r.id, r.path, resp.StatusCode, body)
			}
			if caller == "tok-ha" && resp.StatusCode == 299 && !open && r.plain == nil {
				allowedHA = append(allowedHA, r.id)
			}
			if caller == "tok-presence" && resp.StatusCode == 299 && !open {
				allowedPresence = append(allowedPresence, r.id)
			}
		}
		if caller == "tok-presence" {
			slices.Sort(allowedPresence)
			if !slices.Equal(allowedPresence, presenceOperations) {
				t.Fatalf("presence reaches %v, want %v", allowedPresence, presenceOperations)
			}
		}
		if caller == "tok-ha" {
			slices.Sort(allowedHA)
			if !slices.Equal(allowedHA, haOperations) {
				t.Fatalf("ha reaches %v, want %v", allowedHA, haOperations)
			}
		}
	}
}
