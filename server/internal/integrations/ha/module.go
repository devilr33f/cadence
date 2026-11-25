package ha

import (
	"encoding/json"
	"errors"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// Module is the provider kind homeassistant: config {baseUrl} (http for a LAN
// Home Assistant or https, a path prefix kept), secret token (a long-lived
// access token, sent only in the WebSocket auth message). New returns a
// *Client.
var Module = providers.Module{
	ID:   "homeassistant",
	Kind: providers.KindHomeAssistant,
	ConfigSchema: `{"type":"object","required":["baseUrl"],"additionalProperties":false,"properties":{` +
		`"baseUrl":{"type":"string","pattern":"^https?://","maxLength":512}}}`,
	Secrets: []string{"token"},
	Probe:   probe,
	New: func(cfg json.RawMessage, sec map[string]string) (any, error) {
		var c struct {
			BaseURL string `json:"baseUrl"`
		}
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, errors.New("ha: bad config")
		}
		return NewClient(ClientConfig{BaseURL: c.BaseURL, Token: sec["token"]})
	},
}
