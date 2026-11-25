package hermes

import (
	"encoding/json"
	"errors"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// Module is the provider kind hermes: config {baseUrl, maxRuns} (https only,
// a path prefix kept; 1 to 16 runs at once, default 4), secret apiKey sent as
// a bearer token. New returns a *Client.
var Module = providers.Module{
	ID:   "hermes",
	Kind: providers.KindHermes,
	ConfigSchema: `{"type":"object","required":["baseUrl"],"additionalProperties":false,"properties":{` +
		`"baseUrl":{"type":"string","pattern":"^https://.","maxLength":2048},` +
		`"maxRuns":{"type":"integer","minimum":1,"maximum":16}}}`,
	Secrets: []string{"apiKey"},
	Probe:   probe,
	New: func(cfg json.RawMessage, sec map[string]string) (any, error) {
		var c struct {
			BaseURL string `json:"baseUrl"`
			MaxRuns int    `json:"maxRuns"`
		}
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, errors.New("hermes: bad config")
		}
		return NewClient(ClientConfig{BaseURL: c.BaseURL, APIKey: sec["apiKey"], MaxRuns: c.MaxRuns})
	},
}
