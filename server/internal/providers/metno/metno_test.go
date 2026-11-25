package metno_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/metno"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
)

var moscowNow = time.Date(2026, 11, 15, 9, 30, 0, 0, time.UTC)

const moscowForecast = `{"properties":{"timeseries":[` +
	`{"time":"2026-11-15T09:00:00Z","data":{"instant":{"details":{"air_temperature":2}},` +
	`"next_1_hours":{"summary":{"symbol_code":"rain"},"details":{"precipitation_amount":0.4}}}},` +
	`{"time":"2026-11-15T10:00:00Z","data":{"instant":{"details":{"air_temperature":3}},` +
	`"next_1_hours":{"summary":{"symbol_code":"lightrainshowers_day"},"details":{"precipitation_amount":0.6}}}},` +
	`{"time":"2026-11-16T09:00:00Z","data":{"instant":{"details":{"air_temperature":-1}},` +
	`"next_1_hours":{"summary":{"symbol_code":"snow"},"details":{"precipitation_amount":1.5}}}},` +
	`{"time":"2026-11-16T10:00:00Z","data":{"instant":{"details":{"air_temperature":-4}},` +
	`"next_1_hours":{"summary":{"symbol_code":"heavysnow"},"details":{"precipitation_amount":2.5}}}}` +
	`]}}`

func newWeather(srv *httptest.Server, now func() time.Time) providers.Weather {
	return metno.New(metno.Config{
		ForecastURL:  srv.URL + "/weatherapi/locationforecast/2.0/compact",
		GeocodingURL: srv.URL + "/v1/search",
		Now:          now,
	}, probetest.Client(srv))
}

func geocodeMoscow(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("name") != "Москва" || r.URL.Query().Get("language") != "ru" {
		http.Error(w, "wrong geocode query", http.StatusBadRequest)
		return
	}
	_, _ = io.WriteString(w, `{"results":[{"name":"Москва","country":"Россия","latitude":55.755869,"longitude":37.617299}]}`)
}

func TestForecastMoscow(t *testing.T) {
	var forecastQuery atomic.Value
	var userAgent atomic.Value
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/search":
			geocodeMoscow(w, r)
		case "/weatherapi/locationforecast/2.0/compact":
			forecastQuery.Store(r.URL.Query())
			userAgent.Store(r.UserAgent())
			_, _ = io.WriteString(w, moscowForecast)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f, err := newWeather(srv, func() time.Time { return moscowNow }).Forecast(context.Background(), providers.WeatherQuery{
		Location: "Москва", Days: 2, Language: "ru", TimeZone: "Europe/Moscow",
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.Place != "Москва, Россия" || f.Current == nil || len(f.Days) != 2 {
		t.Fatalf("forecast %+v", f)
	}
	if got := f.Current; got.Time != "2026-11-15T12:00:00+03:00" || got.Temperature != 2 || got.Code != 63 || got.Summary != "Moderate rain" {
		t.Fatalf("current %+v", got)
	}
	today, tomorrow := f.Days[0], f.Days[1]
	if today.Date != "2026-11-15" || today.Min != 2 || today.Max != 3 || today.Code != 63 || today.Summary != "Moderate rain" ||
		today.PrecipitationMm == nil || *today.PrecipitationMm != 1 {
		t.Fatalf("today %+v", today)
	}
	if tomorrow.Date != "2026-11-16" || tomorrow.Min != -4 || tomorrow.Max != -1 || tomorrow.Code != 73 || tomorrow.Summary != "Moderate snowfall" ||
		tomorrow.PrecipitationMm == nil || *tomorrow.PrecipitationMm != 4 {
		t.Fatalf("tomorrow %+v", tomorrow)
	}
	q := forecastQuery.Load().(url.Values)
	if q.Get("lat") != "55.7558" || q.Get("lon") != "37.6172" || len(q) != 2 {
		t.Fatalf("forecast query %v", q)
	}
	if ua, _ := userAgent.Load().(string); !strings.Contains(ua, "Cadence/") || !strings.Contains(ua, "https://") {
		t.Fatalf("user agent %q", ua)
	}
}

func TestExpiresAndIfModifiedSince(t *testing.T) {
	now := moscowNow
	lastModified := now.Add(-time.Hour).UTC().Format(http.TimeFormat)
	var forecasts atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/search" {
			geocodeMoscow(w, r)
			return
		}
		forecasts.Add(1)
		if forecasts.Load() == 2 {
			if got := r.Header.Get("If-Modified-Since"); got != lastModified {
				t.Errorf("If-Modified-Since %q, want %q", got, lastModified)
			}
			w.Header().Set("Expires", now.Add(time.Hour).UTC().Format(http.TimeFormat))
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Last-Modified", lastModified)
		w.Header().Set("Expires", now.Add(time.Hour).UTC().Format(http.TimeFormat))
		_, _ = io.WriteString(w, moscowForecast)
	}))
	defer srv.Close()
	w := newWeather(srv, func() time.Time { return now })
	q := providers.WeatherQuery{Location: "Москва", Days: 2, Language: "ru", TimeZone: "Europe/Moscow"}
	for range 2 {
		if _, err := w.Forecast(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	if forecasts.Load() != 1 {
		t.Fatalf("fresh cache made %d forecast requests", forecasts.Load())
	}
	now = now.Add(2 * time.Hour)
	f, err := w.Forecast(context.Background(), q)
	if err != nil || len(f.Days) != 2 || forecasts.Load() != 2 {
		t.Fatalf("revalidation: %+v, %v, requests %d", f, err, forecasts.Load())
	}
	if _, err = w.Forecast(context.Background(), q); err != nil || forecasts.Load() != 2 {
		t.Fatalf("revalidated cache: %v, requests %d", err, forecasts.Load())
	}
}

func TestExpiredCacheDoesNotHideOutage(t *testing.T) {
	now := moscowNow
	var forecasts atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/search" {
			geocodeMoscow(w, r)
			return
		}
		if forecasts.Add(1) == 1 {
			w.Header().Set("Expires", now.Add(time.Minute).UTC().Format(http.TimeFormat))
			_, _ = io.WriteString(w, moscowForecast)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, probetest.Leak)
	}))
	defer srv.Close()
	w := newWeather(srv, func() time.Time { return now })
	q := providers.WeatherQuery{Location: "Москва", Days: 1, Language: "ru", TimeZone: "Europe/Moscow"}
	if _, err := w.Forecast(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	_, err := w.Forecast(context.Background(), q)
	if err == nil || err.Error() != providers.CodeWeatherFailed || strings.Contains(err.Error(), probetest.Leak) {
		t.Fatalf("err %v", err)
	}
}

func TestNoPlace(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()
	_, err := newWeather(srv, nil).Forecast(context.Background(), providers.WeatherQuery{Location: "Несуществующий", Days: 1})
	if !errors.Is(err, providers.ErrPlaceNotFound) {
		t.Fatalf("err %v", err)
	}
}

func TestModule(t *testing.T) {
	if metno.Module.ID != "met-norway" || metno.Module.Kind != providers.KindWeather ||
		len(metno.Module.Secrets) != 0 || len(metno.Module.OptionalSecrets) != 0 || metno.Module.Probe == nil {
		t.Fatalf("module %+v", metno.Module)
	}
	v, err := metno.Module.New([]byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v.(providers.Weather); !ok {
		t.Fatalf("built %T", v)
	}
}
