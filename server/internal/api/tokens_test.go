package api_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

const issuedToken = "cdn_issuedtokenvalue"

var tokenCreated = time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

// fakeTokens holds tokens by name and checks them as auth.Tokens does.
type fakeTokens struct {
	mu      sync.Mutex
	names   map[string]auth.Scopes
	used    *time.Time
	fail    bool
	created []string
}

func (f *fakeTokens) List(context.Context) ([]auth.TokenInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errStoreSecret
	}
	var out []auth.TokenInfo
	for _, n := range []string{"ha", "owner"} {
		if s, ok := f.names[n]; ok {
			info := auth.TokenInfo{Name: n, Scopes: s, CreatedAt: tokenCreated}
			if n == "ha" {
				info.LastUsedAt = f.used
			}
			out = append(out, info)
		}
	}
	return out, nil
}

func (f *fakeTokens) SetMCPTools(context.Context, string, []string) error { return nil }

func (f *fakeTokens) Create(_ context.Context, name string, scopes, _ []string) (string, auth.Scopes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return "", nil, errStoreSecret
	}
	if _, taken := f.names[name]; taken {
		return "", nil, auth.ErrTokenNameTaken
	}
	s, err := auth.ParseScopes(scopes)
	if err != nil {
		return "", nil, err
	}
	f.names[name] = s
	f.created = append(f.created, name)
	return issuedToken, s, nil
}

func (f *fakeTokens) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errStoreSecret
	}
	if _, ok := f.names[name]; !ok {
		return auth.ErrTokenNotFound
	}
	delete(f.names, name)
	return nil
}

func newTokensRig(t *testing.T) (*scopeRig, *fakeTokens) {
	t.Helper()
	used := tokenCreated.Add(time.Hour)
	f := &fakeTokens{names: map[string]auth.Scopes{"owner": auth.AdminScopes, "ha": {auth.ScopeHA}}, used: &used}
	r := &scopeRig{seen: make(chan auth.Scopes, 1)}
	r.sessionRig = newSessionRig(t, func(o *api.Options) {
		o.VerifyToken = verifyScoped
		log := o.Log
		o.Admin = append(o.Admin, func(a huma.API) { api.RegisterTokens(a, f, nil, log) })
	})
	return r, f
}

