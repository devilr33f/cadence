package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/jsonschemax"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func TestLLMModuleConfig(t *testing.T) {
	m := LLMModule
	if m.ID != "openai-compatible-llm" || m.Kind != providers.KindLLM || len(m.Secrets) != 0 {
		t.Fatalf("module %+v", m)
	}
	schema, err := jsonschemax.Compile(m.ConfigSchema)
	if err != nil {
		t.Fatal(err)
	}
	for cfg, ok := range map[string]bool{
		`{"baseUrl":"https://gw.example/v1"}`:             true,
		`{"baseUrl":"https://gw.example/v1","model":"m"}`: true,
		`{"baseUrl":"http://gw.example/v1"}`:              false,
		`{"model":"m"}`:                                   false,
		`{"baseUrl":"https://gw.example/v1","model":""}`:  false,
		`{"baseUrl":"https://gw.example/v1","x":1}`:       false,
	} {
		if _, got := jsonschemax.Validate(schema, json.RawMessage(cfg)); got != ok {
			t.Errorf("%s: valid=%v", cfg, got)
		}
	}
	if _, err := m.New([]byte(`{"baseUrl":"https://u:p@gw.example/v1"}`), nil); err == nil {
		t.Fatal("userinfo in the base url accepted")
	}
	v, err := m.New([]byte(`{"baseUrl":"https://gw.example/v1/","model":"main"}`), map[string]string{"apiKey": "k"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v.(providers.LLM); !ok {
		t.Fatalf("new gave %T", v)
	}
}

// the probe lists the models with the key; a refused key is auth
func TestLLMProbe(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	for key, want := range map[string]error{"good": nil, "bad": providers.ProbeAuth} {
		l := &compatLLM{base: srv.URL + "/v1", key: key, client: srv.Client()}
		if err := probeLLM(context.Background(), l); !errors.Is(err, want) {
			t.Errorf("key %s: %v", key, err)
		}
	}
}
