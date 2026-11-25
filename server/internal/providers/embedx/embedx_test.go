package embedx_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/embedx"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

const upstreamLeak = "LEAKY-UPSTREAM-TEXT"

func roots(srv *httptest.Server) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(srv.Certificate())
	return p
}

func newEmbedder(srv *httptest.Server, key string) providers.Embedder {
	return embedx.New(embedx.Config{Module: "t", BaseURL: srv.URL + "/v1", Model: "m-1", APIKey: key}, httpx.Client(roots(srv)))
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil || err.Error() != code {
		t.Fatalf("err %v, want %s", err, code)
	}
}

// reply answers each input with a vector whose first number is its 1-based position, in reversed order
func reply(w http.ResponseWriter, body []byte, dim int) {
	var in struct{ Input []string }
	_ = json.Unmarshal(body, &in)
	data := make([]map[string]any, len(in.Input))
	for i := range in.Input {
		v := make([]float32, dim)
		v[0] = float32(i + 1)
		data[len(in.Input)-1-i] = map[string]any{"index": i, "embedding": v}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func TestRequestShapeAndDecoding(t *testing.T) {
	type got struct {
		method, path, auth string
		body               map[string]any
	}
	seen := make(chan got, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		g := got{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization")}
		_ = json.Unmarshal(b, &g.body)
		seen <- g
		reply(w, b, 3)
	}))
	defer srv.Close()
	e := newEmbedder(srv, "key-9")
	if e.Model() != "m-1" {
		t.Fatalf("model %q", e.Model())
	}
	vecs, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	g := <-seen
	in, _ := g.body["input"].([]any)
	if g.method != "POST" || g.path != "/v1/embeddings" || g.auth != "Bearer key-9" || g.body["model"] != "m-1" ||
		g.body["encoding_format"] != "float" || len(in) != 2 || in[0] != "a" || in[1] != "b" {
		t.Fatalf("request %+v", g)
	}
	// the reply is reversed; index restores the input order
	if len(vecs) != 2 || len(vecs[0]) != 3 || vecs[0][0] != 1 || vecs[1][0] != 2 {
		t.Fatalf("vectors %v", vecs)
	}
}

func TestNoAuthHeaderWithoutKey(t *testing.T) {
	seen := make(chan http.Header, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		reply(w, b, 2)
	}))
	defer srv.Close()
	if _, err := newEmbedder(srv, "").Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := (<-seen)["Authorization"]; ok {
		t.Fatal("authorization header sent without a key")
	}
}

func TestInputLimits(t *testing.T) {
	var calls, longest int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		b, _ := io.ReadAll(r.Body)
		var in struct{ Input []string }
		_ = json.Unmarshal(b, &in)
		for _, s := range in.Input {
			longest = max(longest, utf8.RuneCountInString(s))
			if !utf8.ValidString(s) {
				longest = -1 << 20
			}
		}
		reply(w, b, 2)
	}))
	defer srv.Close()
	e := newEmbedder(srv, "k")
	ctx := context.Background()

	_, err := e.Embed(ctx, make([]string, 33))
	assertCode(t, err, providers.CodeEmbedFailed)
	if calls != 0 {
		t.Fatal("oversize batch reached upstream")
	}
	if out, err := e.Embed(ctx, nil); err != nil || len(out) != 0 || calls != 0 {
		t.Fatalf("empty batch: %v %v %d", out, err, calls)
	}
	if _, err := e.Embed(ctx, make([]string, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Embed(ctx, []string{strings.Repeat("ж", 9000)}); err != nil {
		t.Fatal(err)
	}
	if longest != 8000 {
		t.Fatalf("truncated to %d runes", longest)
	}
}

func TestVectorShapeErrors(t *testing.T) {
	for name, body := range map[string]string{
		"count":     `{"data":[{"embedding":[1,2]}]}`,
		"ragged":    `{"data":[{"embedding":[1,2]},{"embedding":[1]}]}`,
		"empty vec": `{"data":[{"embedding":[]},{"embedding":[]}]}`,
		"garbage":   `nope`,
		"bad index": `{"data":[{"index":0,"embedding":[1]},{"index":0,"embedding":[1]}]}`,
		"overflow":  `{"data":[{"embedding":[1e39]},{"embedding":[1]}]}`,
		"null num":  `{"data":[{"embedding":[null]},{"embedding":[1]}]}`,
	} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, body)
		}))
		_, err := newEmbedder(srv, "k").Embed(context.Background(), []string{"a", "b"})
		srv.Close()
		if err == nil || err.Error() != providers.CodeEmbedFailed {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestDimensionChangeBetweenCalls(t *testing.T) {
	dim := 3
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		reply(w, b, dim)
	}))
	defer srv.Close()
	e := newEmbedder(srv, "k")
	if _, err := e.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	dim = 4
	_, err := e.Embed(context.Background(), []string{"a"})
	assertCode(t, err, providers.CodeEmbedFailed)
	dim = 3
	if _, err := e.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatalf("pinned dimension lost: %v", err)
	}
}

func TestNon200HidesUpstreamText(t *testing.T) {
	for _, status := range []int{400, 401, 429, 500} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, upstreamLeak+" key-9")
		}))
		_, err := newEmbedder(srv, "key-9").Embed(context.Background(), []string{"a"})
		srv.Close()
		assertCode(t, err, providers.CodeEmbedFailed)
		if strings.Contains(err.Error(), upstreamLeak) || strings.Contains(err.Error(), "key-9") {
			t.Fatal("leak")
		}
	}
}

func TestRedirectRefused(t *testing.T) {
	var followed bool
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed = true }))
	defer target.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	pool := roots(srv)
	pool.AddCert(target.Certificate())
	e := embedx.New(embedx.Config{Module: "t", BaseURL: srv.URL, Model: "m", APIKey: "k"}, httpx.Client(pool))
	_, err := e.Embed(context.Background(), []string{"a"})
	assertCode(t, err, providers.CodeEmbedUnavailable)
	if followed {
		t.Fatal("redirect followed")
	}
}

func TestUnreachableIsUnavailable(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	e := newEmbedder(srv, "k")
	srv.Close()
	_, err := e.Embed(context.Background(), []string{"a"})
	assertCode(t, err, providers.CodeEmbedUnavailable)
}

func TestOversizeBody(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"embedding":[`)
		chunk := strings.Repeat("0.5,", 1<<16)
		for i := 0; i < 40; i++ {
			_, _ = io.WriteString(w, chunk)
		}
		_, _ = io.WriteString(w, `1]}]}`)
	}))
	defer srv.Close()
	_, err := newEmbedder(srv, "k").Embed(context.Background(), []string{"a"})
	assertCode(t, err, providers.CodeEmbedFailed)
}

func TestTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	e := embedx.New(embedx.Config{Module: "t", BaseURL: srv.URL, Model: "m", Timeout: 100 * time.Millisecond}, httpx.Client(roots(srv)))
	start := time.Now()
	_, err := e.Embed(context.Background(), []string{"a"})
	assertCode(t, err, providers.CodeEmbedTimeout)
	if time.Since(start) > 3*time.Second {
		t.Fatal("slow")
	}
}

func TestCallerCancelIsNotTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err := newEmbedder(srv, "k").Embed(ctx, []string{"a"})
	if err == nil || err.Error() == providers.CodeEmbedTimeout || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
}
