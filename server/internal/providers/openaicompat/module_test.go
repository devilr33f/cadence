package openaicompat_test

import (
	"context"
	"encoding/json"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/jsonschemax"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/openaicompat"
)

func TestEmbedModuleConfig(t *testing.T) {
	m := openaicompat.EmbedModule
	if m.ID != "openai-compatible" || m.Kind != providers.KindEmbed || len(m.Secrets) != 0 ||
		len(m.OptionalSecrets) != 1 || m.OptionalSecrets[0] != "apiKey" {
		t.Fatalf("module %+v", m)
	}
	schema, err := jsonschemax.Compile(m.ConfigSchema)
	if err != nil {
		t.Fatal(err)
	}
	for cfg, ok := range map[string]bool{
		`{"baseUrl":"https://emb.example/v1","model":"m"}`: true,
		`{"baseUrl":"http://emb.example/v1","model":"m"}`:  false,
		`{"baseUrl":"https://emb.example/v1"}`:             false,
		`{"model":"m"}`:                                    false,
		`{"baseUrl":"https://e/v1","model":""}`:            false,
		`{"baseUrl":"https://e/v1","model":"m","x":1}`:     false,
	} {
		if _, got := jsonschemax.Validate(schema, json.RawMessage(cfg)); got != ok {
			t.Errorf("%s: valid=%v", cfg, got)
		}
	}
}

func TestEmbedModuleNew(t *testing.T) {
	m := openaicompat.EmbedModule
	v, err := m.New([]byte(`{"baseUrl":"https://emb.example/v1/","model":"m"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := v.(providers.Embedder)
	if !ok || e.Model() != "m" {
		t.Fatal("kind or model")
	}
	for _, bad := range []string{`{"baseUrl":"http://x/v1","model":"m"}`, `{"baseUrl":"https://","model":"m"}`, `{"baseUrl":"https://u:p@h/v1","model":"m"}`, `{"baseUrl":"https://h/v1?x=1","model":"m"}`, `{"baseUrl":"https://h/v1#f","model":"m"}`, `nope`} {
		if _, err := m.New([]byte(bad), nil); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if _, err := e.Embed(context.Background(), make([]string, 33)); err == nil {
		t.Fatal("limit")
	}
}
