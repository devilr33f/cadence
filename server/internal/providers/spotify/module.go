package spotify

import (
	"context"
	"encoding/json"
	"errors"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

// Module takes an app's client id and secret (Spotify for Developers); the
// hosts are fixed so they only go to Spotify. config {market} narrows the
// catalog to one country's.
var Module = providers.Module{
	ID:   "spotify",
	Kind: providers.KindMusic,
	ConfigSchema: `{"type":"object","additionalProperties":false,"properties":{` +
		`"market":{"type":"string","pattern":"^[A-Z]{2}$"}}}`,
	Secrets: []string{"clientId", "clientSecret"},
	Probe:   probe,
	New: func(cfg json.RawMessage, sec map[string]string) (any, error) {
		var c struct {
			Market string `json:"market"`
		}
		if len(cfg) > 0 && json.Unmarshal(cfg, &c) != nil {
			return nil, errors.New("spotify: bad config")
		}
		if sec["clientId"] == "" || sec["clientSecret"] == "" {
			return nil, errors.New("spotify: client credentials missing")
		}
		return New(Config{ClientID: sec["clientId"], ClientSecret: sec["clientSecret"], Market: c.Market}, httpx.Client(nil)), nil
	},
}

// probe fetches an app token, which searches nothing and costs nothing
func probe(ctx context.Context, v any) error {
	c, ok := v.(*Client)
	if !ok {
		return providers.ProbeBadResponse
	}
	_, _, err := c.fetchToken(ctx)
	var status statusError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &status):
		// a wrong client id or secret is a 400 invalid_client
		if status == 400 {
			return providers.ProbeAuth
		}
		return providers.ProbeStatus(int(status))
	case errors.Is(err, errBadAnswer):
		return providers.ProbeBadResponse
	case errors.Is(err, httpx.ErrRedirect):
		return providers.ProbeBadResponse
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return providers.ProbeTimeout
	}
	return providers.ProbeUnreachable
}
