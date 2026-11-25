package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/config"
)

type grantingLogin struct{}

func (grantingLogin) Login(context.Context, string, []string, string) (auth.Grant, error) {
	return auth.Grant{Session: auth.Session{Token: "s", ExpiresAt: time.Now()}, Browser: "b"}, nil
}

func (grantingLogin) Validate(context.Context, string) (auth.Session, error) {
	return auth.Session{}, auth.ErrUnauthorized
}

func (grantingLogin) Logout(context.Context, string) error { return nil }

// CADENCE_COOKIE_INSECURE reaches the login's cookies: Secure and the prefixes
// only without it
func TestCookieInsecureReachesTheCookies(t *testing.T) {
	for v, want := range map[string]struct {
		names  []string
		secure bool
	}{
		"":  {[]string{"__Host-cadence_session", "__Secure-cadence_browser"}, true},
		"1": {[]string{"cadence_browser", "cadence_session"}, false},
	} {
		env := map[string]string{"DATABASE_URL": "postgres://u:p@h/db", "CADENCE_MASTER_KEY_FILE": "/k", "CADENCE_COOKIE_INSECURE": v}
		cfg, err := config.Load(func(k string) string { return env[k] })
		if err != nil {
			t.Fatal(err)
		}
		login := grantingLogin{}
		app := api.NewApp(withLogin(api.Options{DB: nopPinger{}, Version: "t", Admin: adminOps(adminDeps{login: login})}, cfg, login))
		req := httptest.NewRequest("POST", "/api/v1/session", strings.NewReader(`{"password":"x"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(api.CSRFHeader, "1")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		var names []string
		for _, c := range resp.Cookies() {
			names = append(names, c.Name)
			if c.Secure != want.secure {
				t.Errorf("%q: %s secure %v", v, c.Name, c.Secure)
			}
		}
		slices.Sort(names)
		if resp.StatusCode != http.StatusNoContent || !slices.Equal(names, want.names) {
			t.Fatalf("%q: %d cookies %q", v, resp.StatusCode, names)
		}
	}
}
