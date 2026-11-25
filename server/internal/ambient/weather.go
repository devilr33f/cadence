package ambient

import (
	"context"
	"math"
	"strconv"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	// a place's line is asked again after this; a failed lookup after the shorter one, so a provider that is down is
	// not asked at every clip
	weatherFresh  = 30 * time.Minute
	weatherFailed = 5 * time.Minute
	// ambient.next never waits longer than this for a forecast
	weatherWait = 3 * time.Second
)

// WeatherPlace is what a screen's weather line is read for: the weather provider instance, the home place, language
// and household time zone, as the device's settings name them. A zero Provider or Location is no line.
type WeatherPlace struct {
	Provider, Location, Language, TimeZone string
}

// WeatherDeps read the device's place and its provider.
type WeatherDeps struct {
	Place   func(ctx context.Context, deviceID string) (WeatherPlace, bool)
	Weather func(ctx context.Context, name string) (providers.Weather, error)
	Now     func() time.Time
}

// WeatherLines is the line ambient's overlay shows ("+3…+7 °C · облачно"), today's forecast at the device's home
// place, kept in memory per place; the place is never logged.
type WeatherLines struct {
	d WeatherDeps

	mu    sync.Mutex
	cache map[WeatherPlace]weatherEntry
}

type weatherEntry struct {
	line string
	at   time.Time
	ok   bool
}

func NewWeatherLines(d WeatherDeps) *WeatherLines {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &WeatherLines{d: d, cache: map[WeatherPlace]weatherEntry{}}
}

// Line is the device's weather line, "" when it has none (no provider or place set, or the provider failed).
func (w *WeatherLines) Line(ctx context.Context, deviceID string) string {
	if w == nil || w.d.Place == nil || w.d.Weather == nil {
		return ""
	}
	p, ok := w.d.Place(ctx, deviceID)
	if !ok || p.Provider == "" || p.Location == "" {
		return ""
	}
	now := w.d.Now()
	w.mu.Lock()
	e, hit := w.cache[p]
	w.mu.Unlock()
	if hit && (e.ok && now.Sub(e.at) < weatherFresh || !e.ok && now.Sub(e.at) < weatherFailed) {
		return e.line
	}
	line, fetched := w.fetch(ctx, p)
	w.mu.Lock()
	if !fetched && hit && e.ok && now.Sub(e.at) < 2*weatherFresh {
		// a failed refresh keeps the last line a while
		line = e.line
	} else {
		w.cache[p] = weatherEntry{line: line, at: now, ok: fetched}
	}
	w.mu.Unlock()
	return line
}

func (w *WeatherLines) fetch(ctx context.Context, p WeatherPlace) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, weatherWait)
	defer cancel()
	prov, err := w.d.Weather(ctx, p.Provider)
	if err != nil {
		return "", false
	}
	f, err := prov.Forecast(ctx, providers.WeatherQuery{Location: p.Location, Days: 1, Language: p.Language, TimeZone: p.TimeZone})
	if err != nil || len(f.Days) == 0 {
		return "", false
	}
	return WeatherLine(f.Days[0], p.Language), true
}

// WeatherLine formats a day for the overlay: its temperatures and a word for its WMO code in the language (ru, else
// en).
func WeatherLine(d providers.Day, lang string) string {
	lo, hi := int(math.Round(d.Min)), int(math.Round(d.Max))
	t := signed(hi)
	if lo != hi {
		t = signed(lo) + "…" + signed(hi)
	}
	line := t + " °C"
	if word := weatherWord(d.Code, lang); word != "" {
		line += " · " + word
	}
	return line
}

func signed(n int) string {
	switch {
	case n > 0:
		return "+" + strconv.Itoa(n)
	case n < 0:
		return "−" + strconv.Itoa(-n)
	}
	return "0"
}

// the WMO weather interpretation codes, by group
func weatherWord(code int, lang string) string {
	group := ""
	switch {
	case code == 0:
		group = "clear"
	case code >= 1 && code <= 3:
		group = "cloudy"
	case code == 45 || code == 48:
		group = "fog"
	case code >= 51 && code <= 57:
		group = "drizzle"
	case code >= 61 && code <= 67:
		group = "rain"
	case code >= 71 && code <= 77:
		group = "snow"
	case code >= 80 && code <= 82:
		group = "showers"
	case code == 85 || code == 86:
		group = "snow showers"
	case code >= 95 && code <= 99:
		group = "thunderstorm"
	}
	if lang == "ru" {
		return ruWeather[group]
	}
	return group
}

var ruWeather = map[string]string{
	"clear": "ясно", "cloudy": "облачно", "fog": "туман", "drizzle": "морось", "rain": "дождь", "snow": "снег",
	"showers": "ливень", "snow showers": "снегопад", "thunderstorm": "гроза",
}