// the document's name pattern and scope list are the ones auth checks, and
// the answers list the same scopes
func TestCreateTokenSchemaMatchesAuth(t *testing.T) {
	r, _ := newTokensRig(t)
	got := r.send(t, "tok-adm", "GET", "/api/v1/openapi.json", "")
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Pattern string `json:"pattern"`
					Items   struct {
						Enum []string `json:"enum"`
					} `json:"items"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal([]byte(got.body), &doc); err != nil {
		t.Fatal(err)
	}
	known := auth.Scopes(auth.KnownScopes).Strings()
	s := doc.Components.Schemas["CreateTokenRequest"]
	if s.Properties["name"].Pattern != auth.TokenNamePattern || !slices.Equal(s.Properties["scopes"].Items.Enum, known) {
		t.Fatalf("schema %+v", s)
	}
	for _, name := range []string{"TokenView", "CreatedToken"} {
		if got := doc.Components.Schemas[name].Properties["scopes"].Items.Enum; !slices.Equal(got, known) {
			t.Errorf("%s scopes enum %v", name, got)
		}
	}
}

func (r *scopeRig) send(t *testing.T, bearer, method, path, body string) resp {
	t.Helper()
	return r.do(t, req{method: method, path: path, body: body, header: map[string]string{"Authorization": "Bearer " + bearer}})
}

// the list shows names, scopes and times, never a token or its hash
func TestListTokens(t *testing.T) {
	r, _ := newTokensRig(t)
	got := r.send(t, "tok-adm", "GET", "/api/v1/tokens", "")
	want := `[{"name":"ha","scopes":["ha"],"createdAt":"2026-11-11T09:00:00Z","lastUsedAt":"2026-11-11T10:00:00Z"},` +
		`{"name":"owner","scopes":["admin"],"createdAt":"2026-11-11T09:00:00Z"}]`
	if got.code != 200 || got.body != want {
		t.Fatalf("%d %s", got.code, got.body)
	}
}

// create answers the token once, with the scopes as stored
func TestCreateToken(t *testing.T) {
	r, f := newTokensRig(t)
	got := r.send(t, "tok-adm", "POST", "/api/v1/tokens", `{"name":"home-assistant","scopes":["metrics","ha"]}`)
	if got.code != 201 || got.body != `{"name":"home-assistant","scopes":["ha","metrics"],"token":"`+issuedToken+`"}` {
		t.Fatalf("%d %s", got.code, got.body)
	}
	// the one answer that carries the token is never cached
	if cc := got.header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control %q", cc)
	}
	if !slices.Equal(f.created, []string{"home-assistant"}) {
		t.Fatalf("created %v", f.created)
	}
	if strings.Contains(r.logs.String(), issuedToken) {
		t.Fatalf("log %s", r.logs.String())
	}
}

// a bad name or scope list is refused at its field, a taken name as such,
// and nothing is created
func TestCreateTokenRefuses(t *testing.T) {
	r, f := newTokensRig(t)
	for _, tc := range []struct {
		body string
		code int
		want string
	}{
		{`{"name":"Bad Name","scopes":["ha"]}`, 422, `{"error":"invalid","path":"/body/name"}`},
		{`{"name":"","scopes":["ha"]}`, 422, `{"error":"invalid","path":"/body/name"}`},
		{`{"name":"` + strings.Repeat("a", 65) + `","scopes":["ha"]}`, 422, `{"error":"invalid","path":"/body/name"}`},
		{`{"name":"x","scopes":["root"]}`, 422, `{"error":"invalid","path":"/body/scopes/0"}`},
		{`{"name":"x","scopes":["ha","ha"]}`, 422, `{"error":"invalid","path":"/body/scopes"}`},
		{`{"name":"x","scopes":[]}`, 422, `{"error":"invalid","path":"/body/scopes"}`},
		{`{"name":"x","scopes":null}`, 422, `{"error":"invalid","path":"/body/scopes"}`},
		{`{"name":"x"}`, 422, `{"error":"invalid","path":"/body"}`},
		{`{"name":"x","scopes":["ha"],"token":"cdn_mine"}`, 422, `{"error":"invalid","path":"/body/token"}`},
		{`{"name":"owner","scopes":["ha"]}`, 409, `{"error":"name_taken"}`},
	} {
		if got := r.send(t, "tok-adm", "POST", "/api/v1/tokens", tc.body); got.code != tc.code || got.body != tc.want {
			t.Errorf("%s: %d %s, want %d %s", tc.body, got.code, got.body, tc.code, tc.want)
		}
	}
	if len(f.created) != 0 {
		t.Fatalf("created %v", f.created)
	}
}

func TestDeleteToken(t *testing.T) {
	r, f := newTokensRig(t)
	if got := r.send(t, "tok-adm", "DELETE", "/api/v1/tokens/ha", ""); got.code != 204 {
		t.Fatalf("%d %s", got.code, got.body)
	}
	if _, ok := f.names["ha"]; ok {
		t.Fatal("not deleted")
	}
	if got := r.send(t, "tok-adm", "DELETE", "/api/v1/tokens/ha", ""); got.code != 404 || got.body != `{"error":"not_found"}` {
		t.Fatalf("%d %s", got.code, got.body)
	}
}

// a name from before the name pattern, with a space or a slash, is deleted by
// its escaped form; a malformed escape names no token
func TestDeleteTokenUnescapesTheName(t *testing.T) {
	r, f := newTokensRig(t)
	f.names["old token"], f.names["home/assistant"] = auth.AdminScopes, auth.Scopes{auth.ScopeHA}
	for _, p := range []string{"/api/v1/tokens/old%20token", "/api/v1/tokens/home%2Fassistant"} {
		if got := r.send(t, "tok-adm", "DELETE", p, ""); got.code != 204 {
			t.Fatalf("%s: %d %s", p, got.code, got.body)
		}
	}
	if _, ok := f.names["old token"]; ok || f.names["home/assistant"] != nil {
		t.Fatalf("names %v", f.names)
	}
	if code, body := apitest.Do(t, r.app, "DELETE", "/api/v1/tokens/bad%zz", "tok-adm"); code != 404 || body != `{"error":"not_found"}` {
		t.Fatalf("%d %s", code, body)
	}
}

// a store failure is a bare 500: the cause stays out of the body and the log
func TestTokenStoreFailure(t *testing.T) {
	r, f := newTokensRig(t)
	f.fail = true
	for _, q := range [][3]string{{"GET", "/api/v1/tokens", ""}, {"POST", "/api/v1/tokens", `{"name":"x","scopes":["ha"]}`}, {"DELETE", "/api/v1/tokens/ha", ""}} {
		if got := r.send(t, "tok-adm", q[0], q[1], q[2]); got.code != 500 || got.body != `{"error":"internal"}` {
			t.Fatalf("%s %s: %d %s", q[0], q[1], got.code, got.body)
		}
	}
	if out := r.logs.String(); strings.Contains(out, "secret") || !strings.Contains(out, `"msg":"api_tokens_failed"`) {
		t.Fatalf("log %s", out)
	}
}

// only admin manages tokens: an ha token cannot mint itself a wider one
func TestTokensNeedAdmin(t *testing.T) {
	r, f := newTokensRig(t)
	for _, q := range [][3]string{{"GET", "/api/v1/tokens", ""}, {"POST", "/api/v1/tokens", `{"name":"x","scopes":["admin"]}`}, {"DELETE", "/api/v1/tokens/owner", ""}} {
		for _, bearer := range []string{"tok-ha", "tok-met", "tok-both"} {
			if got := r.send(t, bearer, q[0], q[1], q[2]); got.code != 403 || got.body != `{"error":"forbidden"}` {
				t.Fatalf("%s %s %s: %d %s", bearer, q[0], q[1], got.code, got.body)
			}
		}
	}
	if len(f.created) != 0 || len(f.names) != 2 {
		t.Fatalf("created %v names %v", f.created, f.names)
	}
}
