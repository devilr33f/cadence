// Package metno fetches MET Norway's Locationforecast and uses Open-Meteo's
// keyless geocoder for place names. Queries, places and upstream bodies never
// reach an error or log.
package metno

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/openmeteo"
)

const (
	defaultForecastURL = "https://api.met.no/weatherapi/locationforecast/2.0/compact"
	defaultUserAgent   = "Cadence/2.0 https://git.shino.boo/devilreef/cadence"
	requestTimeout     = 8 * time.Second
	maxBodyBytes       = 1 << 20
	maxDays            = 16
)

type Config struct {
	ForecastURL, GeocodingURL string
	// UserAgent identifies Cadence to MET Norway; empty uses the project URL.
	UserAgent string
	// Log receives provider_panic; nil uses slog default.
	Log *slog.Logger
	// Now is the forecast and geocode cache clock; nil uses time.Now.
	Now func() time.Time
}

type weather struct {
	log       *slog.Logger
	url       string
	userAgent string
	now       func() time.Time
	client    *http.Client
	geocoder  *openmeteo.Geocoder
	cache     *forecastCache
}

func New(c Config, client *http.Client) providers.Weather {
	if c.ForecastURL == "" {
		c.ForecastURL = defaultForecastURL
	}
	if c.UserAgent == "" {
		c.UserAgent = defaultUserAgent
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &weather{
		log: c.Log, url: c.ForecastURL, userAgent: c.UserAgent, now: c.Now, client: client,
		geocoder: openmeteo.NewGeocoder(openmeteo.Config{GeocodingURL: c.GeocodingURL, Now: c.Now}, client),
		cache:    newForecastCache(),
	}
}

var errFailed = errors.New(providers.CodeWeatherFailed)

func (w *weather) Forecast(ctx context.Context, q providers.WeatherQuery) (providers.Forecast, error) {
	var out providers.Forecast
	var err error
	if providers.Guard(w.log, "met-norway", providers.CodeWeatherFailed, func() { out, err = w.forecast(ctx, q) }) {
		return providers.Forecast{}, errFailed
	}
	if errors.Is(err, providers.ErrPlaceNotFound) {
		return providers.Forecast{}, providers.ErrPlaceNotFound
	}
	if err != nil {
		return providers.Forecast{}, errFailed
	}
	return out, nil
}

func (w *weather) forecast(ctx context.Context, q providers.WeatherQuery) (providers.Forecast, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	loc := time.UTC
	if q.TimeZone != "" {
		var err error
		if loc, err = time.LoadLocation(q.TimeZone); err != nil {
			return providers.Forecast{}, errFailed
		}
	}
	p, err := w.geocoder.Locate(ctx, q.Location, q.Language)
	if err != nil {
		return providers.Forecast{}, err
	}
	body, err := w.load(ctx, p.Latitude, p.Longitude)
	if err != nil {
		return providers.Forecast{}, err
	}
	var decoded forecastBody
	if json.Unmarshal(body, &decoded) != nil {
		return providers.Forecast{}, errFailed
	}
	return makeForecast(decoded, p.Name, min(max(q.Days, 1), maxDays), loc, w.now())
}

func (w *weather) load(ctx context.Context, lat, lon float64) ([]byte, error) {
	lat, lon = truncateCoordinate(lat), truncateCoordinate(lon)
	key := strconv.FormatFloat(lat, 'f', 4, 64) + "," + strconv.FormatFloat(lon, 'f', 4, 64)
	now := w.now()
	if body, ok := w.cache.fresh(key, now); ok {
		return body, nil
	}
	v := url.Values{"lat": {strconv.FormatFloat(lat, 'f', -1, 64)}, "lon": {strconv.FormatFloat(lon, 'f', -1, 64)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.url+"?"+v.Encode(), nil)
	if err != nil {
		return nil, errFailed
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", w.userAgent)
	cached, hasCached := w.cache.entry(key)
	if hasCached && cached.lastModified != "" {
		req.Header.Set("If-Modified-Since", cached.lastModified)
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, errFailed
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
		if err != nil || len(body) > maxBodyBytes || !json.Valid(body) {
			return nil, errFailed
		}
		w.cache.put(key, cachedForecast{
			body: body, expires: headerTime(resp.Header, "Expires"), lastModified: resp.Header.Get("Last-Modified"),
		})
		return body, nil
	case http.StatusNotModified:
		if !hasCached {
			return nil, errFailed
		}
		cached.expires = headerTime(resp.Header, "Expires")
		if modified := resp.Header.Get("Last-Modified"); modified != "" {
			cached.lastModified = modified
		}
		w.cache.put(key, cached)
		return cached.body, nil
	default:
		return nil, errFailed
	}
}

func headerTime(h http.Header, name string) time.Time {
	t, err := http.ParseTime(h.Get(name))
	if err != nil {
		return time.Time{}
	}
	return t
}

func truncateCoordinate(v float64) float64 { return math.Trunc(v*10_000) / 10_000 }

type forecastBody struct {
	Properties struct {
		Timeseries []timeStep `json:"timeseries"`
	} `json:"properties"`
}

type timeStep struct {
	Time string `json:"time"`
	Data struct {
		Instant struct {
			Details struct {
				AirTemperature *float64 `json:"air_temperature"`
			} `json:"details"`
		} `json:"instant"`
		Next1  *forecastPeriod `json:"next_1_hours"`
		Next6  *forecastPeriod `json:"next_6_hours"`
		Next12 *forecastPeriod `json:"next_12_hours"`
	} `json:"data"`
}

type forecastPeriod struct {
	Summary struct {
		SymbolCode string `json:"symbol_code"`
	} `json:"summary"`
	Details struct {
		PrecipitationAmount *float64 `json:"precipitation_amount"`
	} `json:"details"`
}

func (s timeStep) condition() (int, bool) {
	for _, p := range []*forecastPeriod{s.Data.Next1, s.Data.Next6, s.Data.Next12} {
		if p != nil {
			return symbolCode(p.Summary.SymbolCode)
		}
	}
	return 0, false
}

func (s timeStep) precipitation() (float64, bool) {
	for _, p := range []*forecastPeriod{s.Data.Next1, s.Data.Next6, s.Data.Next12} {
		if p != nil && p.Details.PrecipitationAmount != nil {
			return *p.Details.PrecipitationAmount, true
		}
	}
	return 0, false
}

type dayAggregate struct {
	min, max         float64
	precipitation    float64
	hasPrecipitation bool
	code             int
	hasCode          bool
	noonDistance     time.Duration
}

func makeForecast(body forecastBody, place string, days int, loc *time.Location, now time.Time) (providers.Forecast, error) {
	series := body.Properties.Timeseries
	if len(series) == 0 {
		return providers.Forecast{}, errFailed
	}
	out := providers.Forecast{Place: place}
	byDate := map[string]*dayAggregate{}
	for _, step := range series {
		at, err := time.Parse(time.RFC3339, step.Time)
		if err != nil {
			return providers.Forecast{}, errFailed
		}
		local := at.In(loc)
		code, hasCode := step.condition()
		if out.Current == nil && step.Data.Instant.Details.AirTemperature != nil && hasCode {
			out.Current = &providers.Current{
				Time: local.Format(time.RFC3339), Temperature: *step.Data.Instant.Details.AirTemperature,
				Code: code, Summary: openmeteo.Summary(code),
			}
		}
		temp := step.Data.Instant.Details.AirTemperature
		if temp == nil {
			continue
		}
		date := local.Format(time.DateOnly)
		day := byDate[date]
		if day == nil {
			day = &dayAggregate{min: *temp, max: *temp}
			byDate[date] = day
		} else {
			day.min, day.max = min(day.min, *temp), max(day.max, *temp)
		}
		if amount, ok := step.precipitation(); ok {
			day.precipitation += amount
			day.hasPrecipitation = true
		}
		if hasCode {
			noon := time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, loc)
			distance := local.Sub(noon).Abs()
			if !day.hasCode || distance < day.noonDistance {
				day.code, day.hasCode, day.noonDistance = code, true, distance
			}
		}
	}
	dates := make([]string, 0, len(byDate))
	for date := range byDate {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	today := now.In(loc).Format(time.DateOnly)
	for _, date := range dates {
		if date < today {
			continue
		}
		day := byDate[date]
		if !day.hasCode {
			continue
		}
		d := providers.Day{Date: date, Min: day.min, Max: day.max, Code: day.code, Summary: openmeteo.Summary(day.code)}
		if day.hasPrecipitation {
			amount := day.precipitation
			d.PrecipitationMm = &amount
		}
		out.Days = append(out.Days, d)
		if len(out.Days) == days {
			break
		}
	}
	if len(out.Days) == 0 {
		return providers.Forecast{}, errFailed
	}
	return out, nil
}
