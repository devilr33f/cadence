package api_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

func login(password string, header map[string]string) req {
	h := map[string]string{api.CSRFHeader: "1"}
	for k, v := range header {
		h[k] = v
	}
	return req{method: "POST", path: "/api/v1/session", body: `{"password":"` + password + `"}`, header: h}
}

// setCookies are a response's Set-Cookie headers by name; n is how many it
// must carry.
func setCookies(t *testing.T, got resp, n int) map[string]*http.Cookie {
	t.Helper()
	raw := got.header.Values("Set-Cookie")
	if len(raw) != n {
		t.Fatalf("Set-Cookie %q", raw)
	}
	m := map[string]*http.Cookie{}
	for _, s := range raw {
		c, err := http.ParseSetCookie(s)
		if err != nil {
			t.Fatal(err)
		}
		m[c.Name] = c
	}
	return m
}

// login sets the session cookie, kept to this host and path / (__Host-), and
// the known-browser cookie, sent to the session routes only (Ruling M7a-R2)
func TestLoginSetsTheCookies(t *testing.T) {
	r := newSessionRig(t)
	got := r.do(t, login(rightPassword, nil))
	if got.code != 204 || got.body != "" {
		t.Fatalf("%d %s", got.code, got.body)
	}
	jar := setCookies(t, got, 2)
	s, b := jar["__Host-cadence_session"], jar["__Secure-cadence_browser"]
	if s == nil || s.Value != newToken || !s.HttpOnly || !s.Secure || s.SameSite != http.SameSiteStrictMode ||
		s.Path != "/" || s.MaxAge != 2592000 || s.Domain != "" {
		t.Fatalf("session cookie %+v", s)
	}
	if b == nil || b.Value != newBrowser || !b.HttpOnly || !b.Secure || b.SameSite != http.SameSiteStrictMode ||
		b.Path != "/api/v1/session" || b.MaxAge != 31536000 || b.Domain != "" {
		t.Fatalf("browser cookie %+v", b)
	}
}

// a plain-http dev stack (CADENCE_COOKIE_INSECURE=1) drops Secure and with it
// the prefixes
func TestLoginCookiesInsecure(t *testing.T) {
	r := newCookieRig(t, true)
	jar := setCookies(t, r.do(t, login(rightPassword, nil)), 2)
	s, b := jar["cadence_session"], jar["cadence_browser"]
	if s == nil || b == nil || s.Secure || b.Secure || !s.HttpOnly || !b.HttpOnly || s.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookies %+v %+v", s, b)
	}
}

// each mode reads its own cookie name only: the bare name cannot stand in for
// the __Host- one
func TestSessionCookieNameFollowsTheMode(t *testing.T) {
	secure, insecure := newSessionRig(t), newCookieRig(t, true)
	for _, tc := range []struct {
		rig  *sessionRig
		name string
		code int
	}{
		{secure, "cadence_session", 401},
		{secure, "__Host-cadence_session", 204},
		{insecure, "__Host-cadence_session", 401},
		{insecure, "cadence_session", 204},
	} {
		got := tc.rig.do(t, req{method: "GET", path: "/api/v1/thing", header: map[string]string{"Cookie": tc.name + "=" + liveToken}})
		if got.code != tc.code {
			t.Errorf("%s (insecure %v): %d", tc.name, tc.rig.insecure, got.code)
		}
	}
}

// the known-browser cookie reaches the login, every value of it in order: a
// sibling host can set one of the same name beside the owner's, and the first
// would shadow it
func TestLoginPassesTheKnownBrowser(t *testing.T) {
	r := newSessionRig(t)
	q := login(rightPassword, nil)
	q.browser = oldBrowser
	r.do(t, q)
	r.do(t, login(rightPassword, nil))
	r.do(t, login(rightPassword, map[string]string{"Cookie": "__Secure-cadence_browser=sibling; other=x; __Secure-cadence_browser=" + oldBrowser}))
	r.sessions.mu.Lock()
	defer r.sessions.mu.Unlock()
	if !slices.Equal(r.sessions.browsers, []string{oldBrowser, "", "sibling," + oldBrowser}) {
		t.Fatalf("browsers %q", r.sessions.browsers)
	}
}

