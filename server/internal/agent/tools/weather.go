package tools

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

const weatherInput = `{
	"type": "object",
	"properties": {
		"location": {"type": "string", "minLength": 1, "maxLength": 100},
		"days": {"type": "integer", "minimum": 1, "maximum": 3},
		"day": {"enum": ["today", "tomorrow", "day_after_tomorrow"]}
	},
	"additionalProperties": false
}`

var dayIndex = map[string]int{"today": 0, "tomorrow": 1, "day_after_tomorrow": 2}

type WeatherDeps struct {
	Settings SettingsFunc
	Weather  func(ctx context.Context, name string) (providers.Weather, error)
}

type currentOut struct {
	Time        string  `json:"time"`
	Temperature float64 `json:"temperature"`
	Code        int     `json:"code"`
	Summary     string  `json:"summary"`
}

type dayOut struct {
	Date            string   `json:"date"`
	Min             float64  `json:"min"`
	Max             float64  `json:"max"`
	PrecipitationMm *float64 `json:"precipitationMm,omitempty"`
	Code            int      `json:"code"`
	Summary         string   `json:"summary"`
}

// Weather is the forecast tool (spec §6).
func Weather(d WeatherDeps) agent.Tool {
	return agent.Tool{
		Name: "weather",
		Description: "Weather forecast in metric units. Use for any weather or temperature question. " +
			"Omit location for the user's home place. Use day for one day, days (1-3) for several.",
		Input:   weatherInput,
		Effects: agent.Read, Output: agent.Trusted,
		Available: func(v settings.Values) bool { return v.Weather != "" },
		Timeout:   18 * time.Second,
		Run: func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			var a struct {
				Location string `json:"location"`
				Days     int    `json:"days"`
				Day      string `json:"day"`
			}
			if err := json.Unmarshal(c.Args, &a); err != nil {
				return nil, badArgs("The arguments are not a JSON object.")
			}
			if a.Day != "" && a.Days != 0 {
				return nil, badArgs("Give day or days, not both.")
			}
			v, err := d.Settings(ctx, c.DeviceID)
			if err != nil {
				return nil, internal()
			}
			if v.Weather == "" {
				return nil, unavailable("No weather provider is set up.")
			}
			if a.Location == "" {
				a.Location = v.WeatherLocation
			}
			if a.Location == "" {
				return nil, unavailable("No location was given and no home place is set.")
			}
			w, err := d.Weather(ctx, v.Weather)
			if err != nil {
				return nil, unavailable("The weather provider is not available.")
			}
			n, idx := a.Days, -1
			if a.Day != "" {
				idx = dayIndex[a.Day]
				n = idx + 1
			}
			if n == 0 {
				n = 1
			}
			f, err := w.Forecast(ctx, providers.WeatherQuery{Location: a.Location, Days: n, Language: v.Language, TimeZone: v.TimeZone})
			if errors.Is(err, providers.ErrPlaceNotFound) {
				return nil, &agent.ToolError{
					Code: "location_not_found", Message: "No place by that name was found.", Retryable: true,
					Hint: "Use the place's English name or a larger nearby city; ask the user if unsure.",
				}
			}
			if err != nil {
				return nil, failed(providers.CodeWeatherFailed)
			}
			days := f.Days
			if idx >= 0 {
				// a day left out for missing data would shift the one asked for
				if len(days) < n {
					return nil, unavailable("The forecast has no data for that day.")
				}
				days = days[idx : idx+1]
			} else if len(days) > n {
				days = days[:n]
			}
			out := make([]dayOut, len(days))
			for i, x := range days {
				out[i] = dayOut{x.Date, x.Min, x.Max, x.PrecipitationMm, x.Code, x.Summary}
			}
			result := map[string]any{"place": f.Place, "days": out}
			if c := f.Current; c != nil {
				result["current"] = currentOut{Time: c.Time, Temperature: c.Temperature, Code: c.Code, Summary: c.Summary}
			}
			return encode(result)
		},
	}
}
