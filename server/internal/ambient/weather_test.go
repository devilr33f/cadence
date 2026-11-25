package ambient

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

type fakeWeather struct {
	calls int
	err   error
	day   providers.Day
	got   providers.WeatherQuery
}

func (f *fakeWeather) Forecast(_ context.Context, q providers.WeatherQuery) (providers.Forecast, error) {
	f.calls++
	f.got = q
	if f.err != nil {
		return providers.Forecast{}, f.err
	}
	return providers.Forecast{Place: "Moscow", Days: []providers.Day{f.day}}, nil
}

func TestWeatherLine(t *testing.T) {
	for _, tc := range []struct {
		day       providers.Day
		lang, out string
	}{
		{providers.Day{Min: 2.6, Max: 7.4, Code: 3}, "ru", "+3…+7 °C · облачно"},
		{providers.Day{Min: -4.2, Max: -0.4, Code: 73}, "ru", "−4…0 °C · снег"},
		{providers.Day{Min: 5, Max: 5, Code: 0}, "en", "+5 °C · clear"},
		{providers.Day{Min: 1, Max: 2, Code: 42}, "de", "+1…+2 °C"},
	} {
		if got := WeatherLine(tc.day, tc.lang); got != tc.out {
			t.Errorf("%+v %s: %q, want %q", tc.day, tc.lang, got, tc.out)
		}
	}
}

// the line is read once per place a while, a failure is retried after its shorter wait, and ambient.next carries it
func TestWeatherLinesCacheAndNext(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	fw := &fakeWeather{day: providers.Day{Min: 1, Max: 4, Code: 61}}
	place := WeatherPlace{Provider: "om", Location: "Moscow", Language: "ru", TimeZone: "Europe/Moscow"}
	w := NewWeatherLines(WeatherDeps{
		Place: func(_ context.Context, id string) (WeatherPlace, bool) { return place, id == "tv" },
		Weather: func(_ context.Context, name string) (providers.Weather, error) {
			if name != "om" {
				return nil, providers.ErrNotFound
			}
			return fw, nil
		},
		Now: func() time.Time { return now },
	})
	ctx := context.Background()
	if got := w.Line(ctx, "tv"); got != "+1…+4 °C · дождь" || fw.got.Location != "Moscow" || fw.got.Language != "ru" || fw.got.TimeZone != "Europe/Moscow" {
		t.Fatalf("line %q, query %+v", got, fw.got)
	}
	w.Line(ctx, "tv")
	if fw.calls != 1 {
		t.Fatalf("%d calls within the fresh window", fw.calls)
	}
	if got := w.Line(ctx, "other"); got != "" {
		t.Fatalf("a device without a place: %q", got)
	}
	// stale and failing: the last line stays a while
	now = now.Add(weatherFresh + time.Minute)
	fw.err = errors.New("down")
	if got := w.Line(ctx, "tv"); got != "+1…+4 °C · дождь" || fw.calls != 2 {
		t.Fatalf("stale %q after %d calls", got, fw.calls)
	}
	// a place never read fails: none, and it is not asked again until the failure window passes
	place.Location = "Nowhere"
	if got := w.Line(ctx, "tv"); got != "" {
		t.Fatalf("failed %q", got)
	}
	w.Line(ctx, "tv")
	if fw.calls != 3 {
		t.Fatalf("%d calls", fw.calls)
	}
	now = now.Add(weatherFailed)
	fw.err = nil
	if got := w.Line(ctx, "tv"); got == "" || fw.calls != 4 {
		t.Fatalf("after the failure window %q, %d calls", got, fw.calls)
	}

	s := New(Config{Manifest: func() *Manifest { return manifestOf(t) }, Weather: w})
	status, body := s.Handle(ctx, Device{ID: "tv", Language: "ru", Profile: stick}, nil)
	var out map[string]any
	if status != rpc.StatusOK || json.Unmarshal(body, &out) != nil || out["weather"] != "+1…+4 °C · дождь" {
		t.Fatalf("%s %s", status, body)
	}
}
