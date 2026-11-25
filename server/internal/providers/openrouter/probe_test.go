package openrouter_test

import (
	"io"
	"net/http"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers/openrouter"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
)

// the probe reads the key's own record (GET /key): free, and unlike the model
// list, which answers without a key, it refuses a wrong one
func keyRecord(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/key" || len(b) != 0 ||
			r.Header.Get("Authorization") != "Bearer "+probetest.Key {
			t.Errorf("request %s %s", r.Method, r.URL)
		}
		_, _ = io.WriteString(w, `{"data":{"label":"`+probetest.Leak+`","usage":1}}`)
	}
}

func TestLLMProbe(t *testing.T) {
	probetest.HTTP(t, probetest.Upstream{
		Probe: openrouter.Module.Probe,
		Build: func(base string, client *http.Client) any {
			return openrouter.New(openrouter.Config{BaseURL: base + "/api/v1"}, probetest.Key, client)
		},
		OK: keyRecord,
	})
}

func TestEmbedProbe(t *testing.T) {
	probetest.HTTP(t, probetest.Upstream{
		Probe: openrouter.EmbedModule.Probe,
		Build: func(base string, client *http.Client) any {
			return openrouter.NewEmbedder(openrouter.EmbedConfig{BaseURL: base + "/api/v1/", Model: "m"}, probetest.Key, client)
		},
		OK: keyRecord,
	})
}
