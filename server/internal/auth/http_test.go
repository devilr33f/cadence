package auth_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
)

type nopPinger struct{}

func (nopPinger) Ping(context.Context) error { return nil }

// the real login behind the api: the password opens a cookie session that
// authenticates and a known browser the next login carries; logout ends the
// session and keeps the browser
func TestOwnerLoginOverHTTP(t *testing.T) {
	r := newLogin(t)
	app := api.NewApp(api.Options{
		DB: nopPinger{}, Version: "t", Sessions: r.l,
		VerifyToken: apitest.Refuse,
		Admin:       []func(huma.API){func(a huma.API) { api.RegisterSession(a, r.l) }},
	})
	send := func(method, body string, cookies ...*http.Cookie) *http.Response {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1/session", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(api.CSRFHeader, "1")
		for _, c := range cookies {
			req.AddCookie(c)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}
	jar := func(resp *http.Response) map[string]*http.Cookie {
		m := map[string]*http.Cookie{}
		for _, c := range resp.Cookies() {
			m[c.Name] = c
		}
		return m
	}
	if resp := send("POST", `{"password":"wrong wrong wrong"}`); resp.StatusCode != 401 {
		t.Fatalf("wrong password: %d", resp.StatusCode)
	}
	first := jar(send("POST", `{"password":"`+ownerPassword+`"}`))
	session, browser := first[api.SessionCookie], first[api.BrowserCookie]
	if session == nil || browser == nil || len(session.Value) != 43 || len(browser.Value) != 43 {
		t.Fatalf("login cookies %v", first)
	}
	state := func() string {
		b, _ := io.ReadAll(send("GET", "", session).Body)
		return string(b)
	}
	if got := state(); !strings.HasPrefix(got, `{"authenticated":true,"expiresAt":"2026-12-11T12:00:00Z"`) {
		t.Fatalf("state %s", got)
	}
	if resp := send("DELETE", "", session); resp.StatusCode != 204 || jar(resp)[api.BrowserCookie] != nil {
		t.Fatalf("logout: %d %v", resp.StatusCode, resp.Cookies())
	}
	if got := state(); got != `{"authenticated":false}` {
		t.Fatalf("after logout %s", got)
	}
	again := jar(send("POST", `{"password":"`+ownerPassword+`"}`, browser))
	if again[api.BrowserCookie] == nil || r.known(t, browser.Value) || !r.known(t, again[api.BrowserCookie].Value) {
		t.Fatal("the second login did not replace the known browser")
	}
	noSecrets(t, r.logs.String(), ownerPassword, session.Value, browser.Value)
}
