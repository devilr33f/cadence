package openrouter_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/openrouter"
)

func TestEmbedModule(t *testing.T) {
	m := openrouter.EmbedModule
	if m.ID != "openrouter-embed" || m.Kind != providers.KindEmbed || len(m.Secrets) != 1 || m.Secrets[0] != "apiKey" {
		t.Fatalf("module %+v", m)
	}
	if _, err := m.New([]byte(`{"model":"x"}`), map[string]string{}); err == nil {
		t.Fatal("missing key accepted")
	}
	if _, err := m.New([]byte(`{}`), map[string]string{"apiKey": "k"}); err == nil {
		t.Fatal("missing model accepted")
	}
	v, err := m.New([]byte(`{"model":"openai/text-embedding-3-small"}`), map[string]string{"apiKey": "k"})
	if err != nil {
		t.Fatal(err)
	}
	e, ok := v.(providers.Embedder)
	if !ok || e.Model() != "openai/text-embedding-3-small" {
		t.Fatal("kind or model")
	}
}

func TestEmbedHostAndPath(t *testing.T) {
	seen := make(chan string, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.URL.Path
		_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[1]}]}`)
	}))
	defer srv.Close()
	e := openrouter.NewEmbedder(openrouter.EmbedConfig{BaseURL: srv.URL + "/api/v1", Model: "m"}, "k", srv.Client())
	if _, err := e.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if p := <-seen; p != "/api/v1/embeddings" {
		t.Fatalf("path %q", p)
	}
}
