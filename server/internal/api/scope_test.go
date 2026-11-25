package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

// scopedTokens are the rig's bearers: each a token of these scopes
var scopedTokens = map[string]auth.Scopes{
	"tok-adm":  auth.AdminScopes,
	"tok-ha":   {auth.ScopeHA},
	"tok-met":  {auth.ScopeMetrics},
	"tok-both": {auth.ScopeHA, auth.ScopeMetrics},
}

func verifyScoped(_ context.Context, bearer string) (auth.Grants, error) {
	if s, ok := scopedTokens[bearer]; ok {
		return auth.Grants{Scopes: s}, nil
	}
	return auth.Grants{}, auth.ErrUnauthorized
}

type scopeRig struct {
	*sessionRig
	// the scopes the live route's handler saw
	seen chan auth.Scopes
}

// newScopeRig serves /thing (no scope declared: admin), /open (declared ha),
// a plain /plain route that needs metrics, a plain /bare one that names no
// scope, and the session operations.
func newScopeRig(t *testing.T) *scopeRig {
	t.Helper()
	r := &scopeRig{seen: make(chan auth.Scopes, 1)}
	r.sessionRig = newSessionRig(t, func(o *api.Options) {
		o.VerifyToken = verifyScoped
		o.Admin = append(o.Admin, func(a huma.API) {
			defer api.Describe(a, map[string]api.Doc{"get-open": {Summary: "open", Scope: auth.ScopeHA}})
			huma.Register(a, huma.Operation{OperationID: "get-open", Method: "GET", Path: "/open", DefaultStatus: 204},
				func(context.Context, *struct{}) (*thingOut, error) { return &thingOut{}, nil })
		})
		seen := func(c fiber.Ctx) error {
			r.seen <- api.CallerScopes(c)
			return c.SendStatus(204)
		}
		o.Plain = []api.PlainRoute{
			{Op: "get-plain", Method: "GET", Path: "/plain", Scopes: []auth.Scope{auth.ScopeMetrics}, Handler: seen},
			{Op: "get-bare", Method: "GET", Path: "/bare", Handler: seen},
		}
	})
	return r
}

func (r *scopeRig) as(t *testing.T, bearer, method, path string) resp {
	t.Helper()
	q := req{method: method, path: path, header: map[string]string{api.CSRFHeader: "1"}}
	switch bearer {
	case "cookie":
		q.cookie = liveToken
	case "":
	default:
		q.header["Authorization"] = "Bearer " + bearer
	}
	return r.do(t, q)
}

// an operation that declares no scope is admin's alone; ha reaches only what
// declares it; the owner's session is admin; a refusal is a detail-free 403
// (M7a-D6)
func TestScopeEnforcement(t *testing.T) {
	r := newScopeRig(t)
	for _, tc := range []struct {
		bearer, method, path string
		allowed              bool
	}{
		{"tok-adm", "GET", "/api/v1/thing", true}, {"tok-adm", "DELETE", "/api/v1/thing", true}, {"tok-adm", "GET", "/api/v1/open", true},
		{"cookie", "POST", "/api/v1/thing", true}, {"cookie", "GET", "/api/v1/open", true},
		{"tok-ha", "GET", "/api/v1/open", true}, {"tok-ha", "GET", "/api/v1/thing", false}, {"tok-ha", "POST", "/api/v1/thing", false},
		{"tok-ha", "GET", "/api/v1/openapi.json", false}, {"tok-ha", "DELETE", "/api/v1/session", false},
		{"tok-met", "GET", "/api/v1/open", false}, {"tok-met", "GET", "/api/v1/thing", false},
		{"tok-both", "GET", "/api/v1/open", true}, {"tok-both", "PUT", "/api/v1/thing", false},
		{"tok-adm", "GET", "/api/v1/openapi.json", true}, {"cookie", "GET", "/api/v1/openapi.json", true},
	} {
		got := r.as(t, tc.bearer, tc.method, tc.path)
		switch {
		case tc.allowed && (got.code == 403 || got.code >= 300):
			t.Errorf("%s %s %s: %d %s", tc.bearer, tc.method, tc.path, got.code, got.body)
		case !tc.allowed && (got.code != 403 || got.body != `{"error":"forbidden"}`):
			t.Errorf("%s %s %s: %d %s, want 403 forbidden", tc.bearer, tc.method, tc.path, got.code, got.body)
		}
	}
	out := r.logs.String()
	if !strings.Contains(out, `"msg":"api_forbidden","op":"thing-get","scope":"admin"`) ||
		!strings.Contains(out, `"msg":"api_forbidden","op":"get-open","scope":"ha"`) {
		t.Fatalf("log %s", out)
	}
	for bearer := range scopedTokens {
		if strings.Contains(out, `"`+bearer+`"`) || strings.Contains(out, "Bearer") {
			t.Fatalf("log carries a bearer: %s", out)
		}
	}
}

