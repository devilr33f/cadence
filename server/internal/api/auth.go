package api

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

const (
	// SessionCookie holds the owner's session token: __Host- keeps it to this
	// host, over https, on Path=/.
	SessionCookie = "__Host-cadence_session"
	// BrowserCookie holds the owner's known-browser token (Ruling M7a-R2), sent
	// to the session routes only.
	BrowserCookie = "__Secure-cadence_browser"
	// on a plain-http dev stack the cookies drop Secure, and with it the
	// prefixes, which browsers refuse without it
	insecureSessionCookie = "cadence_session"
	insecureBrowserCookie = "cadence_browser"
	// CSRFHeader must be "1" on a cookie request that may change something.
	CSRFHeader = "X-Cadence-CSRF"
)

// SessionCookieName is the session cookie's name; insecure is a plain-http
// dev stack (CADENCE_COOKIE_INSECURE=1).
func SessionCookieName(insecure bool) string {
	if insecure {
		return insecureSessionCookie
	}
	return SessionCookie
}

// BrowserCookieName is the known-browser cookie's name.
func BrowserCookieName(insecure bool) string {
	if insecure {
		return insecureBrowserCookie
	}
	return BrowserCookie
}

// Sessions is the owner's login behind the cookies.
type Sessions interface {
	Login(ctx context.Context, ip string, browsers []string, password string) (auth.Grant, error)
	Validate(ctx context.Context, token string) (auth.Session, error)
	Logout(ctx context.Context, token string) error
}

// principal is how an admin request authenticated, and what it may do.
type principal struct {
	cookie bool
	// the cookie session's absolute expiry
	expires time.Time
	scopes  auth.Scopes
	// mcpTools is the token's MCP tool list, empty for every tool; a cookie
	// session is admin's, which every tool follows
	mcpTools []string
	// recheck asks the store again whether the credential still holds with
	// these scopes, for a connection that outlives its request
	recheck func(context.Context) error
}

// authState is what the middleware leaves a request: its principal, nil when
// anonymous, whether it is login or the session state (which need neither
// credentials nor a scope), and the cookie rules the session operations write
// by.
type authState struct {
	p        *principal
	public   bool
	insecure bool
}

type stateKey struct{}

type authConfig struct {
	verify   func(ctx context.Context, bearer string) (auth.Grants, error)
	sessions Sessions
	// the configured origin; empty: the request's own
	origin   string
	insecure bool
	log      *slog.Logger
}

// handler authenticates every request on the admin group by its bearer or,
// without an Authorization header, its session cookie (M7a-D4). A cookie
// request is held to the csrf rules (M7a-D5); login always is. Login and the
// session state answer without credentials.
func (a authConfig) handler(c fiber.Ctx) error {
	login, public := sessionRoute(c)
	if login && !a.sameSite(c) {
		return refuse(c, fiber.StatusForbidden, "csrf")
	}
	p, err := a.identify(c)
	switch {
	case err != nil && !errors.Is(err, auth.ErrUnauthorized):
		// the cause can carry driver text, so only the event is logged
		if a.log != nil {
			a.log.Error("auth_unavailable")
		}
		return refuse(c, fiber.StatusServiceUnavailable, "unavailable")
	case err != nil && public:
		c.Locals(stateKey{}, &authState{public: true, insecure: a.insecure})
		return c.Next()
	case err != nil:
		return refuse(c, fiber.StatusUnauthorized, "unauthorized")
	}
	if p.cookie {
		if !login && unsafeMethod(c.Method()) && !a.sameSite(c) {
			return refuse(c, fiber.StatusForbidden, "csrf")
		}
		// a browser always sends Origin on a websocket handshake, and no
		// csrf header can ride on one
		if asksUpgrade(c) && !a.sameOrigin(c.Get(fiber.HeaderOrigin), c) {
			return refuse(c, fiber.StatusForbidden, "csrf")
		}
	}
	c.Locals(stateKey{}, &authState{p: p, public: public, insecure: a.insecure})
	return c.Next()
}

