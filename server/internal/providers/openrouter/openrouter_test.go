package openrouter_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
	"git.shino.boo/devilreef/cadence/server/internal/providers/openrouter"
)

const upstreamLeak = "LEAKY-UPSTREAM-TEXT"

func roots(srv *httptest.Server) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(srv.Certificate())
	return p
}

var baseReq = providers.LLMRequest{Model: "m/x", Messages: []providers.Message{
	{Role: "system", Content: "sys"}, {Role: "user", Content: "hi"}}}

func collect(t *testing.T, llm providers.LLM) ([]providers.LLMChunk, error) {
	t.Helper()
	return collectReq(t, llm, baseReq)
}

func collectReq(t *testing.T, llm providers.LLM, req providers.LLMRequest) ([]providers.LLMChunk, error) {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := llm.Stream(c, req)
	if err != nil {
		return nil, err
	}
	var out []providers.LLMChunk
	for chunk := range ch {
		out = append(out, chunk)
	}
	return out, nil
}

func TestOpenRouterStream(t *testing.T) {
	type got struct {
		path, auth string
		body       map[string]any
	}
	seen := make(chan got, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		g := got{path: r.URL.Path, auth: r.Header.Get("Authorization")}
		_ = json.Unmarshal(b, &g.body)
		seen <- g
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range []string{
			": OPENROUTER PROCESSING\n\n",
			`data: {"choices":[{"delta":{"content":"It is "}}]}` + "\n\n",
			`data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n",
			`data: {"choices":[{"delta":{"content":"test "}}]}` + "\n\n",
			`data: {"choices":[{"delta":{"content":"time."}}]}` + "\n\n",
			"data: [DONE]\n\n",
			`data: {"choices":[{"delta":{"content":"after done"}}]}` + "\n\n",
		} {
			_, _ = io.WriteString(w, l)
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()

	chunks, err := collect(t, openrouter.New(openrouter.Config{BaseURL: srv.URL + "/api/v1"}, "key-7", httpx.Client(roots(srv))))
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 3 || chunks[0].Text != "It is " || chunks[1].Text != "test " || chunks[2].Text != "time." {
		t.Fatalf("chunks %+v", chunks)
	}
	for _, c := range chunks {
		if c.Err != nil {
			t.Fatalf("err %v", c.Err)
		}
	}
	g := <-seen
	if g.path != "/api/v1/chat/completions" || g.auth != "Bearer key-7" {
		t.Fatalf("request %s %s", g.path, g.auth)
	}
	msgs, _ := g.body["messages"].([]any)
	if g.body["stream"] != true || g.body["model"] != "m/x" || g.body["max_tokens"] != float64(180) || len(msgs) != 2 {
		t.Fatalf("body %v", g.body)
	}
	if m := msgs[0].(map[string]any); m["role"] != "system" || m["content"] != "sys" {
		t.Fatalf("system message %v", m)
	}
	if m := msgs[1].(map[string]any); m["role"] != "user" || m["content"] != "hi" {
		t.Fatalf("user message %v", m)
	}
}

func TestOpenRouterHTTPError(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{{500, providers.CodeProviderError}, {429, providers.CodeRateLimited}, {401, providers.CodeAuth}} {
		t.Run(fmt.Sprintf("%d", tc.status), func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, upstreamLeak)
			}))
			defer srv.Close()
			chunks, err := collect(t, openrouter.New(openrouter.Config{BaseURL: srv.URL}, "k", httpx.Client(roots(srv))))
			if err != nil {
				t.Fatal(err)
			}
			if len(chunks) != 1 || chunks[0].Err == nil || chunks[0].Err.Error() != tc.code || chunks[0].Text != "" {
				t.Fatalf("chunks %+v", chunks)
			}
			code, status := providers.ErrorFacts(chunks[0].Err, providers.CodeLLMFailed)
			if code != tc.code || status != tc.status || strings.Contains(code, upstreamLeak) {
				t.Fatalf("facts %q %d", code, status)
			}
		})
	}
}

func TestOpenRouterRejectsUntrustedTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	chunks, err := collect(t, openrouter.New(openrouter.Config{BaseURL: srv.URL}, "k", httpx.Client(nil)))
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Err == nil {
		t.Fatalf("chunks %+v", chunks)
	}
}

func TestOpenRouterRefusesRedirect(t *testing.T) {
	hits := 0
	other := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer other.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusMovedPermanently)
	}))
	defer srv.Close()
	chunks, err := collect(t, openrouter.New(openrouter.Config{BaseURL: srv.URL}, "k", httpx.Client(roots(srv))))
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Err == nil {
		t.Fatalf("chunks %+v", chunks)
	}
	if hits != 0 {
		t.Fatal("redirect target was hit")
	}
}

func sse(t *testing.T, lines ...string) []providers.LLMChunk {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range lines {
			_, _ = io.WriteString(w, l+"\n\n")
		}
	}))
	defer srv.Close()
	chunks, err := collect(t, openrouter.New(openrouter.Config{BaseURL: srv.URL}, "k", httpx.Client(roots(srv))))
	if err != nil {
		t.Fatal(err)
	}
	return chunks
}

func TestOpenRouterNullErrorIsNoError(t *testing.T) {
	chunks := sse(t,
		`data: {"choices":[{"delta":{"content":"a"}}],"error":null}`,
		`data: {"choices":[{"delta":{"content":"b"}}],"error":null}`,
		"data: [DONE]")
	if len(chunks) != 2 || chunks[0].Text != "a" || chunks[1].Text != "b" || chunks[0].Err != nil || chunks[1].Err != nil {
		t.Fatalf("chunks %+v", chunks)
	}
}

func TestOpenRouterNoDone(t *testing.T) {
	chunks := sse(t, `data: {"choices":[{"delta":{"content":"a"}}]}`)
	if len(chunks) != 2 || chunks[0].Text != "a" || chunks[1].Err == nil || chunks[1].Err.Error() != providers.CodeLLMFailed {
		t.Fatalf("chunks %+v", chunks)
	}
}

func TestOpenRouterMidStreamError(t *testing.T) {
	chunks := sse(t,
		`data: {"choices":[{"delta":{"content":"a"}}]}`,
		`data: {"error":{"message":"`+upstreamLeak+`","code":500}}`,
		`data: {"choices":[{"delta":{"content":"b"}}]}`,
		"data: [DONE]")
	if len(chunks) != 2 || chunks[1].Err == nil || chunks[1].Err.Error() != providers.CodeLLMFailed || chunks[1].Text != "" {
		t.Fatalf("chunks %+v", chunks)
	}
}

func TestOpenRouterReadLoopPanicEndsStream(t *testing.T) {
	var logs bytes.Buffer
	openrouter.SetDecodeHook(t, func([]byte) { panic("secret-xyz") })
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {}\n\n")
	}))
	defer srv.Close()
	llm := openrouter.New(openrouter.Config{BaseURL: srv.URL, Log: slog.New(slog.NewTextHandler(&logs, nil))},
		"k", httpx.Client(roots(srv)))
	chunks, err := collect(t, llm)
	if err != nil || len(chunks) != 1 || chunks[0].Err == nil || chunks[0].Err.Error() != providers.CodeLLMFailed {
		t.Fatalf("chunks %+v err %v", chunks, err)
	}
	if out := logs.String(); strings.Contains(out, "secret-xyz") || !strings.Contains(out, "provider_panic") {
		t.Fatalf("log %q", out)
	}
}
