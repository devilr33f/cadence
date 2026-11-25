package deepgram

import (
	"encoding/json"
	"errors"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

var errNoKey = errors.New("deepgram: api key missing")

// FluxModule and AuraModule take the key from secret apiKey; the host is fixed so a key only goes to Deepgram.
var FluxModule = providers.Module{
	ID:   "deepgram-flux",
	Kind: providers.KindSTT,
	ConfigSchema: `{"type":"object","additionalProperties":false,"properties":{` +
		`"model":{"type":"string","minLength":1},` +
		`"eotThreshold":{"type":"number","exclusiveMinimum":0,"maximum":1}}}`,
	Secrets: []string{"apiKey"},
	Probe:   probe,
	New: func(cfg json.RawMessage, sec map[string]string) (any, error) {
		var c struct {
			Model        string  `json:"model"`
			EOTThreshold float64 `json:"eotThreshold"`
		}
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, err
		}
		if sec["apiKey"] == "" {
			return nil, errNoKey
		}
		return NewFlux(FluxConfig{Model: c.Model, EOTThreshold: c.EOTThreshold}, sec["apiKey"], httpx.Client(nil)), nil
	},
}

var AuraModule = providers.Module{
	ID:           "deepgram-aura",
	Kind:         providers.KindTTS,
	ConfigSchema: `{"type":"object","additionalProperties":false}`,
	Secrets:      []string{"apiKey"},
	Probe:        probe,
	New: func(_ json.RawMessage, sec map[string]string) (any, error) {
		if sec["apiKey"] == "" {
			return nil, errNoKey
		}
		return NewAura(AuraConfig{}, sec["apiKey"], httpx.Client(nil)), nil
	},
}
