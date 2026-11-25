package hermes

import (
	"context"
	"net/http"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// probe reads the capabilities the kind's own Probe reads (GET
// v1/capabilities, which hermes-agent answers only with the bearer key):
// no run is made, so the agent does nothing. A server without what the kind
// needs answers bad_response.
func probe(ctx context.Context, v any) error {
	c, ok := v.(*Client)
	if !ok {
		return providers.ProbeBadResponse
	}
	b, err := providers.ProbeGET(ctx, c.http, c.base+"v1/capabilities", http.Header{"Authorization": {"Bearer " + c.key}})
	if err != nil {
		return err
	}
	if !capabilitiesOK(b) {
		return providers.ProbeBadResponse
	}
	return nil
}
