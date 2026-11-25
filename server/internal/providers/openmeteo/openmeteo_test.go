package openmeteo_test

import (
	"context"
	"crypto/x509"
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
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
	"git.shino.boo/devilreef/cadence/server/internal/providers/openmeteo"
)

const upstreamLeak = "LEAKY-UPSTREAM-TEXT"

func roots(srv *httptest.Server) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(srv.Certificate())
	return p
}

func newWeather(srv *httptest.Server, now func() time.Time) providers.Weather {
	return openmeteo.New(openmeteo.Config{
		GeocodingURL: srv.URL + "/v1/search", ForecastURL: srv.URL + "/v1/forecast", Now: now,
	}, httpx.Client(roots(srv)))
}

type upstream struct {
	geo, fc atomic.Int32
	geoQ    atomic.Value
	fcQ     atomic.Value
}

func (u *upstream) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/search":
		u.geo.Add(1)
		u.geoQ.Store(r.URL.Query())
		_, _ = io.WriteString(w, `{"results":[{"name":"Bishkek","country":"Kyrgyzstan","latitude":42.87,"longitude":74.59}]}`)
	case "/v1/forecast":
		u.fc.Add(1)
		u.fcQ.Store(r.URL.Query())
		_, _ = io.WriteString(w, `{"utc_offset_seconds":21600,"current":{"time":"2026-11-08T12:00","weather_code":0,"temperature_2m":7.5},`+
			`"daily":{"time":["2026-11-08","2026-11-09"],"weather_code":[0,61],`+
			`"temperature_2m_max":[12.5,9],"temperature_2m_min":[3,1.5],"precipitation_sum":[0,4.2]}}`)
	default:
		http.NotFound(w, r)
	}
}

