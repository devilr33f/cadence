package mcp

import (
	"encoding/json"
	"errors"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// Module is the provider kind mcp: one MCP server over streamable HTTP.
// Config {url (https only, no userinfo, query or fragment), toolPrefix?,
// timeoutMs? (1000 to 30000, default 8000), maxResultChars? (256 to 65536,
// default 8192)}; optional secret authorization, the whole Authorization
// header value, sent only to url. New returns a *Client; a supervisor
// connects it.
var Module = providers.Module{
	ID:   "mcp-http",
	Kind: providers.KindMCP,
	ConfigSchema: `{"type":"object","required":["url"],"additionalProperties":false,"properties":{` +
		`"url":{"type":"string","pattern":"^https://.","maxLength":2048},` +
		`"toolPrefix":{"type":"string","pattern":"^[a-z][a-z0-9_]{0,15}$"},` +
		`"timeoutMs":{"type":"integer","minimum":1000,"maximum":30000},` +
		`"maxResultChars":{"type":"integer","minimum":256,"maximum":65536}}}`,
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
			URL            string `json:"url"`
			ToolPrefix     string `json:"toolPrefix"`
			TimeoutMs      int    `json:"timeoutMs"`
			MaxResultChars int    `json:"maxResultChars"`
		}
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, errors.New("mcp: bad config")
		}
		return NewClient(ClientConfig{
			URL: c.URL, Authorization: sec["authorization"], Timeout: time.Duration(c.TimeoutMs) * time.Millisecond,
			MaxResultChars: c.MaxResultChars, ToolPrefix: c.ToolPrefix,
		})
	},
}
