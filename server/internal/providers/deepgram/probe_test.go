package deepgram_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers/deepgram"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
)

// the probe reads the key's own details: no audio, no synthesis, and no
// project scope needed
func tokenDetails(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet || r.URL.Path != "/v1/auth/token" || r.URL.RawQuery != "" || len(b) != 0 ||
			r.Header.Get("Authorization") != "Token "+probetest.Key {
			t.Errorf("request %s %s", r.Method, r.URL)
		}
		_, _ = io.WriteString(w, `{"api_key":{"api_key_id":"k","comment":"`+probetest.Leak+`","scopes":["member"]}}`)
	}
}

func wss(base string) string { return "wss" + strings.TrimPrefix(base, "https") }

func TestFluxProbe(t *testing.T) {
	probetest.HTTP(t, probetest.Upstream{
		Probe: deepgram.FluxModule.Probe,
		Build: func(base string, client *http.Client) any {
			return deepgram.NewFlux(deepgram.FluxConfig{BaseURL: wss(base)}, probetest.Key, client)
		},
		OK: tokenDetails,
	})
}

func TestAuraProbe(t *testing.T) {
	probetest.HTTP(t, probetest.Upstream{
		Probe: deepgram.AuraModule.Probe,
		Build: func(base string, client *http.Client) any {
			return deepgram.NewAura(deepgram.AuraConfig{BaseURL: wss(base)}, probetest.Key, client)
		},
		OK: tokenDetails,
	})
}
