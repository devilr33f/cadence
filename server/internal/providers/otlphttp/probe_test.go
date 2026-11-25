package otlphttp_test

import (
	"compress/gzip"
	"io"
	"net/http"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers/otlphttp"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
)

// the probe exports an empty logs request, which OTLP says a collector should
// take with success: no data, so nothing is stored or billed
func TestProbe(t *testing.T) {
	probetest.HTTP(t, probetest.Upstream{
		Probe: otlphttp.Module.Probe,
		Build: func(base string, client *http.Client) any {
			c, err := otlphttp.NewClient(otlphttp.ClientConfig{URL: base + "/otlp", Authorization: "Bearer " + probetest.Key, HTTP: client})
			if err != nil {
				panic(err)
			}
			return c
		},
		OK: func(t *testing.T) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				var body []byte
				if zr, err := gzip.NewReader(r.Body); err == nil {
					body, err = io.ReadAll(zr)
					if err != nil {
						t.Error(err)
					}
				}
				if r.Method != http.MethodPost || r.URL.Path != "/otlp/v1/logs" || len(body) != 0 ||
					r.Header.Get("Authorization") != "Bearer "+probetest.Key || r.Header.Get("Content-Type") != "application/x-protobuf" {
					t.Errorf("request %s %s body %d", r.Method, r.URL, len(body))
				}
				w.Header().Set("Content-Type", "application/x-protobuf")
			}
		},
		// a collector's answer to an empty export is an empty message
		AnyBody: true,
	})
}
