package providers

import (
	"context"
	"errors"
)

// ChainWeather tries each provider in order. An unknown place is a request
// problem, not an outage, so it does not move to the next provider.
func ChainWeather(chain ...Weather) Weather {
	return weatherChain(chain)
}

type weatherChain []Weather

func (chain weatherChain) Forecast(ctx context.Context, q WeatherQuery) (Forecast, error) {
	var last error
	for _, w := range chain {
		f, err := w.Forecast(ctx, q)
		if err == nil {
			return f, nil
		}
		if errors.Is(err, ErrPlaceNotFound) || ctx.Err() != nil {
			return Forecast{}, err
		}
		last = err
	}
	if last == nil {
		last = errors.New(CodeWeatherFailed)
	}
	return Forecast{}, last
}

// WeatherWithFallback selects name first, then every other configured weather
// instance in the store's creation order (name breaks ties). Instances that no
// longer build are skipped; the selected instance's build error is returned if
// nothing can be built.
func (s *Instances) WeatherWithFallback(ctx context.Context, name string) (Weather, error) {
	rows, err := s.q.ListProviderInstances(ctx)
	if err != nil {
		return nil, err
	}
	names := []string{name}
	for _, row := range rows {
		if row.Name == name {
			continue
		}
		if mod, ok := s.reg.Module(row.Module); ok && mod.Kind == KindWeather {
			names = append(names, row.Name)
		}
	}
	chain := make([]Weather, 0, len(names))
	var selectedErr error
	for _, instance := range names {
		w, err := s.Weather(ctx, instance)
		if err != nil {
			if instance == name {
				selectedErr = err
			}
			continue
		}
		chain = append(chain, w)
	}
	if len(chain) == 0 {
		if selectedErr != nil {
			return nil, selectedErr
		}
		return nil, ErrNotFound
	}
	return ChainWeather(chain...), nil
}
