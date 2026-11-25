package metno_test

import (
	"io"
	"net/http"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers/metno"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
)

func newProbed(base string, client *http.Client) any {
	return metno.New(metno.Config{ForecastURL: base + "/weatherapi/locationforecast/2.0/compact"}, client)
}

func TestProbe(t *testing.T) {
	probetest.HTTP(t, probetest.Upstream{
		Probe: metno.Module.Probe,
		Build: newProbed,
		OK: func(t *testing.T) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if r.Method != http.MethodGet || r.URL.Path != "/weatherapi/locationforecast/2.0/compact" ||
					q.Get("lat") != "55.7558" || q.Get("lon") != "37.6173" || r.UserAgent() == "" {
					t.Errorf("request %s %s, user agent %q", r.Method, r.URL, r.UserAgent())
				}
				_, _ = io.WriteString(w, moscowForecast)
			}
		},
	})
}