// login and the session state need no scope: an ha bearer gets the state and
// may log in like anyone
func TestScopeLeavesTheSessionRoutesOpen(t *testing.T) {
	r := newScopeRig(t)
	if got := r.as(t, "tok-ha", "GET", "/api/v1/session"); got.code != 200 || got.body != `{"authenticated":true}` {
		t.Fatalf("state %d %s", got.code, got.body)
	}
	got := r.do(t, req{method: "POST", path: "/api/v1/session", body: `{"password":"` + rightPassword + `"}`,
		header: map[string]string{api.CSRFHeader: "1", "Authorization": "Bearer tok-met"}})
	if got.code != 204 {
		t.Fatalf("login %d %s", got.code, got.body)
	}
	if got := r.as(t, "", "GET", "/api/v1/open"); got.code != 401 {
		t.Fatalf("anonymous %d %s", got.code, got.body)
	}
}

// a route that is no huma operation names its scope itself; its handler can
// read the caller's scopes
func TestRequireScopeOnPlainRoutes(t *testing.T) {
	r := newScopeRig(t)
	for _, tc := range []struct {
		bearer string
		code   int
		seen   auth.Scopes
	}{
		{"tok-met", 204, auth.Scopes{auth.ScopeMetrics}},
		{"tok-both", 204, auth.Scopes{auth.ScopeHA, auth.ScopeMetrics}},
		{"tok-adm", 204, auth.AdminScopes},
		{"cookie", 204, auth.AdminScopes},
		{"tok-ha", 403, nil},
		{"", 401, nil},
	} {
		got := r.as(t, tc.bearer, "GET", "/api/v1/plain")
		if got.code != tc.code {
			t.Fatalf("%q: %d %s", tc.bearer, got.code, got.body)
		}
		if tc.code == 403 && got.body != `{"error":"forbidden"}` {
			t.Fatalf("%q: %s", tc.bearer, got.body)
		}
		if tc.code != 204 {
			continue
		}
		if s := <-r.seen; strings.Join(s.Strings(), ",") != strings.Join(tc.seen.Strings(), ",") {
			t.Fatalf("%q: handler saw %v", tc.bearer, s)
		}
	}
	if !strings.Contains(r.logs.String(), `"msg":"api_forbidden","op":"get-plain","scope":"metrics"`) {
		t.Fatalf("log %s", r.logs.String())
	}
	// a plain route that names no scope is admin's alone
	for _, bearer := range []string{"tok-ha", "tok-met", "tok-both"} {
		if got := r.as(t, bearer, "GET", "/api/v1/bare"); got.code != 403 || got.body != `{"error":"forbidden"}` {
			t.Fatalf("%s: %d %s", bearer, got.code, got.body)
		}
	}
	if got := r.as(t, "tok-adm", "GET", "/api/v1/bare"); got.code != 204 {
		t.Fatalf("admin: %d %s", got.code, got.body)
	}
	<-r.seen
	if !strings.Contains(r.logs.String(), `"msg":"api_forbidden","op":"get-bare","scope":"admin"`) {
		t.Fatalf("log %s", r.logs.String())
	}
}

