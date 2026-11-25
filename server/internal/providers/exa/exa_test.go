package exa_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/exa"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

const upstreamLeak = "LEAKY-UPSTREAM-TEXT"

func roots(srv *httptest.Server) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(srv.Certificate())
	return p
}

func newSearch(srv *httptest.Server) providers.Search {
	return exa.New(exa.Config{BaseURL: srv.URL}, "key-9", httpx.Client(roots(srv)))
}

func assertFailed(t *testing.T, err error) {
	t.Helper()
	if err == nil || err.Error() != providers.CodeSearchFailed {
		t.Fatalf("err %v", err)
	}
}

func TestSearch(t *testing.T) {
	type got struct {
		method, path, key string
		body              map[string]any
	}
	seen := make(chan got, 1)
	long, longTitle := strings.Repeat("ж", 1500), strings.Repeat("т", 300)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		g := got{method: r.Method, path: r.URL.Path, key: r.Header.Get("x-api-key")}
		_ = json.Unmarshal(b, &g.body)
		seen <- g
		_, _ = io.WriteString(w, `{"results":[`+
			`{"title":"A","url":"https://a.example","highlights":["one","two"]},`+
			`{"title":"`+longTitle+`","url":"https://b.example","highlights":["`+long+`"]},`+
			`{"title":"C","url":"https://c.example","highlights":[]}]}`)
	}))
	defer srv.Close()

	res, err := newSearch(srv).Search(context.Background(), "what is up")
	if err != nil {
		t.Fatal(err)
	}
	g := <-seen
	contents, _ := g.body["contents"].(map[string]any)
	if g.method != "POST" || g.path != "/search" || g.key != "key-9" || g.body["query"] != "what is up" ||
		g.body["type"] != "fast" || g.body["numResults"] != float64(3) || contents["highlights"] != true {
		t.Fatalf("request %+v", g)
	}
	if len(res) != 2 || res[0].Title != "A" || res[0].Text != "one two" {
		t.Fatalf("results %+v", res)
	}
	if utf8.RuneCountInString(res[1].Text) != 1000 {
		t.Fatalf("truncation: %d runes", utf8.RuneCountInString(res[1].Text))
	}
	// a title is page text the model reads too
	if res[1].Title != longTitle[:len("т")*200] {
		t.Fatalf("title truncation: %d runes", utf8.RuneCountInString(res[1].Title))
	}
	for _, r := range res {
		if strings.Contains(r.Title+r.Text, "example") {
			t.Fatal("url leaked into the model's text")
		}
	}
	// the screen's link card gets the url and its site
	if res[0].URL != "https://a.example" || res[0].Site != "a.example" {
		t.Fatalf("link %+v", res[0])
	}
}

func TestSearchKeepsTheCardFields(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results":[`+
			`{"title":"A","url":"https://www.a.example/p?q=1","publishedDate":"2026-11-13T10:00:00.000Z","image":"https://img.example/a.jpg","highlights":["one"]},`+
			`{"title":"B","url":"javascript:alert(1)","publishedDate":"soon","image":"http://img.example/b.jpg","highlights":["two"]}]}`)
	}))
	defer srv.Close()
	res, err := newSearch(srv).Search(context.Background(), "q")
	if err != nil || len(res) != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if a := res[0]; a.URL != "https://www.a.example/p?q=1" || a.Site != "a.example" || a.Published != "2026-11-13T10:00:00Z" || a.ImageURL != "https://img.example/a.jpg" {
		t.Fatalf("a %+v", a)
	}
	if b := res[1]; b.URL != "" || b.Site != "" || b.Published != "" || b.ImageURL != "" {
		t.Fatalf("b %+v", b)
	}
}

func TestPageText(t *testing.T) {
	var body map[string]any
	var path string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = io.WriteString(w, `{"results":[{"url":"https://a.example","text":"`+strings.Repeat("ж", 9000)+`"}]}`)
	}))
	defer srv.Close()
	pt, ok := newSearch(srv).(providers.PageText)
	if !ok {
		t.Fatal("exa has no page text")
	}
	text, err := pt.PageText(context.Background(), "https://a.example")
	if err != nil || utf8.RuneCountInString(text) != 8000 || path != "/contents" {
		t.Fatalf("%d runes, %v, %s", utf8.RuneCountInString(text), err, path)
	}
	if urls, _ := body["urls"].([]any); len(urls) != 1 || urls[0] != "https://a.example" {
		t.Fatalf("body %v", body)
	}
	fail := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	defer fail.Close()
	_, err = newSearch(fail).(providers.PageText).PageText(context.Background(), "https://a.example")
	assertFailed(t, err)
}

func TestHTTPErrorHidesUpstream(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, upstreamLeak)
	}))
	defer srv.Close()
	_, err := newSearch(srv).Search(context.Background(), "q")
	assertFailed(t, err)
	if strings.Contains(err.Error(), upstreamLeak) {
		t.Fatal("leak")
	}
}

// a refusal is logged by its status alone, so a block (cloudflare's 403 page) is told from a timeout
func TestFailureLoggedByCode(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, upstreamLeak)
	}))
	defer srv.Close()
	var logs bytes.Buffer
	s := exa.New(exa.Config{BaseURL: srv.URL, Log: slog.New(slog.NewTextHandler(&logs, nil))}, "key-9", httpx.Client(roots(srv)))
	_, err := s.Search(context.Background(), "secret query")
	assertFailed(t, err)
	got := logs.String()
	if !strings.Contains(got, "msg=search_failed provider=exa code=status_403") {
		t.Fatalf("log %q", got)
	}
	for _, leak := range []string{"secret query", "key-9", upstreamLeak} {
		if strings.Contains(got, leak) {
			t.Fatalf("%q reached the log", leak)
		}
	}
}

func TestMalformedBody(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, upstreamLeak)
	}))
	defer srv.Close()
	_, err := newSearch(srv).Search(context.Background(), "q")
	assertFailed(t, err)
}

func TestUntrustedTLS(t *testing.T) {
	var hit atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit.Add(1) }))
	defer srv.Close()
	s := exa.New(exa.Config{BaseURL: srv.URL}, "k", httpx.Client(nil))
	_, err := s.Search(context.Background(), "q")
	assertFailed(t, err)
	if hit.Load() != 0 {
		t.Fatal("request reached untrusted upstream")
	}
}

func TestRedirectRefused(t *testing.T) {
	var hit atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit.Add(1) }))
	defer target.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	_, err := newSearch(srv).Search(context.Background(), "q")
	assertFailed(t, err)
	if hit.Load() != 0 {
		t.Fatal("redirect followed")
	}
}

func TestTimeout(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()
	s := exa.New(exa.Config{BaseURL: srv.URL, Timeout: 100 * time.Millisecond}, "k", httpx.Client(roots(srv)))
	start := time.Now()
	_, err := s.Search(context.Background(), "q")
	assertFailed(t, err)
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout not applied")
	}
}

func TestPanicIsFailure(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()
	exa.SetDecodeHook(t, func([]byte) { panic(upstreamLeak) })
	_, err := newSearch(srv).Search(context.Background(), "q")
	assertFailed(t, err)
}

func TestModule(t *testing.T) {
	m := exa.Module
	if m.Kind != providers.KindSearch || len(m.Secrets) != 1 || m.Secrets[0] != "apiKey" {
		t.Fatal("module")
	}
	if _, err := m.New([]byte(`{}`), map[string]string{}); err == nil {
		t.Fatal("missing key accepted")
	}
	v, err := m.New([]byte(`{}`), map[string]string{"apiKey": "k"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v.(providers.Search); !ok {
		t.Fatal("kind")
	}
}
