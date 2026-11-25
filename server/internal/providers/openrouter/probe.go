package openrouter

import (
	"context"
	"net/http"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// keyPath is the key's own record (GET /key): free, and it refuses a wrong
// key, which the model list (answered without one) never does
const keyPath = "/key"

func probe(ctx context.Context, v any) error {
	l, ok := v.(*llm)
	if !ok {
		return providers.ProbeBadResponse
	}
	_, err := providers.ProbeGET(ctx, l.client, l.base+keyPath, http.Header{"Authorization": {"Bearer " + l.key}})
	return err
}
