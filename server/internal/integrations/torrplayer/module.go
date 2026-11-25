package torrplayer

import (
	"context"
	"encoding/json"
	"errors"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// Module is the catalog provider kind torrplayer: config {baseUrl} (http or
// https, the address torrplayer's control API answers on, which the TVs
// reach too: its play URLs are built on it), secret token (the bearer token
// of its pairing). New returns a *Provider.
var Module = providers.Module{
	ID:   "torrplayer",
	Kind: providers.KindCatalog,
	ConfigSchema: `{"type":"object","required":["baseUrl"],"additionalProperties":false,"properties":{` +
		`"baseUrl":{"type":"string","pattern":"^https?://.","maxLength":2048}}}`,
	Secrets: []string{"token"},
	Probe:   probe,
	New: func(cfg json.RawMessage, sec map[string]string) (any, error) {
		var c struct {
			BaseURL string `json:"baseUrl"`
		}
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, errors.New("torrplayer: bad config")
		}
		cl, err := NewClient(ClientConfig{BaseURL: c.BaseURL, Token: sec["token"]})
		if err != nil {
			return nil, err
		}
		return New(cl), nil
	},
}

// probe reads /status, which needs the token and changes nothing.
func probe(ctx context.Context, v any) error {
	p, ok := v.(*Provider)
	if !ok {
		return providers.ProbeBadResponse
	}
	_, err := p.c.status(ctx)
	var he *httpError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.DeadlineExceeded):
		return providers.ProbeTimeout
	case errors.As(err, &he):
		return providers.ProbeStatus(he.status)
	}
	return providers.ProbeUnreachable
}
