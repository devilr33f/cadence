// Package openmeteo geocodes a place and fetches a daily forecast from
// Open-Meteo. Queries, places and upstream bodies never reach an error or log.
package openmeteo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	defaultGeocodingURL = "https://geocoding-api.open-meteo.com/v1/search"
	defaultForecastURL  = "https://api.open-meteo.com/v1/forecast"
	requestTimeout      = 8 * time.Second
	maxBodyBytes        = 1 << 20
	maxDays             = 16
	currentFields       = "weather_code,temperature_2m"
	dailyFields         = "weather_code,temperature_2m_max,temperature_2m_min,precipitation_sum"
)

type Config struct {
	GeocodingURL, ForecastURL string
	// Log receives provider_panic; nil uses slog default
	Log *slog.Logger
	// Now is the cache clock; nil uses time.Now
	Now func() time.Time
}

type weather struct {
	log      *slog.Logger
	geoURL   string
	fcURL    string
	client   *http.Client
	geocoder *Geocoder
}

func New(c Config, client *http.Client) providers.Weather {
	if c.GeocodingURL == "" {
		c.GeocodingURL = defaultGeocodingURL
	}
	if c.ForecastURL == "" {
		c.ForecastURL = defaultForecastURL
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &weather{
		log: c.Log, geoURL: c.GeocodingURL, fcURL: c.ForecastURL, client: client,
		geocoder: NewGeocoder(c, client),
	}
}

var errFailed = errors.New(providers.CodeWeatherFailed)

func (w *weather) Forecast(ctx context.Context, q providers.WeatherQuery) (providers.Forecast, error) {
	var out providers.Forecast
	var err error
	if providers.Guard(w.log, "open-meteo", providers.CodeWeatherFailed, func() { out, err = w.forecast(ctx, q) }) {
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
	days := min(max(q.Days, 1), maxDays)
	p, err := w.geocoder.Locate(ctx, q.Location, q.Language)
	if err != nil {
		return providers.Forecast{}, err
	}
	v := url.Values{}
	v.Set("latitude", strconv.FormatFloat(p.Latitude, 'f', -1, 64))
	v.Set("longitude", strconv.FormatFloat(p.Longitude, 'f', -1, 64))
	v.Set("current", currentFields)
	v.Set("daily", dailyFields)
	if q.TimeZone == "" {
		v.Set("timezone", "auto")
	} else {
		v.Set("timezone", q.TimeZone)
	}
	v.Set("forecast_days", strconv.Itoa(days))
	// pointers, since a null in these arrays is missing data, not 0 ("Clear sky")
	var body struct {
		UTCOffsetSeconds int `json:"utc_offset_seconds"`
		Current          struct {
			Time        string   `json:"time"`
			Code        *int     `json:"weather_code"`
			Temperature *float64 `json:"temperature_2m"`
		} `json:"current"`
		Daily struct {
			Time []string   `json:"time"`
			Code []*int     `json:"weather_code"`
			Max  []*float64 `json:"temperature_2m_max"`
			Min  []*float64 `json:"temperature_2m_min"`
			Prec []*float64 `json:"precipitation_sum"`
		} `json:"daily"`
	}
	if err := w.get(ctx, w.fcURL, v, &body); err != nil {
		return providers.Forecast{}, err
	}
	d := body.Daily
	n := len(d.Time)
	if n == 0 || len(d.Code) != n || len(d.Max) != n || len(d.Min) != n || len(d.Prec) != n {
		return providers.Forecast{}, errFailed
	}
	f := providers.Forecast{Place: p.Name}
	if c := body.Current; c.Code != nil && c.Temperature != nil && c.Time != "" {
		loc := time.FixedZone("", body.UTCOffsetSeconds)
		if q.TimeZone != "" {
			loaded, err := time.LoadLocation(q.TimeZone)
			if err != nil {
				return providers.Forecast{}, errFailed
			}
			loc = loaded
		}
		at, err := time.ParseInLocation("2006-01-02T15:04", c.Time, loc)
		if err != nil {
			return providers.Forecast{}, errFailed
		}
		f.Current = &providers.Current{
			Time: at.Format(time.RFC3339), Temperature: *c.Temperature, Code: *c.Code, Summary: Summary(*c.Code),
		}
	}
	for i := range n {
		if d.Code[i] == nil || d.Max[i] == nil || d.Min[i] == nil {
			continue
		}
		f.Days = append(f.Days, providers.Day{
			Date: d.Time[i], Min: *d.Min[i], Max: *d.Max[i], PrecipitationMm: d.Prec[i],
			Code: *d.Code[i], Summary: Summary(*d.Code[i]),
		})
	}
	if len(f.Days) == 0 {
		return providers.Forecast{}, errFailed
	}
	return f, nil
}

func (w *weather) get(ctx context.Context, base string, v url.Values, into any) error {
	return getJSON(ctx, w.client, base, v, into)
}

func getJSON(ctx context.Context, client *http.Client, base string, v url.Values, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+v.Encode(), nil)
	if err != nil {
		return errFailed
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return errFailed
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return errFailed
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return errFailed
	}
	if decodeHook != nil {
		decodeHook(b)
	}
	if json.Unmarshal(b, into) != nil {
		return errFailed
	}
	return nil
}