func TestForecast(t *testing.T) {
	u := &upstream{}
	srv := httptest.NewTLSServer(http.HandlerFunc(u.handler))
	defer srv.Close()
	w := newWeather(srv, nil)
	f, err := w.Forecast(context.Background(), providers.WeatherQuery{Location: "Bishkek", Days: 2, Language: "ru", TimeZone: "Asia/Bishkek"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Place != "Bishkek, Kyrgyzstan" || len(f.Days) != 2 {
		t.Fatalf("forecast %+v", f)
	}
	if f.Current == nil || f.Current.Time != "2026-11-08T12:00:00+06:00" || f.Current.Temperature != 7.5 || f.Current.Code != 0 {
		t.Fatalf("current %+v", f.Current)
	}
	d := f.Days[1]
	if d.Date != "2026-11-09" || d.Max != 9 || d.Min != 1.5 || d.PrecipitationMm == nil || *d.PrecipitationMm != 4.2 || d.Code != 61 || d.Summary != "Slight rain" {
		t.Fatalf("day %+v", d)
	}
	if f.Days[0].Summary != "Clear sky" {
		t.Fatalf("day0 %+v", f.Days[0])
	}
	gq := u.geoQ.Load().(url.Values)
	if gq.Get("name") != "Bishkek" || gq.Get("count") != "1" || gq.Get("language") != "ru" {
		t.Fatalf("geocode query %v", gq)
	}
	fq := u.fcQ.Load().(url.Values)
	if fq.Get("latitude") != "42.87" || fq.Get("longitude") != "74.59" || fq.Get("timezone") != "Asia/Bishkek" ||
		fq.Get("current") != "weather_code,temperature_2m" ||
		fq.Get("forecast_days") != "2" || fq.Get("daily") != "weather_code,temperature_2m_max,temperature_2m_min,precipitation_sum" {
		t.Fatalf("forecast query %v", fq)
	}
}

func TestGeocodeCache(t *testing.T) {
	u := &upstream{}
	srv := httptest.NewTLSServer(http.HandlerFunc(u.handler))
	defer srv.Close()
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	w := newWeather(srv, func() time.Time { return now })
	q := providers.WeatherQuery{Location: "Bishkek", Days: 1, Language: "en"}
	for range 2 {
		if _, err := w.Forecast(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	if u.geo.Load() != 1 || u.fc.Load() != 2 {
		t.Fatalf("geo %d fc %d", u.geo.Load(), u.fc.Load())
	}
	q.Language = "ru"
	if _, err := w.Forecast(context.Background(), q); err != nil || u.geo.Load() != 2 {
		t.Fatalf("language key: err %v geo %d", err, u.geo.Load())
	}
	now = now.Add(25 * time.Hour)
	q.Language = "en"
	if _, err := w.Forecast(context.Background(), q); err != nil || u.geo.Load() != 3 {
		t.Fatalf("ttl: err %v geo %d", err, u.geo.Load())
	}
}

func TestGeocodeCacheBounded(t *testing.T) {
	u := &upstream{}
	srv := httptest.NewTLSServer(http.HandlerFunc(u.handler))
	defer srv.Close()
	w := newWeather(srv, nil)
	for i := range 300 {
		q := providers.WeatherQuery{Location: strings.Repeat("a", i+1), Days: 1}
		if _, err := w.Forecast(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	if n := openmeteo.CacheLen(w); n > 256 {
		t.Fatalf("cache %d", n)
	}
}

func assertFailed(t *testing.T, err error) {
	t.Helper()
	if err == nil || err.Error() != providers.CodeWeatherFailed {
		t.Fatalf("err %v", err)
	}
}

// a place geocoding does not know is the model's to fix, not an outage
func TestNoGeocodeResult(t *testing.T) {
	for _, body := range []string{`{"generationtime_ms":0.1}`, `{"results":[]}`, `{"results":[{"name":""}]}`} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, body)
		}))
		_, err := newWeather(srv, nil).Forecast(context.Background(), providers.WeatherQuery{Location: "Nowhere-xyz", Days: 1})
		srv.Close()
		if !errors.Is(err, providers.ErrPlaceNotFound) || strings.Contains(err.Error(), "Nowhere-xyz") {
			t.Fatalf("%s: err %v", body, err)
		}
	}
}

// a null in the daily arrays is missing data: a day without its code or
// temperatures is left out, and a null precipitation is unknown, not 0
func TestForecastNulls(t *testing.T) {
	forecast := func(daily string) (providers.Forecast, error) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/search" {
				_, _ = io.WriteString(w, `{"results":[{"name":"Bishkek","latitude":42.87,"longitude":74.59}]}`)
				return
			}
			_, _ = io.WriteString(w, `{"daily":`+daily+`}`)
		}))
		defer srv.Close()
		return newWeather(srv, nil).Forecast(context.Background(), providers.WeatherQuery{Location: "Bishkek", Days: 5})
	}
	_, err := forecast(`{"time":["2026-11-08"],"weather_code":[null],"temperature_2m_max":[1],"temperature_2m_min":[0],"precipitation_sum":[0]}`)
	assertFailed(t, err)
	f, err := forecast(`{"time":["2026-11-08","2026-11-09","2026-11-10","2026-11-11","2026-11-12"],` +
		`"weather_code":[97,null,71,73,75],"temperature_2m_max":[12,9,null,3,2],"temperature_2m_min":[3,1,0,null,-4],` +
		`"precipitation_sum":[null,1,2,3,4.5]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Days) != 2 || f.Days[0].Date != "2026-11-08" || f.Days[1].Date != "2026-11-12" {
		t.Fatalf("days %+v", f.Days)
	}
	if d := f.Days[0]; d.Code != 97 || d.Summary != "Heavy thunderstorm" || d.PrecipitationMm != nil {
		t.Fatalf("day %+v", d)
	}
	if d := f.Days[1]; d.Summary != "Heavy snowfall" || d.PrecipitationMm == nil || *d.PrecipitationMm != 4.5 || d.Min != -4 {
		t.Fatalf("day %+v", d)
	}
}

func TestHTTPErrorHidesUpstream(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, upstreamLeak)
	}))
	defer srv.Close()
	_, err := newWeather(srv, nil).Forecast(context.Background(), providers.WeatherQuery{Location: "x", Days: 1})
	assertFailed(t, err)
	if strings.Contains(err.Error(), upstreamLeak) {
		t.Fatal("leak")
	}
}

func TestUntrustedTLS(t *testing.T) {
	u := &upstream{}
	srv := httptest.NewTLSServer(http.HandlerFunc(u.handler))
	defer srv.Close()
	w := openmeteo.New(openmeteo.Config{GeocodingURL: srv.URL + "/v1/search", ForecastURL: srv.URL + "/v1/forecast"}, httpx.Client(nil))
	_, err := w.Forecast(context.Background(), providers.WeatherQuery{Location: "x", Days: 1})
	assertFailed(t, err)
	if u.geo.Load() != 0 {
		t.Fatal("request reached untrusted upstream")
	}
}

func TestRedirectRefused(t *testing.T) {
	var hit atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit.Add(1) }))
	defer target.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer srv.Close()
	_, err := newWeather(srv, nil).Forecast(context.Background(), providers.WeatherQuery{Location: "x", Days: 1})
	assertFailed(t, err)
	if hit.Load() != 0 {
		t.Fatal("redirect followed")
	}
}

func TestPanicIsFailure(t *testing.T) {
	u := &upstream{}
	srv := httptest.NewTLSServer(http.HandlerFunc(u.handler))
	defer srv.Close()
	openmeteo.SetDecodeHook(t, func([]byte) { panic(upstreamLeak) })
	_, err := newWeather(srv, nil).Forecast(context.Background(), providers.WeatherQuery{Location: "x", Days: 1})
	assertFailed(t, err)
}

func TestModule(t *testing.T) {
	if openmeteo.Module.Kind != providers.KindWeather || len(openmeteo.Module.Secrets) != 0 {
		t.Fatal("module")
	}
	v, err := openmeteo.Module.New([]byte(`{}`), nil)
	if err != nil || v == nil {
		t.Fatal(err)
	}
	if _, ok := v.(providers.Weather); !ok {
		t.Fatal("kind")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal()
	}
}
