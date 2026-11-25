package openrouter

import (
	"encoding/json"
	"errors"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

// Module takes the key from secret apiKey; the host is fixed so a key only goes to OpenRouter.
var Module = providers.Module{
	ID:           "openrouter",
	Kind:         providers.KindLLM,
	ConfigSchema: `{"type":"object","additionalProperties":false}`,
	Secrets:      []string{"apiKey"},
	Probe:        probe,
	New: func(_ json.RawMessage, sec map[string]string) (any, error) {
		if sec["apiKey"] == "" {
			return nil, errors.New("openrouter: api key missing")
		}
		return New(Config{}, sec["apiKey"], httpx.Client(nil)), nil
	},
}
