package openmeteo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// probePlace is geocoded by every test: Open-Meteo is free and keyless, so the
// test reads one place, past the cache, and asks for no forecast
const probePlace = "Bishkek"

func probe(ctx context.Context, v any) error {
	w, ok := v.(*weather)
	if !ok {
		return providers.ProbeBadResponse
	}
	q := url.Values{"name": {probePlace}, "count": {"1"}, "format": {"json"}}
	b, err := providers.ProbeGET(ctx, w.client, w.geoURL+"?"+q.Encode(), http.Header{})
	if err != nil {
		return err
	}
	var body struct {
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}
	if json.Unmarshal(b, &body) != nil || len(body.Results) == 0 || body.Results[0].Name == "" {
		return providers.ProbeBadResponse
	}
	return nil
}
