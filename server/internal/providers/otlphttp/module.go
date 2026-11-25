package otlphttp

import (
	"encoding/json"
	"errors"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// Module is the provider kind otlp: one OTLP/HTTP collector. Config {url
// (https only, no userinfo, query or fragment, at most 2048 bytes; /v1/logs
// and /v1/metrics are posted under it), timeoutMs? (1000 to 30000, default
// 10000)}; optional secret authorization, the whole Authorization header
// value, sent only to those two URLs. New returns a *Client.
var Module = providers.Module{
	ID:   "otlp-http",
	Kind: providers.KindOTLP,
	ConfigSchema: `{"type":"object","required":["url"],"additionalProperties":false,"properties":{` +
		`"url":{"type":"string","pattern":"^https://.","maxLength":2048},` +
		`"timeoutMs":{"type":"integer","minimum":1000,"maximum":30000}}}`,
	OptionalSecrets: []string{"authorization"},
	Probe:           probe,
	// the client's own endpoint rules, so a URL it would refuse, as one with a
	// credential in its userinfo or query, never reaches the stored config
	Check: func(cfg json.RawMessage) (string, bool) {
		var c struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(cfg, &c) != nil {
			return "", false
		}
		if _, err := parseEndpoint(c.URL); err != nil {
			return "/url", false
		}
		return "", true
	},
	New: func(cfg json.RawMessage, sec map[string]string) (any, error) {
		var c struct {
			URL       string `json:"url"`
			TimeoutMs int    `json:"timeoutMs"`
		}
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, errors.New("otlphttp: bad config")
		}
		return NewClient(ClientConfig{URL: c.URL, Authorization: sec["authorization"], Timeout: time.Duration(c.TimeoutMs) * time.Millisecond})
	},
}
