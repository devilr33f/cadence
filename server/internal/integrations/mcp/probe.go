package mcp

import (
	"context"
	"net/http"
	"sync/atomic"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// probe connects a session of its own and lists the tools once, then closes
// it: no tool is called, and a supervised client's session is left alone.
// The client's codes fold a 5xx, a 429 and no answer into mcp_unavailable,
// so the last status the server answered with tells them apart.
func probe(ctx context.Context, v any) error {
	c, ok := v.(*Client)
	if !ok {
		return providers.ProbeBadResponse
	}
	seen := &answered{base: c.base}
	defer seen.closeIdle()
	pc := c.through(seen)
	defer pc.Close()
	err := pc.Connect(ctx)
	if err == nil {
		_, err = pc.ListTools(ctx)
	}
	switch Code(err) {
	case "":
		return err
	case CodeAuth:
		return providers.ProbeAuth
	case CodeTimeout:
		return providers.ProbeTimeout
	case CodeUnavailable:
		switch seen.status.Load() {
		case 0:
			return providers.ProbeUnreachable
		case http.StatusTooManyRequests:
			return providers.ProbeRateLimited
		}
	}
	return providers.ProbeBadResponse
}

// answered keeps the last status the server answered with.
type answered struct {
	base   http.RoundTripper
	status atomic.Int64
}

func (a *answered) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := a.base.RoundTrip(r)
	if err == nil {
		a.status.Store(int64(resp.StatusCode))
	}
	return resp, err
}

// closeIdle drops the test's idle connections: its build keeps none.
func (a *answered) closeIdle() {
	if t, ok := a.base.(interface{ CloseIdleConnections() }); ok {
		t.CloseIdleConnections()
	}
}
