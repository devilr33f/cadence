package api_test

import (
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

const csrfError = `{"error":"csrf"}`

// an admin request authenticates by bearer or by the session cookie; the
// bearer wins when both are sent, and never falls back to the cookie
func TestBearerOrCookie(t *testing.T) {
	r := newSessionRig(t)
	for _, tc := range []struct {
		name, bearer, cookie string
		code                 int
	}{
		{"bearer", "good", "", 204},
		{"cookie", "", liveToken, 204},
		{"dead cookie", "", deadToken, 401},
		{"no credentials", "", "", 401},
		{"bearer wins over a dead cookie", "good", deadToken, 204},
		{"a bad bearer is not saved by a live cookie", "bad", liveToken, 401},
	} {
		q := req{method: "GET", path: "/api/v1/thing", cookie: tc.cookie, header: map[string]string{}}
		if tc.bearer != "" {
			q.header["Authorization"] = "Bearer " + tc.bearer
		}
		got := r.do(t, q)
		if got.code != tc.code || (tc.code == 401 && got.body != `{"error":"unauthorized"}`) {
			t.Errorf("%s: %d %s", tc.name, got.code, got.body)
		}
	}
}

// without a session service the cookie is not read
func TestCookieNeedsSessions(t *testing.T) {
	r := newSessionRig(t, func(o *api.Options) { o.Sessions = nil })
	if got := r.do(t, req{method: "GET", path: "/api/v1/thing", cookie: liveToken}); got.code != 401 {
		t.Fatalf("%d %s", got.code, got.body)
	}
}

func TestCookieOutageIs503AndLogsNoCause(t *testing.T) {
	r := newSessionRig(t)
	got := r.do(t, req{method: "GET", path: "/api/v1/thing", cookie: "boom"})
	if got.code != 503 || got.body != `{"error":"unavailable"}` {
		t.Fatalf("%d %s", got.code, got.body)
	}
	if out := r.logs.String(); !strings.Contains(out, `"msg":"auth_unavailable"`) || strings.Contains(out, "password") || strings.Contains(out, "boom") {
		t.Fatalf("log %s", out)
	}
}

type source struct {
	name, origin, referer string
	same                  bool
}

// the rig's requests go to http://example.com
var sources = []source{
	{"none", "", "", true},
	{"origin same", "http://example.com", "", true},
	{"origin same, upper case", "HTTP://EXAMPLE.COM", "", true},
	{"origin same, default port", "http://example.com:80", "", true},
	{"origin same, cross referer", "http://example.com", "https://evil.example/x", true},
	{"origin cross", "https://evil.example", "", false},
	{"origin cross, same referer", "https://evil.example", "http://example.com/dash", false},
	{"origin other scheme", "https://example.com", "", false},
	{"origin other port", "http://example.com:8080", "", false},
	{"origin null", "null", "", false},
	{"origin junk", "::", "", false},
	{"referer same", "", "http://example.com/dash?tab=1", true},
	{"referer cross", "", "https://evil.example/x", false},
	{"referer subdomain", "", "http://a.example.com/", false},
}

func (s source) set(h map[string]string) {
	if s.origin != "" {
		h["Origin"] = s.origin
	}
	if s.referer != "" {
		h["Referer"] = s.referer
	}
}

func unsafe(method string) bool { return method != "GET" && method != "HEAD" && method != "OPTIONS" }

// a cookie request that may change something carries X-Cadence-CSRF: 1 and,
// when it has an Origin (else a Referer), the server's own origin; a bearer
// request is exempt (M7a-D5)
func TestCSRFMatrix(t *testing.T) {
	r := newSessionRig(t)
	for _, via := range []string{"cookie", "bearer"} {
		for _, method := range methods {
			for _, csrf := range []string{"", "1", "0", "true"} {
				for _, src := range sources {
					h := map[string]string{}
					q := req{method: method, path: "/api/v1/thing", header: h}
					if via == "cookie" {
						q.cookie = liveToken
					} else {
						h["Authorization"] = "Bearer good"
					}
					if csrf != "" {
						h[api.CSRFHeader] = csrf
					}
					src.set(h)
					want := 204
					if via == "cookie" && unsafe(method) && (csrf != "1" || !src.same) {
						want = 403
					}
					got := r.do(t, q)
					if got.code != want || (want == 403 && got.body != csrfError) {
						t.Errorf("%s %s csrf=%q %s: %d %s", via, method, csrf, src.name, got.code, got.body)
					}
				}
			}
		}
	}
}

// CADENCE_PUBLIC_ORIGIN replaces the request's own scheme and host
func TestCSRFPublicOrigin(t *testing.T) {
	r := newSessionRig(t, func(o *api.Options) { o.Origin = "https://dash.example" })
	for _, src := range []source{
		{"configured", "https://dash.example", "", true},
		{"configured, default port", "https://dash.example:443", "", true},
		{"configured referer", "", "https://dash.example/x", true},
		{"the request's own host", "http://example.com", "", false},
		{"none", "", "", true},
	} {
		h := map[string]string{api.CSRFHeader: "1"}
		src.set(h)
		want := 204
		if !src.same {
			want = 403
		}
		if got := r.do(t, req{method: "POST", path: "/api/v1/thing", header: h, cookie: liveToken}); got.code != want {
			t.Errorf("%s: %d %s", src.name, got.code, got.body)
		}
	}
}

// behind a trusted proxy the request's own origin is the forwarded scheme and
// host; from anywhere else those headers are ignored
func TestCSRFForwardedOrigin(t *testing.T) {
	h := map[string]string{api.CSRFHeader: "1", "Origin": "https://dash.example", "X-Forwarded-Proto": "https", "X-Forwarded-Host": "dash.example"}
	trusted := newSessionRig(t, func(o *api.Options) { o.TrustedProxies = []string{"0.0.0.0"} })
	if got := trusted.do(t, req{method: "POST", path: "/api/v1/thing", header: h, cookie: liveToken}); got.code != 204 {
		t.Fatalf("trusted proxy: %d %s", got.code, got.body)
	}
	direct := newSessionRig(t)
	if got := direct.do(t, req{method: "POST", path: "/api/v1/thing", header: h, cookie: liveToken}); got.code != 403 {
		t.Fatalf("untrusted: %d %s", got.code, got.body)
	}
}

// a websocket upgrade on the cookie needs a same-origin Origin (a browser
// always sends one); the bearer's is unchanged
func TestCookieUpgradeNeedsSameOrigin(t *testing.T) {
	r := newSessionRig(t)
	upgrade := map[string]string{"Connection": "Upgrade", "Upgrade": "websocket"}
	for _, tc := range []struct {
		name, origin, cookie, bearer string
		forbidden                    bool
	}{
		{"cookie, cross origin", "https://evil.example", liveToken, "", true},
		{"cookie, no origin", "", liveToken, "", true},
		{"cookie, same origin", "http://example.com", liveToken, "", false},
		{"bearer, cross origin", "https://evil.example", "", "good", false},
		{"bearer, no origin", "", "", "good", false},
	} {
		h := map[string]string{}
		for k, v := range upgrade {
			h[k] = v
		}
		if tc.origin != "" {
			h["Origin"] = tc.origin
		}
		if tc.bearer != "" {
			h["Authorization"] = "Bearer " + tc.bearer
		}
		got := r.do(t, req{method: "GET", path: "/api/v1/thing", header: h, cookie: tc.cookie})
		if (got.code == 403) != tc.forbidden || (tc.forbidden && got.body != csrfError) {
			t.Errorf("%s: %d %s", tc.name, got.code, got.body)
		}
	}
}
