package xweb

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
	"git.shino.boo/devilreef/cadence/server/internal/providers/netscape"
)

const cookiesTxt = "# Netscape HTTP Cookie File\n" +
	".x.com\tTRUE\t/\tTRUE\t1893456000\tauth_token\tAUTH-SECRET-1\n" +
	".x.com\tTRUE\t/\tTRUE\t1893456000\tct0\tCSRF-SECRET-2\n" +
	".youtube.com\tTRUE\t/\tTRUE\t1893456000\tSID\tNOT-FOR-X\n"

// fakeX serves the web app's page and scripts and the search; search answers each search call in turn
type fakeX struct {
	// setCookies are sent with the home page
	setCookies []*http.Cookie
	t          *testing.T
	srv        *httptest.Server
	homes      atomic.Int32
	search     func(w http.ResponseWriter, r *http.Request)
	lastReq    *http.Request
}

func newFakeX(t *testing.T) *fakeX {
	t.Helper()
	f := &fakeX{t: t}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/home":
			f.homes.Add(1)
			for _, k := range f.setCookies {
				http.SetCookie(w, k)
			}
			if !strings.Contains(r.Header.Get("Cookie"), "auth_token=AUTH-SECRET-1") {
				http.Redirect(w, r, "/i/flow/login", http.StatusFound)
				return
			}
			_, _ = w.Write([]byte(fixture(t, "home.html")))
		case r.URL.Path == "/responsive-web/client-web/ondemand.s.5f2e7a1b9ca.js":
			_, _ = w.Write([]byte(fixture(t, "ondemand.js")))
		case r.URL.Path == "/responsive-web/client-web/main.0f1e2d3c.js":
			_, _ = w.Write([]byte(fixture(t, "main.js")))
		case strings.HasPrefix(r.URL.Path, "/i/api/graphql/"):
			f.lastReq = r.Clone(context.Background())
			f.search(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	f.search = func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(fixture(t, "search.json"))) }
	return f
}

