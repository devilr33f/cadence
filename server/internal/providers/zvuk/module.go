package zvuk

import (
	"context"
	"encoding/json"
	"errors"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

// Module needs no secret: a guest token from the site plays every song at
// 128 kbps. The optional token is a signed-in account's (its `auth` cookie),
// which with a subscription plays high (320 kbps) and flac. The host is
// fixed so the token only goes to Zvuk.
var Module = providers.Module{
	ID:   "zvuk",
	Kind: providers.KindMusic,
	ConfigSchema: `{"type":"object","additionalProperties":false,"properties":{` +
		`"quality":{"enum":["mid","high","flac"]}}}`,
	OptionalSecrets: []string{"token"},
	Probe:           probe,
	New: func(raw json.RawMessage, sec map[string]string) (any, error) {
		var cfg struct {
			Quality string `json:"quality"`
		}
		if len(raw) > 0 && json.Unmarshal(raw, &cfg) != nil {
			return nil, errors.New("zvuk: bad config")
		}
		switch cfg.Quality {
		case "", QualityMid, QualityHigh, QualityFLAC:
		default:
			return nil, errors.New("zvuk: bad quality")
		}
		if t := sec["token"]; t != "" && !tokenRe.MatchString(t) {
			return nil, errors.New("zvuk: bad token")
		}
		return New(Config{Token: sec["token"], Quality: cfg.Quality}, httpx.Client(nil)), nil
	},
}

// probe gets a guest token when it has none and runs one search, which
// costs nothing
func probe(ctx context.Context, v any) error {
	c, ok := v.(*Client)
	if !ok {
		return providers.ProbeBadResponse
	}
	var a searchAnswer
	err := c.search(ctx, "test", 1, "tracks", &a)
	var status statusError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &status):
		return providers.ProbeStatus(int(status))
	case errors.Is(err, errBadAnswer), errors.Is(err, errGraphQL), errors.Is(err, errBlocked),
		errors.Is(err, errNoToken), errors.Is(err, httpx.ErrRedirect):
		return providers.ProbeBadResponse
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return providers.ProbeTimeout
	}
	return providers.ProbeUnreachable
}
