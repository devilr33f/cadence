package metno

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// probe reads one compact Moscow forecast: MET Norway has no separate status
// endpoint, and this call is free and needs no geocoding.
func probe(ctx context.Context, v any) error {
	w, ok := v.(*weather)
	if !ok {
		return providers.ProbeBadResponse
	}
	q := url.Values{"lat": {"55.7558"}, "lon": {"37.6173"}}
	header := http.Header{"User-Agent": {w.userAgent}}
	body, err := providers.ProbeGET(ctx, w.client, w.url+"?"+q.Encode(), header)
	if err != nil {
		return err
	}
	var decoded forecastBody
	if json.Unmarshal(body, &decoded) != nil || len(decoded.Properties.Timeseries) == 0 {
		return providers.ProbeBadResponse
	}
	first := decoded.Properties.Timeseries[0]
	if first.Data.Instant.Details.AirTemperature == nil {
		return providers.ProbeBadResponse
	}
	if _, ok := first.condition(); !ok {
		return providers.ProbeBadResponse
	}
	return nil
}
