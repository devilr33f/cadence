package t3code

import (
	"encoding/json"
	"errors"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// Module is the provider kind t3code: config {baseUrl} (http for a T3 Code
// on the LAN or https, a path prefix kept), secret token (a T3 session token
// with the orchestration:read and orchestration:operate scopes, sent only as
// a bearer). New returns a *Client, which connects once Run runs.
var Module = providers.Module{
	ID:   "t3code",
	Kind: providers.KindT3Code,
	ConfigSchema: `{"type":"object","required":["baseUrl"],"additionalProperties":false,"properties":{` +
		`"baseUrl":{"type":"string","pattern":"^https?://.","maxLength":2048}}}`,
	Secrets: []string{"token"},
	Probe:   probe,
	New: func(cfg json.RawMessage, sec map[string]string) (any, error) {
		var c struct {
			BaseURL string `json:"baseUrl"`
		}
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, errors.New("t3code: bad config")
		}
		return NewClient(ClientConfig{BaseURL: c.BaseURL, Token: sec["token"]})
	},
}
