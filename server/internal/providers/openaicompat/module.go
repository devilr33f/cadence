// Package openaicompat holds provider modules for servers that speak the
// OpenAI API at an operator-chosen https base URL.
package openaicompat

import (
	"encoding/json"
	"errors"
	"net/url"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/embedx"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

// EmbedModule posts to {baseUrl}/embeddings; the key is optional because
// self-hosted servers often take none. TLS is always verified.
var EmbedModule = providers.Module{
	ID:   "openai-compatible",
	Kind: providers.KindEmbed,
	ConfigSchema: `{"type":"object","required":["baseUrl","model"],"additionalProperties":false,"properties":{` +
		`"baseUrl":{"type":"string","pattern":"^https://."},"model":{"type":"string","minLength":1}}}`,
	OptionalSecrets: []string{"apiKey"},
	// the server's model list (GET {baseUrl}/models): it embeds nothing, so nothing is billed
	Probe: embedx.Probe("/models"),
	New: func(cfg json.RawMessage, sec map[string]string) (any, error) {
		var c struct {
			BaseURL string `json:"baseUrl"`
			Model   string `json:"model"`
		}
		if err := json.Unmarshal(cfg, &c); err != nil || c.Model == "" {
			return nil, errors.New("openai-compatible: bad config")
		}
		u, err := url.Parse(c.BaseURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("openai-compatible: bad base url")
		}
		return embedx.New(embedx.Config{
			Module: "openai-compatible", BaseURL: c.BaseURL, Model: c.Model, APIKey: sec["apiKey"],
		}, httpx.Client(nil)), nil
	},
}
