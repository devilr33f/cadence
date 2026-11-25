package metno

import (
	"encoding/json"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

// Module needs no secret; the hosts and identifying User-Agent are fixed.
var Module = providers.Module{
	ID:           "met-norway",
	Kind:         providers.KindWeather,
	ConfigSchema: `{"type":"object","additionalProperties":false}`,
	Probe:        probe,
	New: func(json.RawMessage, map[string]string) (any, error) {
		return New(Config{}, httpx.Client(nil)), nil
	},
}
