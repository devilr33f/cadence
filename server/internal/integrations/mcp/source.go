package mcp

import (
	"log/slog"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
)

// Source offers the catalog's tools to the agent, one per offered entry, as
// the catalog is when a turn begins: a turn keeps its tools while the
// catalog changes, and each call checks the catalog again.
type Source struct {
	cat *Catalog
	log *slog.Logger
}

// NewSource reads cat; log nil is slog's default. It logs one line per call:
// instance, local tool name, code, ms and the result's bytes.
func NewSource(cat *Catalog, log *slog.Logger) *Source {
	if log == nil {
		log = slog.Default()
	}
	return &Source{cat: cat, log: log}
}

// Tools are the offered entries with the owner's overrides: Write,
// Untrusted, opt-in and woken-only until marked otherwise (Rulings M5d-R1,
// R3). Only a tool marked both read and trusted leaves a turn untainted
// (Ruling M5d-R8). EventTurns is never set, so a Write tool never runs in an
// event turn, while a Read one may, as every Read tool; never taint-exempt: a
// server's text is no one's instructions. Their schemas were compiled when
// listed.
func (s *Source) Tools() []agent.Tool {
	entries, timeouts := s.cat.offering()
	out := make([]agent.Tool, 0, len(entries))
	for _, e := range entries {
		out = append(out, s.tool(e, timeouts[e.Instance]))
	}
	return out
}

func (s *Source) tool(e Entry, timeout time.Duration) agent.Tool {
	t := e.Tool
	t.Effects, t.Output = agent.Write, agent.Untrusted
	if e.Override.Effects == EffectsRead {
		t.Effects = agent.Read
	}
	// a write's result reports on what it did, in the server's words
	if e.Override.Trust == TrustTrusted && t.Effects == agent.Read {
		t.Output = agent.Trusted
	}
	t.DefaultOff = !e.Override.EnabledDefault
	t.RequiresWokenSession = t.Effects == agent.Write && !e.Override.AnyTurn
	t.EventTurns, t.TaintExempt, t.SilentOnSuccess = false, false, false
	t.Timeout = timeout
	t.Run = s.run(callee{
		instance: e.Instance, local: e.Local, remote: e.Remote, hash: e.Override.Hash, effects: t.Effects, timeout: timeout,
	})
	return t
}

// offering is every offered entry and each running server's call timeout,
// read at once.
func (c *Catalog) offering() ([]Entry, map[string]time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	timeouts := make(map[string]time.Duration, len(c.servers))
	for name, s := range c.servers {
		timeouts[name] = s.cfg.Client.Timeout()
	}
	return c.offered, timeouts
}
