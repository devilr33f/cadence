package openmeteo_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers/openmeteo"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
)

func newProbed(base string, client *http.Client) any {
	return openmeteo.New(openmeteo.Config{GeocodingURL: base + "/v1/search", ForecastURL: base + "/v1/forecast"}, client)
}

// the probe geocodes one fixed place and asks for no forecast
func TestProbe(t *testing.T) {
	probetest.HTTP(t, probetest.Upstream{
		Probe: openmeteo.Module.Probe,
		Build: newProbed,
		OK: func(t *testing.T) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if r.Method != http.MethodGet || r.URL.Path != "/v1/search" || q.Get("name") != "Bishkek" || q.Get("count") != "1" {
					t.Errorf("request %s %s", r.Method, r.URL)
				}
				_, _ = io.WriteString(w, `{"results":[{"name":"Bishkek","country":"Kyrgyzstan","latitude":42.87,"longitude":74.59}]}`)
			}
		},
	})
}

// a geocoder that knows no Bishkek is not answering as Open-Meteo does
func TestProbeNoPlace(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"generationtime_ms":0.1}`)
	}))
	defer srv.Close()
	probetest.Want(t, openmeteo.Module.Probe(context.Background(), newProbed(srv.URL, probetest.Client(srv))), "bad_response")
}
