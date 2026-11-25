package tools_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/metno"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// Moscow in Russian crosses geocoding, MET Norway, the provider contract and
// the agent tool, with no real upstream: current, today and tomorrow are all
// metric and on the household's clock.
func TestWeatherMoscowRussianEndToEnd(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/search":
			if r.URL.Query().Get("name") != "Москва" || r.URL.Query().Get("language") != "ru" {
				t.Errorf("geocode query %v", r.URL.Query())
			}
			_, _ = io.WriteString(w, `{"results":[{"name":"Москва","country":"Россия","latitude":55.755869,"longitude":37.617299}]}`)
		case "/weatherapi/locationforecast/2.0/compact":
			if r.URL.Query().Get("lat") != "55.7558" || r.URL.Query().Get("lon") != "37.6172" || r.UserAgent() == "" {
				t.Errorf("forecast query %v, user agent %q", r.URL.Query(), r.UserAgent())
			}
			_, _ = io.WriteString(w, `{"properties":{"timeseries":[`+
				`{"time":"2026-11-15T09:00:00Z","data":{"instant":{"details":{"air_temperature":2.5}},`+
				`"next_1_hours":{"summary":{"symbol_code":"rain"},"details":{"precipitation_amount":0.4}}}},`+
				`{"time":"2026-11-15T10:00:00Z","data":{"instant":{"details":{"air_temperature":3}},`+
				`"next_1_hours":{"summary":{"symbol_code":"rain"},"details":{"precipitation_amount":0.6}}}},`+
				`{"time":"2026-11-16T09:00:00Z","data":{"instant":{"details":{"air_temperature":-1}},`+
				`"next_1_hours":{"summary":{"symbol_code":"snow"},"details":{"precipitation_amount":1.5}}}},`+
				`{"time":"2026-11-16T10:00:00Z","data":{"instant":{"details":{"air_temperature":-4}},`+
				`"next_1_hours":{"summary":{"symbol_code":"heavysnow"},"details":{"precipitation_amount":2.5}}}}`+
				`]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	weather := metno.New(metno.Config{
		ForecastURL: srv.URL + "/weatherapi/locationforecast/2.0/compact", GeocodingURL: srv.URL + "/v1/search",
		Now: func() time.Time { return time.Date(2026, 11, 15, 9, 30, 0, 0, time.UTC) },
	}, probetest.Client(srv))
	tool := tools.Weather(tools.WeatherDeps{
		Settings: settingsOf(settings.Values{
			Weather: "met", WeatherLocation: "Москва", Language: "ru", TimeZone: "Europe/Moscow",
		}),
		Weather: func(context.Context, string) (providers.Weather, error) { return weather, nil },
	})
	raw, toolErr := run(t, tool, cadencev2.Trigger_TRIGGER_WAKE, `{"days":2}`)
	if toolErr != nil {
		t.Fatalf("%+v", toolErr)
	}
	var out struct {
		Place   string `json:"place"`
		Current struct {
			Time        string  `json:"time"`
			Temperature float64 `json:"temperature"`
			Code        int     `json:"code"`
			Summary     string  `json:"summary"`
		} `json:"current"`
		Days []struct {
			Date            string `json:"date"`
			Min, Max        float64
			PrecipitationMm *float64 `json:"precipitationMm"`
			Code            int      `json:"code"`
			Summary         string   `json:"summary"`
		} `json:"days"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Place != "Москва, Россия" || out.Current.Time != "2026-11-15T12:00:00+03:00" ||
		out.Current.Temperature != 2.5 || out.Current.Code != 63 || out.Current.Summary != "Moderate rain" {
		t.Fatalf("place and current: %s", raw)
	}
	if len(out.Days) != 2 || out.Days[0].Date != "2026-11-15" || out.Days[0].Min != 2.5 || out.Days[0].Max != 3 ||
		out.Days[0].PrecipitationMm == nil || *out.Days[0].PrecipitationMm != 1 ||
		out.Days[1].Date != "2026-11-16" || out.Days[1].Min != -4 || out.Days[1].Max != -1 || out.Days[1].Code != 73 {
		t.Fatalf("days: %s", raw)
	}
}