// the gate skips only an open operation reached as login or the session
// state: an operation at that path that is not documented open needs its
// scope, and credentials
func TestScopeGateSkipsOnlyOpenOperations(t *testing.T) {
	o := adminOptions()
	o.VerifyToken = verifyScoped
	o.Admin = []func(huma.API){func(a huma.API) {
		huma.Register(a, huma.Operation{OperationID: "get-session-alike", Method: "GET", Path: "/session", DefaultStatus: 204},
			func(context.Context, *struct{}) (*thingOut, error) { return &thingOut{}, nil })
	}}
	app := api.NewApp(o)
	for _, tc := range []struct {
		bearer string
		code   int
	}{{"", 401}, {"tok-ha", 403}, {"tok-adm", 204}} {
		q := httptest.NewRequest("GET", "/api/v1/session", nil)
		if tc.bearer != "" {
			q.Header.Set("Authorization", "Bearer "+tc.bearer)
		}
		resp, err := app.Test(q)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.code {
			t.Fatalf("%q: %d, want %d", tc.bearer, resp.StatusCode, tc.code)
		}
	}
}

// the document names each operation's scope; login and the state need none
func TestSpecNamesScopes(t *testing.T) {
	r := newScopeRig(t)
	got := r.as(t, "tok-adm", "GET", "/api/v1/openapi.json")
	var doc struct {
		Paths map[string]map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal([]byte(got.body), &doc); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]map[string]any{
		"/thing":   {"get": "admin", "post": "admin"},
		"/open":    {"get": "ha"},
		"/session": {"get": nil, "post": nil, "delete": "admin"},
	} {
		for method, scope := range want {
			if s := doc.Paths[path][method]["x-cadence-scope"]; s != scope {
				t.Errorf("%s %s: scope %v, want %v", method, path, s, scope)
			}
		}
	}
	if strings.Contains(got.body, "openapi.json") {
		t.Fatal("the document lists its own route")
	}
}

// huma serves only openapi.json: its yaml, 3.0 and schema siblings are gone
func TestSpecSiblingsAreNotServed(t *testing.T) {
	r := newScopeRig(t)
	for _, path := range []string{"/api/v1/openapi.yaml", "/api/v1/openapi-3.0.json", "/api/v1/openapi-3.0.yaml", "/api/v1/schemas/ErrorBody.json"} {
		if got := r.as(t, "tok-adm", "GET", path); got.code != 404 {
			t.Errorf("%s: %d", path, got.code)
		}
	}
	if got := r.as(t, "tok-adm", "GET", "/api/v1/openapi.json"); got.code != 200 || got.header.Get("Content-Type") != "application/openapi+json" {
		t.Fatalf("%d %q", got.code, got.header.Get("Content-Type"))
	}
}

// a doc that names an unknown scope fails at startup
func TestDescribeRefusesAnUnknownScope(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	api.NewApp(api.Options{DB: fakePinger{}, Version: "t", Admin: []func(huma.API){func(a huma.API) {
		defer api.Describe(a, map[string]api.Doc{"get-x": {Summary: "x", Scope: "root"}})
		huma.Register(a, huma.Operation{OperationID: "get-x", Method: "GET", Path: "/x"},
			func(context.Context, *struct{}) (*thingOut, error) { return &thingOut{}, nil })
	}}})
}

// the token behind a 403 never reaches the log, nor a request body
func TestForbiddenLogsNoRequest(t *testing.T) {
	var logs bytes.Buffer
	o := adminOptions()
	o.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	o.VerifyToken = verifyScoped
	req := httptest.NewRequest("GET", "/api/v1/ping?secret=q", strings.NewReader(`{"secret":"b"}`))
	req.Header.Set("Authorization", "Bearer tok-ha")
	resp, err := api.NewApp(o).Test(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 403 || string(b) != `{"error":"forbidden"}` {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if out := logs.String(); strings.Contains(out, "secret") || !strings.Contains(out, `"scope":"admin"`) {
		t.Fatalf("log %s", out)
	}
}
