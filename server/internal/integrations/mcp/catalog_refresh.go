package mcp

import (
	"context"
	"errors"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// refresher re-lists on each poke: the timers', an admin's or a call's
// that met an unknown tool. Pokes that come while it lists make one more
// listing, not one each. Client.Refresh lists on a session of its own in the
// modern era, so the SDK's ttlMs listing cache never answers.
func (c *Catalog) refresher(ctx context.Context, s *server) {
	defer close(s.done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.poke:
		}
		tools, err := s.cfg.Client.Refresh(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			c.failed(s, refreshCode(err), true)
			continue
		}
		c.take(ctx, s, tools)
	}
}

// take applies a listing of s, its server's untrusted text, under a recover
// guard: a panic is a failed refresh.
func (c *Catalog) take(ctx context.Context, s *server, tools []RemoteTool) {
	if providers.Guard(c.log, Module.ID, CodeProtocol, func() { c.listed(ctx, s, tools) }) {
		c.failed(s, CodeProtocol, true)
	}
}

func refreshCode(err error) string {
	if code := Code(err); code != "" {
		return code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return CodeTimeout
	}
	return CodeUnavailable
}

func (s *server) wake() {
	select {
	case s.poke <- struct{}{}:
	default:
	}
}

func stopTimer(t Timer) {
	if t != nil {
		t.Stop()
	}
}

// Refresh asks for a re-list of a running server, false when it is not.
func (c *Catalog) Refresh(instance string) bool {
	c.mu.Lock()
	s := c.servers[instance]
	c.mu.Unlock()
	if s == nil {
		return false
	}
	s.wake()
	return true
}

// CallFailed hears of a call that failed with code: one that met an unknown
// tool asks for a re-list.
func (c *Catalog) CallFailed(instance, code string) {
	if code == CodeUnknownTool {
		c.Refresh(instance)
	}
}

// arm sets s's next re-list d from now; held under c.mu.
func (c *Catalog) arm(s *server, d time.Duration) {
	stopTimer(s.tick)
	s.tick = c.clock.AfterFunc(d, s.wake)
}

// failed records a failure of s; the first since its last good listing
// starts the KeepFor that withdraws its tools. A failed refresh is tried
// again after RetryEvery.
func (c *Catalog) failed(s *server, code string, refresh bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.servers[s.cfg.Instance] != s {
		return
	}
	if refresh {
		c.log.Warn("mcp_refresh_failed", "instance", s.cfg.Instance, "code", code)
		c.arm(s, RetryEvery)
	}
	s.state.Code = code
	c.down(s, code)
	if s.state.FailingSince.IsZero() {
		s.state.FailingSince = c.clock.Now()
		s.expiry = c.clock.AfterFunc(KeepFor, func() { c.expire(s) })
	}
}

// expire withdraws the tools of s when it has been failing for KeepFor.
func (c *Catalog) expire(s *server) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := &s.state
	if c.servers[s.cfg.Instance] != s || st.FailingSince.IsZero() || st.Withdrawn || c.clock.Now().Sub(st.FailingSince) < KeepFor {
		return
	}
	st.Withdrawn = true
	c.log.Warn("mcp_tools_withdrawn", "instance", s.cfg.Instance, "code", CodeUnavailable)
	c.rebuild()
}
