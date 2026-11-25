package hermes_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/hermes"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
)

func probed(base string, client *http.Client) any {
	c, err := hermes.NewClient(hermes.ClientConfig{BaseURL: base + prefix, APIKey: probetest.Key, HTTP: client})
	if err != nil {
		panic(err)
	}
	return c
}

// the probe reads the capabilities the kind needs, behind the bearer key:
// no run is made
func TestModuleProbe(t *testing.T) {
	probetest.HTTP(t, probetest.Upstream{
		Probe: hermes.Module.Probe,
		Build: probed,
		OK: func(t *testing.T) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				if r.Method != http.MethodGet || r.URL.Path != prefix+"/v1/capabilities" || len(b) != 0 ||
					r.Header.Get("Authorization") != "Bearer "+probetest.Key {
					t.Errorf("request %s %s", r.Method, r.URL)
				}
				_, _ = io.WriteString(w, capsOK)
			}
		},
	})
}

// a server without the runs the kind needs answers, but not as needed
func TestModuleProbeIncompatible(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"object":"hermes.api_server.capabilities","features":{}}`)
	}))
	defer srv.Close()
	probetest.Want(t, hermes.Module.Probe(context.Background(), probed(srv.URL, probetest.Client(srv))), "bad_response")
}