func (f *fakeX) client(t *testing.T, cookies string, logs *bytes.Buffer) *Client {
	t.Helper()
	file, err := netscape.Parse(cookies)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(f.srv.Certificate())
	c, err := New(Config{BaseURL: f.srv.URL, AssetsURL: f.srv.URL, Cookies: file,
		Log: slog.New(slog.NewTextHandler(logs, nil))}, httpx.Client(pool))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSearchSignsInAndReadsThePosts(t *testing.T) {
	f := newFakeX(t)
	var logs bytes.Buffer
	c := f.client(t, cookiesTxt, &logs)
	posts, err := c.SearchPosts(context.Background(), `starship from:spacex -filter:replies`, true, 5)
	if err != nil {
		t.Fatal(err)
	}
	r := f.lastReq
	if r.URL.Path != "/i/api/graphql/Q1w2E3r4T5y6/SearchTimeline" {
		t.Fatalf("path %s", r.URL.Path)
	}
	var vars map[string]any
	_ = json.Unmarshal([]byte(r.URL.Query().Get("variables")), &vars)
	if vars["rawQuery"] != `starship from:spacex -filter:replies` || vars["product"] != "Latest" || vars["count"] != 5.0 {
		t.Fatalf("variables %v", vars)
	}
	if got := r.URL.Query().Get("features"); got != `{"not_in_state":false,"responsive_web_graphql_timeline_navigation_enabled":true,"rweb_video_screen_enabled":false,"view_counts_everywhere_api_enabled":true}` {
		t.Fatalf("features %s", got)
	}
	if r.URL.Query().Get("fieldToggles") != `{"withArticleRichContentState":false}` {
		t.Fatalf("toggles %s", r.URL.Query().Get("fieldToggles"))
	}
	h := r.Header
	if h.Get("Authorization") != "Bearer AAAAAAAAAAAAAAAAAAAAAFAKEbearerFORtests%3Dabc123" || h.Get("X-Csrf-Token") != "CSRF-SECRET-2" ||
		h.Get("Cookie") != "auth_token=AUTH-SECRET-1; ct0=CSRF-SECRET-2" || h.Get("X-Twitter-Auth-Type") != "OAuth2Session" ||
		len(h.Get("X-Client-Transaction-Id")) < 80 {
		t.Fatalf("headers %v", h)
	}

	want := []providers.Post{
		{URL: "https://x.com/alice/status/101", Author: "Alice", Handle: "@alice", Time: "2026-11-12T18:04:05Z",
			Text: "A long post about Starship & launches, see https://example.com/b", Likes: 10, Reposts: 3, Replies: 2, Views: 1200,
			Note:  "Readers added context: the launch was delayed.",
			Quote: &providers.Post{URL: "https://x.com/bob/status/50", Author: "Bob", Handle: "@bob", Text: "the quoted post", Time: "2026-11-12T18:04:05Z", Likes: 10, Reposts: 3, Replies: 2, Views: 1200}},
		{URL: "https://x.com/carol/status/102", Author: "Carol", Handle: "@carol", Text: "limited visibility post", Time: "2026-11-12T18:04:05Z", Likes: 10, Reposts: 3, Replies: 2},
		{URL: "https://x.com/dave/status/40", Author: "Dave", Handle: "@dave", Text: "the original post", Time: "2026-11-12T18:04:05Z", Likes: 10, Reposts: 3, Replies: 2, Views: 1200, RepostedBy: "@erin"},
		{URL: "https://x.com/frank/status/104", Author: "Frank", Handle: "@frank", Text: "@alice replying here", Time: "2026-11-12T18:04:05Z", Likes: 10, Reposts: 3, Replies: 2, Views: 1200, ReplyTo: "@alice"},
	}
	got, _ := json.Marshal(posts)
	exp, _ := json.Marshal(want)
	if string(got) != string(exp) {
		t.Fatalf("posts\n%s\nwant\n%s", got, exp)
	}
	// the session is kept: a second search reads no page
	if _, err := c.SearchPosts(context.Background(), "q", false, 1); err != nil || f.homes.Load() != 1 {
		t.Fatalf("second search: %v, %d home reads", err, f.homes.Load())
	}
}

// a retired query id, changed features or a refused signature reads the web app again, once
func TestStaleSessionIsReadAgainOnce(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusBadRequest, http.StatusForbidden} {
		f := newFakeX(t)
		var calls atomic.Int32
		f.search = func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				w.WriteHeader(status)
				return
			}
			_, _ = w.Write([]byte(fixture(t, "search.json")))
		}
		var logs bytes.Buffer
		if _, err := f.client(t, cookiesTxt, &logs).SearchPosts(context.Background(), "q", false, 3); err != nil || f.homes.Load() != 2 {
			t.Fatalf("%d: %v, %d home reads", status, err, f.homes.Load())
		}
	}
}

func TestFailuresAreLoggedByCodeAlone(t *testing.T) {
	for _, c := range []struct {
		name, code string
		search     func(w http.ResponseWriter, r *http.Request)
		cookies    string
	}{
		{"rate limit", "rate_limited", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }, cookiesTxt},
		{"stays stale", "status_404", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }, cookiesTxt},
		{"not json", "decode", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }, cookiesTxt},
		{"signed out", "signed_out", nil, strings.ReplaceAll(cookiesTxt, "AUTH-SECRET-1", "EXPIRED-SECRET-3")},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeX(t)
			if c.search != nil {
				f.search = c.search
			}
			var logs bytes.Buffer
			_, err := f.client(t, c.cookies, &logs).SearchPosts(context.Background(), "secret query text", false, 3)
			if err == nil || err.Error() != providers.CodeXSearchFailed {
				t.Fatalf("err %v", err)
			}
			if !strings.Contains(logs.String(), "msg=x_search_failed provider=x-web code="+c.code) {
				t.Fatalf("log %q", logs.String())
			}
			for _, leak := range []string{"secret query", "SECRET-1", "SECRET-2", "SECRET-3"} {
				if strings.Contains(logs.String(), leak) {
					t.Fatalf("%q reached the log", leak)
				}
			}
		})
	}
}

