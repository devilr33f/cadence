package openaicompat_test

import (
	"io"
	"net/http"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers/embedx"
	"git.shino.boo/devilreef/cadence/server/internal/providers/openaicompat"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
)

// the probe lists the server's models (GET {baseUrl}/models): no embedding is
// made, so nothing is billed
func models(key string) func(t *testing.T) http.HandlerFunc {
	return func(t *testing.T) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			auth, sent := r.Header["Authorization"]
			if r.Method != http.MethodGet || r.URL.Path != "/v1/models" || len(b) != 0 ||
				(key == "" && sent) || (key != "" && (len(auth) != 1 || auth[0] != "Bearer "+key)) {
				t.Errorf("request %s %s auth sent %v", r.Method, r.URL, sent)
			}
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"`+probetest.Leak+`"}]}`)
		}
	}
}

func build(key string) func(string, *http.Client) any {
	return func(base string, client *http.Client) any {
		return embedx.New(embedx.Config{Module: "openai-compatible", BaseURL: base + "/v1", Model: "m", APIKey: key}, client)
	}
}

func TestProbe(t *testing.T) {
	probetest.HTTP(t, probetest.Upstream{Probe: openaicompat.EmbedModule.Probe, Build: build(probetest.Key), OK: models(probetest.Key)})
}

// a self-hosted server that takes no key gets no Authorization header
func TestProbeWithoutKey(t *testing.T) {
	probetest.HTTP(t, probetest.Upstream{Probe: openaicompat.EmbedModule.Probe, Build: build(""), OK: models("")})
}