func (a authConfig) identify(c fiber.Ctx) (*principal, error) {
	if h := c.Get(fiber.HeaderAuthorization); h != "" {
		tok, ok := strings.CutPrefix(h, "Bearer ")
		if !ok || tok == "" || a.verify == nil {
			return nil, auth.ErrUnauthorized
		}
		grants, err := a.verify(c.Context(), tok)
		if err != nil {
			return nil, err
		}
		if len(grants.Scopes) == 0 {
			return nil, auth.ErrUnauthorized
		}
		return &principal{scopes: grants.Scopes, mcpTools: grants.MCPTools, recheck: func(ctx context.Context) error {
			now, err := a.verify(ctx, tok)
			switch {
			case err != nil:
				return err
			case !slices.Equal(now.Scopes, grants.Scopes) || !slices.Equal(now.MCPTools, grants.MCPTools):
				return auth.ErrUnauthorized
			}
			return nil
		}}, nil
	}
	tok := c.Cookies(SessionCookieName(a.insecure))
	if tok == "" || a.sessions == nil {
		return nil, auth.ErrUnauthorized
	}
	s, err := a.sessions.Validate(c.Context(), tok)
	if err != nil {
		return nil, err
	}
	// validating again sees a logout, the expiry, idleness and a password
	// change, which revokes every session
	return &principal{cookie: true, expires: s.ExpiresAt, scopes: auth.AdminScopes, recheck: func(ctx context.Context) error {
		_, err := a.sessions.Validate(ctx, tok)
		return err
	}}, nil
}

// sessionRoute tells login (POST) and the session state (GET) apart, matched
// as fiber routes them: ascii case folded, trailing slashes stripped.
func sessionRoute(c fiber.Ctx) (login, public bool) {
	if !asciiEqualFold(strings.TrimRight(c.Path(), "/"), adminPrefix+sessionPath) {
		return false, false
	}
	m := c.Method()
	return m == fiber.MethodPost, m == fiber.MethodPost || m == fiber.MethodGet
}

// asciiEqualFold folds A-Z only, as fiber's router does; strings.EqualFold
// would also match non-ascii letters such as U+017F to "s".
func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	lower := func(c byte) byte {
		if 'A' <= c && c <= 'Z' {
			return c + 'a' - 'A'
		}
		return c
	}
	for i := range len(a) {
		if lower(a[i]) != lower(b[i]) {
			return false
		}
	}
	return true
}

func unsafeMethod(m string) bool {
	return m != fiber.MethodGet && m != fiber.MethodHead && m != fiber.MethodOptions
}

// asksUpgrade is any upgrade signal, as the websocket handler reads it, so a
// partial handshake is held to the rule too.
func asksUpgrade(c fiber.Ctx) bool {
	if c.Get(fiber.HeaderUpgrade) != "" {
		return true
	}
	for tok := range strings.SplitSeq(c.Get(fiber.HeaderConnection), ",") {
		if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
			return true
		}
	}
	return false
}

// sameSite is the csrf check: the header, and the Origin (else the Referer),
// when sent, of the server's own origin.
func (a authConfig) sameSite(c fiber.Ctx) bool {
	if c.Get(CSRFHeader) != "1" {
		return false
	}
	src := c.Get(fiber.HeaderOrigin)
	if src == "" {
		src = c.Get(fiber.HeaderReferer)
	}
	return src == "" || a.sameOrigin(src, c)
}

func (a authConfig) sameOrigin(src string, c fiber.Ctx) bool {
	got, ok := originOf(src)
	if !ok {
		return false
	}
	want := a.origin
	if want == "" {
		// scheme and host follow X-Forwarded-* only from a trusted proxy
		want, ok = originOf(c.Scheme() + "://" + c.Host())
	}
	return ok && got == want
}

// originOf is a url's origin as a browser serializes it: scheme://host[:port],
// lowercase, without a default port.
func originOf(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if host == "" {
		return "", false
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port == "" || (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		return scheme + "://" + host, true
	}
	return scheme + "://" + host + ":" + port, true
}

// refuse answers the error model as huma's errors do: application/json, no
// charset.
func refuse(c fiber.Ctx, status int, code string) error {
	return c.Status(status).JSON(fiber.Map{"error": code}, fiber.MIMEApplicationJSON)
}
