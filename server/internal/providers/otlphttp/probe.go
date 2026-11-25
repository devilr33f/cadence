package otlphttp

import (
	"context"
	"errors"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// probe exports an empty logs request: OTLP says a collector SHOULD take a
// request with no data with success, and with none nothing is stored or
// billed, while 401 and 403 still tell a refused authorization
func probe(ctx context.Context, v any) error {
	c, ok := v.(*Client)
	if !ok {
		return providers.ProbeBadResponse
	}
	// the build is the test's own: its connection is not kept
	defer c.http.CloseIdleConnections()
	err := c.Post(ctx, "logs", nil)
	var e *Error
	switch {
	case err == nil:
		return nil
	case !errors.As(err, &e):
		return err
	}
	switch e.code {
	case "status":
		return providers.ProbeStatus(e.status)
	case "timeout":
		return providers.ProbeTimeout
	case "redirect":
		return providers.ProbeBadResponse
	}
	return providers.ProbeUnreachable
}
