package mcp

import (
	"context"
	"log/slog"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

type SupervisorConfig struct {
	// Instance names the server in logs
	Instance string
	// Client is the supervisor's alone: nothing else may Connect or Close it
	Client *Client
	// Log nil is slog's default
	Log *slog.Logger
	// OnTools gets each complete listing, once per session, on the
	// supervisor's goroutine; it must return once ctx ends, which Stop does
	OnTools func(ctx context.Context, tools []RemoteTool)
	// OnDown gets the code each time a session ends or fails to come up, on
	// the supervisor's goroutine; it must not block
	OnDown func(code string)
}

// Supervisor keeps one session to a server on its own goroutine: connect,
// list, hold the session until a request finds it broken, reconnect with
// backoff.
type Supervisor struct {
	cfg    SupervisorConfig
	cancel context.CancelFunc
	done   chan struct{}
}

// Supervise starts at once and never waits for the server.
func Supervise(cfg SupervisorConfig) *Supervisor {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Supervisor{cfg: cfg, cancel: cancel, done: make(chan struct{})}
	go s.run(ctx)
	return s
}

// Stop closes the client, aborting its requests in flight, and waits for
// the goroutine.
func (s *Supervisor) Stop() {
	s.cancel()
	<-s.done
}

// run backs off 1 s doubling to 60 s, back to 1 s after a session that stayed
// up 60 s; a rejected credential waits 60 s. A legacy session the server
// dropped is reconnected at once, but only once until a session stays up.
func (s *Supervisor) run(ctx context.Context) {
	defer close(s.done)
	defer s.cfg.Client.Close()
	t := s.cfg.Client.t
	delay, retried := t.backoffMin, false
	for {
		up, code, lost := s.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if up >= t.stableAfter {
			delay, retried = t.backoffMin, false
		}
		s.cfg.Log.Warn("mcp_unavailable", "instance", s.cfg.Instance, "code", code)
		if s.cfg.OnDown != nil {
			providers.Guard(s.cfg.Log, Module.ID, CodeProtocol, func() { s.cfg.OnDown(code) })
		}
		wait := delay
		switch {
		case lost && !retried:
			wait, retried = 0, true
		case code == CodeAuth:
			wait, delay = t.backoffMax, t.backoffMax
		default:
			delay = min(2*delay, t.backoffMax)
		}
		s.cfg.Log.Info("mcp_reconnect", "instance", s.cfg.Instance, "delay_ms", wait.Milliseconds())
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// session runs one session to its end: how long it was up, and why it ended.
// It retires the session, so requests in flight on it finish while the next
// one connects.
func (s *Supervisor) session(ctx context.Context) (time.Duration, string, bool) {
	c := s.cfg.Client
	sess, err := c.connect(ctx)
	if err != nil {
		return 0, Code(err), false
	}
	defer c.retire(sess)
	tools, err := c.ListTools(ctx)
	if err != nil {
		return 0, Code(err), false
	}
	if !c.up(sess) {
		code, lost := ended(sess)
		return 0, code, lost
	}
	s.cfg.Log.Info("mcp_connected", "instance", s.cfg.Instance, "era", sess.era, "tools", len(tools))
	if s.cfg.OnTools != nil {
		providers.Guard(s.cfg.Log, Module.ID, CodeProtocol, func() { s.cfg.OnTools(ctx, tools) })
	}
	since := time.Now()
	select {
	case <-ctx.Done():
		return 0, "", false
	case <-sess.broken:
	}
	code, lost := ended(sess)
	return time.Since(since), code, lost
}

// ended is why s broke, read only once broken is closed: breakOn writes it
// before.
func ended(s *session) (string, bool) {
	select {
	case <-s.broken:
	default:
		return CodeUnavailable, false
	}
	if s.code == "" {
		return CodeUnavailable, false
	}
	return s.code, s.lost
}