func TestCookiesNeedTheSession(t *testing.T) {
	file, _ := netscape.Parse(".x.com\tTRUE\t/\tTRUE\t1893456000\tauth_token\tA\n")
	if _, err := New(Config{Cookies: file}, nil); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("no ct0: %v", err)
	}
	file, _ = netscape.Parse(".youtube.com\tTRUE\t/\tTRUE\t1893456000\tauth_token\tA\n.youtube.com\tTRUE\t/\tTRUE\t1\tct0\tB\n")
	if _, err := New(Config{Cookies: file}, nil); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("another site's: %v", err)
	}
	if _, err := Module(nil).NewSaving(nil, map[string]string{"cookies": "not cookies"}, nil); !errors.Is(err, ErrBadCookies) {
		t.Fatalf("garbage: %v", err)
	}
}

func TestProbeSaysWhetherTheCookiesSignIn(t *testing.T) {
	f := newFakeX(t)
	var logs bytes.Buffer
	if err := probe(context.Background(), f.client(t, cookiesTxt, &logs)); err != nil {
		t.Fatalf("signed in: %v", err)
	}
	if err := probe(context.Background(), f.client(t, strings.ReplaceAll(cookiesTxt, "AUTH-SECRET-1", "X"), &logs)); !errors.Is(err, providers.ProbeAuth) {
		t.Fatalf("signed out: %v", err)
	}
}

func TestSessionIsReadAgainWhenOld(t *testing.T) {
	f := newFakeX(t)
	var logs bytes.Buffer
	c := f.client(t, cookiesTxt, &logs)
	now := time.Now()
	c.cfg.Now = func() time.Time { return now }
	_, _ = c.SearchPosts(context.Background(), "q", false, 1)
	now = now.Add(sessionTTL + time.Second)
	_, _ = c.SearchPosts(context.Background(), "q", false, 1)
	if f.homes.Load() != 2 {
		t.Fatalf("%d home reads", f.homes.Load())
	}
}

// a cookie x renews on its answers (ct0 rotates) is sent from the next request on and saved; x clearing auth_token
// is ignored, and a cookie another site names is not taken
func TestRenewedCookiesAreUsedAndSaved(t *testing.T) {
	f := newFakeX(t)
	f.setCookies = []*http.Cookie{
		{Name: "ct0", Value: "CSRF-ROTATED-4", Domain: ".x.com", Path: "/", MaxAge: 3600},
		{Name: "auth_token", Value: "", Domain: ".x.com", Path: "/", MaxAge: -1},
		{Name: "theirs", Value: "NOT-OURS", Domain: ".example.com", Path: "/"},
	}
	var logs bytes.Buffer
	c := f.client(t, cookiesTxt, &logs)
	saved := make(chan string, 4)
	c.cfg.Save = func(_ context.Context, key, value string) error {
		saved <- key + "=" + value
		return nil
	}
	if _, err := c.SearchPosts(context.Background(), "q", false, 1); err != nil {
		t.Fatal(err)
	}
	if h := f.lastReq.Header; h.Get("X-Csrf-Token") != "CSRF-ROTATED-4" ||
		h.Get("Cookie") != "auth_token=AUTH-SECRET-1; ct0=CSRF-ROTATED-4" {
		t.Fatalf("search sent %q / %q", h.Get("X-Csrf-Token"), h.Get("Cookie"))
	}
	select {
	case got := <-saved:
		jar := netscape.For([]byte(strings.TrimPrefix(got, "cookies=")), "x.com")
		if !strings.HasPrefix(got, "cookies=") || jar["ct0"] != "CSRF-ROTATED-4" || jar["auth_token"] != "AUTH-SECRET-1" || jar["theirs"] != "" {
			t.Fatalf("saved %v", jar)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("never saved")
	}
	for _, leak := range []string{"ROTATED", "SECRET"} {
		if strings.Contains(logs.String(), leak) {
			t.Fatalf("%q reached the log", leak)
		}
	}
}
