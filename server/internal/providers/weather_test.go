package providers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/secrets"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

type chainWeather struct {
	name  string
	calls *[]string
	err   error
}

func (w chainWeather) Forecast(context.Context, providers.WeatherQuery) (providers.Forecast, error) {
	*w.calls = append(*w.calls, w.name)
	if w.err != nil {
		return providers.Forecast{}, w.err
	}
	return providers.Forecast{Place: w.name, Days: []providers.Day{{Date: "2026-11-15"}}}, nil
}

func weatherModule(id string, build func() providers.Weather) providers.Module {
	return providers.Module{
		ID: id, Kind: providers.KindWeather, ConfigSchema: `{"type":"object","additionalProperties":false}`,
		New: func(_ json.RawMessage, _ map[string]string) (any, error) { return build(), nil },
	}
}

func TestWeatherWithFallbackOrder(t *testing.T) {
	pool := storetest.Pool(t)
	box, _ := secrets.New(bytes.Repeat([]byte{9}, 32))
	var calls []string
	mods := []providers.Module{
		weatherModule("one", func() providers.Weather { return chainWeather{name: "backup-first-created", calls: &calls} }),
		weatherModule("two", func() providers.Weather {
			return chainWeather{name: "selected", calls: &calls, err: errors.New(providers.CodeWeatherFailed)}
		}),
		weatherModule("three", func() providers.Weather { return chainWeather{name: "last", calls: &calls} }),
	}
	ins := providers.NewInstances(pool, box, providers.NewRegistry(false, mods...))
	ctx := context.Background()
	modules := map[string]string{"backup": "one", "selected": "two", "last": "three"}
	for _, name := range []string{"backup", "selected", "last"} {
		if _, err := ins.Create(ctx, name, modules[name], nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	w, err := ins.WeatherWithFallback(ctx, "selected")
	if err != nil {
		t.Fatal(err)
	}
	f, err := w.Forecast(ctx, providers.WeatherQuery{Location: "Москва"})
	if err != nil || f.Place != "backup-first-created" {
		t.Fatalf("forecast %+v, err %v", f, err)
	}
	if want := []string{"selected", "backup-first-created"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls %v, want %v", calls, want)
	}
}

func TestWeatherFallbackDoesNotRetryUnknownPlace(t *testing.T) {
	calls := []string{}
	w := providers.ChainWeather(
		chainWeather{name: "first", calls: &calls, err: providers.ErrPlaceNotFound},
		chainWeather{name: "second", calls: &calls},
	)
	_, err := w.Forecast(context.Background(), providers.WeatherQuery{})
	if !errors.Is(err, providers.ErrPlaceNotFound) || !reflect.DeepEqual(calls, []string{"first"}) {
		t.Fatalf("err %v, calls %v", err, calls)
	}
}
