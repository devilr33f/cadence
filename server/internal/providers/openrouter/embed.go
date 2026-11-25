package openrouter

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/embedx"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

type EmbedConfig struct {
	BaseURL string
	Model   string
	Timeout time.Duration
	Log     *slog.Logger
}

func NewEmbedder(c EmbedConfig, apiKey string, client *http.Client) providers.Embedder {
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	return embedx.New(embedx.Config{
		Module: "openrouter-embed", BaseURL: c.BaseURL, Model: c.Model, APIKey: apiKey, Timeout: c.Timeout, Log: c.Log,
	}, client)
}

// EmbedModule has its own id because the registry is keyed by id and
// "openrouter" is the LLM module; the host is fixed so a key only goes to OpenRouter.
var EmbedModule = providers.Module{
	ID:           "openrouter-embed",
	Kind:         providers.KindEmbed,
	ConfigSchema: `{"type":"object","required":["model"],"additionalProperties":false,"properties":{"model":{"type":"string","minLength":1}}}`,
	Secrets:      []string{"apiKey"},
	Probe:        embedx.Probe(keyPath),
	New: func(cfg json.RawMessage, sec map[string]string) (any, error) {
		var c struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(cfg, &c); err != nil || c.Model == "" {
			return nil, errors.New("openrouter-embed: model missing")
		}
		if sec["apiKey"] == "" {
			return nil, errors.New("openrouter-embed: api key missing")
		}
		return NewEmbedder(EmbedConfig{Model: c.Model}, sec["apiKey"], httpx.Client(nil)), nil
	},
}
