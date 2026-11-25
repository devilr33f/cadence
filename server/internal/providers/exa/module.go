package exa

import (
	"context"
	"encoding/json"
	"errors"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

// Module takes the key from secret apiKey; the host is fixed so a key only goes to Exa.
var Module = providers.Module{
	ID:           "exa",
	Kind:         providers.KindSearch,
	ConfigSchema: `{"type":"object","additionalProperties":false}`,
	Secrets:      []string{"apiKey"},
	// exa has no free authenticated call: search and contents are billed per
	// request, and its listing routes (websets, research, monitors) belong to
	// products a search key may not have, so a test answers unsupported and
	// never spends a search
	Probe: func(context.Context, any) error { return providers.ProbeUnsupported },
	New: func(_ json.RawMessage, sec map[string]string) (any, error) {
		if sec["apiKey"] == "" {
			return nil, errors.New("exa: api key missing")
		}
		return New(Config{}, sec["apiKey"], httpx.Client(nil)), nil
	},
}