// login needs the csrf header and a same-origin source too, whatever else it
// carries, so another site cannot log a browser in (login csrf)
func TestLoginCSRF(t *testing.T) {
	r := newSessionRig(t)
	for _, tc := range []struct {
		name   string
		header map[string]string
		code   int
	}{
		{"no header", map[string]string{api.CSRFHeader: ""}, 403},
		{"header 0", map[string]string{api.CSRFHeader: "0"}, 403},
		{"cross origin", map[string]string{"Origin": "https://evil.example"}, 403},
		{"cross referer", map[string]string{"Referer": "https://evil.example/login"}, 403},
		{"bearer, no header", map[string]string{api.CSRFHeader: "", "Authorization": "Bearer good"}, 403},
		{"same origin", map[string]string{"Origin": "http://example.com"}, 204},
		{"no origin", nil, 204},
	} {
		q := login(rightPassword, tc.header)
		if v, ok := tc.header[api.CSRFHeader]; ok && v == "" {
			delete(q.header, api.CSRFHeader)
		}
		if got := r.do(t, q); got.code != tc.code || (tc.code == 403 && got.body != csrfError) {
			t.Errorf("%s: %d %s", tc.name, got.code, got.body)
		}
	}
	// a path fiber routes to the same operation is login too
	q := login(rightPassword, nil)
	delete(q.header, api.CSRFHeader)
	for _, path := range []string{"/api/v1/session/", "/api/v1/session//", "/api/v1/SESSION"} {
		q.path = path
		if got := r.do(t, q); got.code != 403 {
			t.Errorf("%s: %d %s", path, got.code, got.body)
		}
	}
	if ips, _ := r.sessions.calls(); len(ips) != 2 {
		t.Fatalf("logins tried: %d, want the 2 allowed", len(ips))
	}
	// fiber folds ascii only: a long s (U+017F) is another route, so no login
	if code, body := apitest.Do(t, newSessionRig(t).app, "POST", "/api/v1/ſession", ""); code != 401 {
		t.Fatalf("long s: %d %s", code, body)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	r := newSessionRig(t)
	got := r.do(t, login("a wrong password", nil))
	if got.code != 401 || got.body != `{"error":"unauthorized"}` || got.header.Get("Set-Cookie") != "" {
		t.Fatalf("%d %s %v", got.code, got.body, got.header)
	}
	for _, body := range []string{`{}`, `{"password":12}`, `{"password":"x","user":"owner"}`, `not json`} {
		q := login("", nil)
		q.body = body
		if got := r.do(t, q); got.code != 422 && got.code != 400 {
			t.Errorf("%s: %d %s", body, got.code, got.body)
		}
	}
	if ips, _ := r.sessions.calls(); len(ips) != 1 {
		t.Fatalf("logins tried %d", len(ips))
	}
}

func TestLoginRateLimited(t *testing.T) {
	r := newSessionRig(t)
	r.sessions.loginErr = &auth.RateLimitedError{RetryAfter: 61*time.Second + 200*time.Millisecond}
	got := r.do(t, login(rightPassword, nil))
	if got.code != 429 || got.body != `{"error":"rate_limited"}` || got.header.Get("Retry-After") != "62" || got.header.Get("Set-Cookie") != "" {
		t.Fatalf("%d %s %v", got.code, got.body, got.header)
	}
}

func TestLoginStoreFailureIs500(t *testing.T) {
	r := newSessionRig(t)
	r.sessions.loginErr = errStoreSecret
	got := r.do(t, login(rightPassword, nil))
	if got.code != 500 || got.body != `{"error":"internal"}` {
		t.Fatalf("%d %s", got.code, got.body)
	}
	if strings.Contains(r.logs.String(), "secret") {
		t.Fatalf("log %s", r.logs.String())
	}
}

// the client ip is the socket's, or X-Forwarded-For's rightmost untrusted
// entry when the socket is a trusted proxy
func TestLoginClientIP(t *testing.T) {
	xff := map[string]string{"X-Forwarded-For": "198.51.100.1, 203.0.113.9"}
	direct := newSessionRig(t)
	direct.do(t, login(rightPassword, xff))
	trusted := newSessionRig(t, func(o *api.Options) { o.TrustedProxies = []string{"0.0.0.0"} })
	trusted.do(t, login(rightPassword, xff))
	trusted.do(t, login(rightPassword, nil))
	if ips, _ := direct.sessions.calls(); !slices.Equal(ips, []string{"0.0.0.0"}) {
		t.Fatalf("direct: %q", ips)
	}
	if ips, _ := trusted.sessions.calls(); !slices.Equal(ips, []string{"203.0.113.9", "0.0.0.0"}) {
		t.Fatalf("trusted: %q", ips)
	}
}

func TestSessionState(t *testing.T) {
	r := newSessionRig(t)
	for _, tc := range []struct {
		name, cookie, bearer, want string
	}{
		{"anonymous", "", "", `{"authenticated":false}`},
		{"dead cookie", deadToken, "", `{"authenticated":false}`},
		{"bad bearer", "", "bad", `{"authenticated":false}`},
		{"cookie", liveToken, "", `{"authenticated":true,"expiresAt":"2026-12-11T12:00:00Z"}`},
		{"bearer", "", "good", `{"authenticated":true}`},
	} {
		q := req{method: "GET", path: "/api/v1/session", cookie: tc.cookie, header: map[string]string{}}
		if tc.bearer != "" {
			q.header["Authorization"] = "Bearer " + tc.bearer
		}
		if got := r.do(t, q); got.code != 200 || got.body != tc.want {
			t.Errorf("%s: %d %s", tc.name, got.code, got.body)
		}
	}
}

func TestLogout(t *testing.T) {
	r := newSessionRig(t)
	if got := r.do(t, req{method: "DELETE", path: "/api/v1/session"}); got.code != 401 {
		t.Fatalf("anonymous: %d %s", got.code, got.body)
	}
	if got := r.do(t, req{method: "DELETE", path: "/api/v1/session", cookie: liveToken}); got.code != 403 || got.body != csrfError {
		t.Fatalf("no csrf header: %d %s", got.code, got.body)
	}
	if _, outs := r.sessions.calls(); len(outs) != 0 {
		t.Fatalf("revoked %q before a valid logout", outs)
	}
	got := r.do(t, req{method: "DELETE", path: "/api/v1/session", cookie: liveToken, header: map[string]string{api.CSRFHeader: "1"}})
	if got.code != 204 {
		t.Fatalf("logout: %d %s", got.code, got.body)
	}
	c := setCookies(t, got, 1)["__Host-cadence_session"]
	if c == nil || c.Value != "" || c.MaxAge != -1 || !c.HttpOnly || !c.Secure || c.Path != "/" {
		t.Fatalf("cleared cookie %+v", c)
	}
	got = r.do(t, req{method: "DELETE", path: "/api/v1/session", header: map[string]string{"Authorization": "Bearer good"}})
	if got.code != 204 || setCookies(t, got, 1)["__Host-cadence_session"].MaxAge != -1 {
		t.Fatalf("bearer logout: %d %s", got.code, got.body)
	}
	if _, outs := r.sessions.calls(); !slices.Equal(outs, []string{liveToken}) {
		t.Fatalf("revoked %q", outs)
	}
}

// only login and the state answer without credentials
func TestSessionRoutesOtherwiseNeedAuth(t *testing.T) {
	r := newSessionRig(t)
	for _, m := range []string{"PUT", "PATCH", "HEAD", "DELETE"} {
		if got := r.do(t, req{method: m, path: "/api/v1/session", header: map[string]string{api.CSRFHeader: "1"}}); got.code != 401 {
			t.Errorf("%s: %d %s", m, got.code, got.body)
		}
	}
}

// nothing secret reaches a log or a body: not the password, the token, the
// cookie or the csrf header
func TestSessionLogsAndBodiesCarryNoSecrets(t *testing.T) {
	r := newSessionRig(t)
	var bodies []string
	for _, q := range []req{
		login(rightPassword, nil),
		login("a wrong password", nil),
		{method: "GET", path: "/api/v1/session", cookie: liveToken},
		{method: "POST", path: "/api/v1/thing", cookie: liveToken},
		{method: "GET", path: "/api/v1/thing", cookie: "boom"},
		{method: "DELETE", path: "/api/v1/session", cookie: liveToken, header: map[string]string{api.CSRFHeader: "1"}},
	} {
		bodies = append(bodies, r.do(t, q).body)
	}
	out := r.logs.String() + strings.Join(bodies, "\n")
	for _, s := range []string{rightPassword, "wrong password", newToken, newBrowser, liveToken, "cadence_session", "cadence_browser", api.CSRFHeader, "boom"} {
		if strings.Contains(out, s) {
			t.Fatalf("carries %q: %s", s, out)
		}
	}
}
