package torrserver

import (
	"context"
	"encoding/json"
	"errors"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// Module is the catalog provider kind torrserver: config {baseUrl,
// publicUrl?, user?} (http or https: the address the server calls, and the
// one the TVs reach when it differs; its play URLs carry no credentials),
// optional secret password (basic auth with user). New returns a *Provider.
var Module = providers.Module{
	ID:   "torrserver",
	Kind: providers.KindCatalog,
	ConfigSchema: `{"type":"object","required":["baseUrl"],"additionalProperties":false,"properties":{` +
		`"baseUrl":{"type":"string","pattern":"^https?://.","maxLength":2048},` +
		`"publicUrl":{"type":"string","pattern":"^https?://.","maxLength":2048},` +
		`"user":{"type":"string","maxLength":128}}}`,
	OptionalSecrets: []string{"password"},
	Probe:           probe,
	New: func(cfg json.RawMessage, sec map[string]string) (any, error) {
		var c struct {
			BaseURL   string `json:"baseUrl"`
			PublicURL string `json:"publicUrl"`
			User      string `json:"user"`
		}
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, errors.New("torrserver: bad config")
		}
		cl, err := NewClient(ClientConfig{BaseURL: c.BaseURL, PublicURL: c.PublicURL, User: c.User, Password: sec["password"]})
		if err != nil {
			return nil, err
		}
		return New(cl), nil
	},
}

// probe lists the torrents: /echo is open, list needs the credentials.
func probe(ctx context.Context, v any) error {
	p, ok := v.(*Provider)
	if !ok {
		return providers.ProbeBadResponse
	}
	_, err := p.c.list(ctx)
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
