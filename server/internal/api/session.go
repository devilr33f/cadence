package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humafiber"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

const (
	adminPrefix = "/api/v1"
	sessionPath = "/session"
	sessionTag  = "session"
)

// Caller, embedded in an operation's input, hands the handler what the auth
// middleware found, the request's session and known-browser cookies and its
// client ip.
type Caller struct {
	p        *principal
	insecure bool
	session  string
	// every value of the known-browser cookie, in the order sent: a sibling
	// host can set one of that name beside the owner's, so the first may not
	// be it
	browsers []string
	ip       string
}

// Resolve reads the caller off the request.
func (r *Caller) Resolve(ctx huma.Context) []error {
	c := humafiber.Unwrap(ctx)
	if st, ok := c.Locals(stateKey{}).(*authState); ok {
		r.p, r.insecure = st.p, st.insecure
	}
	r.session = c.Cookies(SessionCookieName(r.insecure))
	name := BrowserCookieName(r.insecure)
	for k, v := range c.Request().Header.Cookies() {
		if string(k) == name && len(v) > 0 {
			r.browsers = append(r.browsers, string(v))
		}
	}
	r.ip = c.IP()
	return nil
}

// Scopes are what the caller may do: nil for an anonymous one, admin for the
// owner's session.
func (r *Caller) Scopes() auth.Scopes {
	if r.p == nil {
		return nil
	}
	return r.p.scopes
}

// MCPTools are the MCP tools the caller's token names, empty for every tool.
func (r *Caller) MCPTools() []string {
	if r.p == nil {
		return nil
	}
	return r.p.mcpTools
}

// cookie is one of the login's cookies, Secure but on a plain-http dev stack.
func (r *Caller) cookie(name, value, path string, maxAge time.Duration) http.Cookie {
	age := int(maxAge / time.Second)
	if maxAge < 0 {
		age = -1
	}
	return http.Cookie{ //nolint:gosec // Secure is off only on a plain-http dev stack
		Name: name, Value: value, Path: path, MaxAge: age,
		HttpOnly: true, Secure: !r.insecure, SameSite: http.SameSiteStrictMode,
	}
}

// LoginRequest is the owner's password.
type LoginRequest struct {
	Password string `json:"password" doc:"The owner's password."`
}

type loginIn struct {
	Caller
	Body LoginRequest
}

type cookieOut struct {
	SetCookie []http.Cookie `header:"Set-Cookie" doc:"Login sets the session cookie (__Host-cadence_session, Path=/) and the known-browser cookie (__Secure-cadence_browser, Path=/api/v1/session, a year), both HttpOnly, Secure and SameSite=Strict (on a plain-http dev stack cadence_session and cadence_browser, without Secure). Logout clears the session cookie."`
}

type stateIn struct{ Caller }

// SessionState is whether a request is authenticated, and until when a
// cookie session lasts at most.
type SessionState struct {
	Authenticated bool       `json:"authenticated"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty" doc:"The cookie session's absolute expiry; absent for an API token, which does not expire."`
}

type stateOut struct{ Body SessionState }

type logoutIn struct{ Caller }

var noAuth = []map[string][]string{}

var sessionDocs = map[string]Doc{
	"create-session": {Summary: "Log in as the owner: sets the session cookie"},
	"get-session":    {Summary: "Tell whether the request is authenticated"},
	"delete-session": {Summary: "Log out: revoke the cookie's session and clear the cookie"},
}

// RegisterSession mounts the owner's login (M7a-D4): POST and GET answer
// without credentials, DELETE needs them.
func RegisterSession(a huma.API, s Sessions) {
	defer Describe(a, sessionDocs)
	huma.Register(a, huma.Operation{
		OperationID: "create-session", Method: http.MethodPost, Path: sessionPath, Tags: []string{sessionTag},
		DefaultStatus: http.StatusNoContent, Security: noAuth,
		Description: fmt.Sprintf("Needs the header %s: 1 and, when it sends Origin or Referer, the server's own origin. "+
			"Failures count per client ip (an ipv6 one by its /64; %d) and in all (%d) over %d minutes; past either the answer is 429 with Retry-After. "+
			"A login with a valid known-browser cookie counts in that browser's own bucket instead (%d), and the browser's %dth failed login over its life forgets it; "+
			"a successful one replaces it with a fresh one.",
			CSRFHeader, auth.LoginFailuresPerIP, auth.LoginFailures, int(auth.LoginWindow/time.Minute), auth.LoginFailuresPerIP, auth.BrowserFailures),
	}, func(ctx context.Context, in *loginIn) (*cookieOut, error) {
		g, err := s.Login(ctx, in.ip, in.browsers, in.Body.Password)
		var limited *auth.RateLimitedError
		switch {
		case errors.As(err, &limited):
			wait := strconv.Itoa(int(math.Ceil(limited.RetryAfter.Seconds())))
			return nil, huma.ErrorWithHeaders(Error(http.StatusTooManyRequests, "rate_limited"), http.Header{"Retry-After": {wait}})
		case errors.Is(err, auth.ErrUnauthorized):
			return nil, Error(http.StatusUnauthorized, "unauthorized")
		case err != nil:
			return nil, Error(http.StatusInternalServerError, "internal")
		}
		return &cookieOut{SetCookie: []http.Cookie{
			in.cookie(SessionCookieName(in.insecure), g.Session.Token, "/", auth.SessionLifetime),
			in.cookie(BrowserCookieName(in.insecure), g.Browser, adminPrefix+sessionPath, auth.BrowserLifetime),
		}}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "get-session", Method: http.MethodGet, Path: sessionPath, Tags: []string{sessionTag}, Security: noAuth,
		Description: "Answers without credentials too: authenticated is false unless a valid API token or session cookie came.",
	}, func(_ context.Context, in *stateIn) (*stateOut, error) {
		out := &stateOut{}
		if in.p != nil {
			out.Body.Authenticated = true
			if in.p.cookie {
				expires := in.p.expires.UTC()
				out.Body.ExpiresAt = &expires
			}
		}
		return out, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "delete-session", Method: http.MethodDelete, Path: sessionPath, Tags: []string{sessionTag},
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *logoutIn) (*cookieOut, error) {
		if in.session != "" {
			if err := s.Logout(ctx, in.session); err != nil {
				return nil, Error(http.StatusInternalServerError, "internal")
			}
		}
		return &cookieOut{SetCookie: []http.Cookie{in.cookie(SessionCookieName(in.insecure), "", "/", -1)}}, nil
	})
}
