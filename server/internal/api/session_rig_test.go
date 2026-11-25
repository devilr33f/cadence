package api_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

const (
	rightPassword = "the right password"
	liveToken     = "live-session-token"
	deadToken     = "dead-session-token"
	newToken      = "fresh-session-token"
	newBrowser    = "fresh-browser-token"
	oldBrowser    = "old-browser-token"
)

var errStoreSecret = errors.New("driver says secret")

var sessionExpires = time.Date(2026, 12, 11, 12, 0, 0, 0, time.UTC)

// fakeSessions answers the owner login: one live cookie token, one right
// password, and an outage for the token "boom".
type fakeSessions struct {
	mu       sync.Mutex
	loginErr error
	ips      []string
	browsers []string
	logouts  []string
}

func (f *fakeSessions) Login(_ context.Context, ip string, browsers []string, password string) (auth.Grant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ips = append(f.ips, ip)
	f.browsers = append(f.browsers, strings.Join(browsers, ","))
	switch {
	case f.loginErr != nil:
		return auth.Grant{}, f.loginErr
	case password != rightPassword:
		return auth.Grant{}, auth.ErrUnauthorized
	}
	return auth.Grant{Session: auth.Session{Token: newToken, ExpiresAt: sessionExpires}, Browser: newBrowser}, nil
}

func (f *fakeSessions) Validate(_ context.Context, token string) (auth.Session, error) {
	switch token {
	case liveToken:
		return auth.Session{ExpiresAt: sessionExpires}, nil
	case "boom":
		return auth.Session{}, errors.New("password authentication failed for user cadence")
	}
	return auth.Session{}, auth.ErrUnauthorized
}

func (f *fakeSessions) Logout(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logouts = append(f.logouts, token)
	return nil
}

func (f *fakeSessions) calls() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ips...), append([]string(nil), f.logouts...)
}

type thingOut struct{}

// sessionRig is an admin app with cookie auth, the session operations and a
// /thing operation on every method.
type sessionRig struct {
	sessions *fakeSessions
	logs     *bytes.Buffer
	app      *fiber.App
	insecure bool
}

func newSessionRig(t *testing.T, edit ...func(*api.Options)) *sessionRig {
	t.Helper()
	return newCookieRig(t, false, edit...)
}

// newCookieRig is newSessionRig with the cookie's Secure left off when insecure.
func newCookieRig(t *testing.T, insecure bool, edit ...func(*api.Options)) *sessionRig {
	t.Helper()
	r := &sessionRig{sessions: &fakeSessions{}, logs: &bytes.Buffer{}, insecure: insecure}
	o := adminOptions()
	o.Log = slog.New(slog.NewJSONHandler(r.logs, nil))
	o.Sessions, o.CookieInsecure = r.sessions, insecure
	o.Admin = append(o.Admin, func(a huma.API) { api.RegisterSession(a, r.sessions) }, func(a huma.API) {
		for _, m := range methods {
			huma.Register(a, huma.Operation{OperationID: "thing-" + strings.ToLower(m), Method: m, Path: "/thing", DefaultStatus: 204},
				func(context.Context, *struct{}) (*thingOut, error) { return &thingOut{}, nil })
		}
	})
	for _, e := range edit {
		e(&o)
	}
	r.app = api.NewApp(o)
	return r
}

var methods = []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

type req struct {
	method, path, body string
	header             map[string]string
	// the session and known-browser cookies, under the rig's names
	cookie, browser string
}

type resp struct {
	code   int
	body   string
	header http.Header
}

func (r *sessionRig) do(t *testing.T, q req) resp {
	t.Helper()
	hr := httptest.NewRequest(q.method, q.path, strings.NewReader(q.body))
	if q.body != "" {
		hr.Header.Set("Content-Type", "application/json")
	}
	for k, v := range q.header {
		hr.Header.Set(k, v)
	}
	if q.cookie != "" {
		hr.AddCookie(&http.Cookie{Name: api.SessionCookieName(r.insecure), Value: q.cookie})
	}
	if q.browser != "" {
		hr.AddCookie(&http.Cookie{Name: api.BrowserCookieName(r.insecure), Value: q.browser})
	}
	res, err := r.app.Test(hr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, strings.TrimSpace(string(b)), res.Header}
}
