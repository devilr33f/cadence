package openmeteo

import (
	"encoding/json"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

// Module needs no secret; the hosts are fixed to the public Open-Meteo endpoints.
var Module = providers.Module{
	ID:           "open-meteo",
	Kind:         providers.KindWeather,
	ConfigSchema: `{"type":"object","additionalProperties":false}`,
	Probe:        probe,
	New: func(json.RawMessage, map[string]string) (any, error) {
		return New(Config{}, httpx.Client(nil)), nil
	},
}
