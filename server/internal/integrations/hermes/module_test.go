package hermes_test

import (
	"encoding/json"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/hermes"
	"git.shino.boo/devilreef/cadence/server/internal/jsonschemax"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func TestModule(t *testing.T) {
	if hermes.Module.ID != "hermes" || hermes.Module.Kind != providers.KindHermes ||
		len(hermes.Module.Secrets) != 1 || hermes.Module.Secrets[0] != "apiKey" {
		t.Fatalf("module %+v", hermes.Module)
	}
	schema, err := jsonschemax.Compile(hermes.Module.ConfigSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []string{
		`{}`, `{"baseUrl":"http://h.example"}`, `{"baseUrl":"https://h.example","x":1}`,
		`{"baseUrl":"https://h.example","maxRuns":0}`, `{"baseUrl":"https://h.example","maxRuns":17}`,
	} {
		if _, ok := jsonschemax.Validate(schema, json.RawMessage(cfg)); ok {
			t.Errorf("schema took %s", cfg)
		}
	}
	for cfg, runs := range map[string]int{`{"baseUrl":"https://h.example/agent"}`: 4, `{"baseUrl":"https://h.example","maxRuns":16}`: 16} {
		if _, ok := jsonschemax.Validate(schema, json.RawMessage(cfg)); !ok {
			t.Errorf("schema refused %s", cfg)
		}
		v, err := hermes.Module.New(json.RawMessage(cfg), map[string]string{"apiKey": "k"})
		if c, ok := v.(*hermes.Client); err != nil || !ok || c.MaxRuns() != runs {
			t.Fatalf("%s: %T %v", cfg, v, err)
		}
	}
	for _, cfg := range []string{`{"baseUrl":"https://u:pw@h.example"}`, `{"baseUrl":"https://h.example?q=1"}`, `not json`, `{"baseUrl":"https://h.example","maxRuns":99}`} {
		if _, err := hermes.Module.New(json.RawMessage(cfg), map[string]string{"apiKey": "k"}); err == nil || strings.Contains(err.Error(), "pw") {
			t.Errorf("%s: %v", cfg, err)
		}
	}
	if _, err := hermes.Module.New(json.RawMessage(`{"baseUrl":"https://h.example"}`), nil); err == nil {
		t.Error("no key accepted")
	}
}
