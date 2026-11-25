package mcp

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
)

// listed applies a complete listing of s: names, checks, the overrides. It
// holds the catalog's listing lock from reading the stored names to writing
// the new ones, so no two servers take one name.
func (c *Catalog) listed(ctx context.Context, s *server, tools []RemoteTool) {
	if !c.lockListing(ctx) {
		return
	}
	defer c.unlockListing()
	if !c.running(s) {
		return
	}
	entries := make([]Entry, len(tools))
	var remotes []string
	seenRemote := map[string]bool{}
	for i, t := range tools {
		e := Entry{Instance: s.cfg.Instance, Remote: t.Name, Annotations: t.Annotations, SchemaBytes: len(t.InputSchema)}
		switch {
		case !validRemote(t.Name):
			e.Skip = SkipName
		case seenRemote[t.Name]:
			e.Skip = SkipDuplicate
		default:
			seenRemote[t.Name] = true
			remotes = append(remotes, t.Name)
		}
		entries[i] = e
	}
	stored, taken, err := c.store.names(ctx, s.cfg.Instance)
	if err != nil {
		c.failed(s, CodeStore, true)
		return
	}
	names := assignNames(s.prefix, remotes, stored, taken)
	endpoint := s.cfg.Client.endpoint.String()
	var rows []seen
	for i := range entries {
		e, t := &entries[i], tools[i]
		if e.Skip != "" {
			continue
		}
		if e.Local = names[e.Remote]; e.Local == "" {
			e.Skip = SkipDuplicate
			continue
		}
		rows = append(rows, seen{remote: e.Remote, local: e.Local, hash: toolHash(endpoint, t)})
		e.Description = describe(s.prefix, t.Description)
		if e.Skip = checkSchema(t.InputSchema); e.Skip != "" {
			continue
		}
		input, err := schemaText(t.InputSchema)
		if err != nil {
			e.Skip = SkipSchema
			continue
		}
		tool, err := agent.Compile(agent.Tool{Name: e.Local, Description: e.Description, Input: input})
		if err != nil {
			e.Skip = SkipSchema
			continue
		}
		e.Tool = tool
	}
	overrides, err := c.store.see(ctx, s.cfg.Instance, rows)
	if err != nil {
		c.failed(s, CodeStore, true)
		return
	}
	era := s.cfg.Client.Status().Era
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.servers[s.cfg.Instance] != s {
		return
	}
	s.items, s.overrides = entries, overrides
	s.state.ListedAt, s.state.FailingSince, s.state.Withdrawn, s.state.Code = c.clock.Now(), time.Time{}, false, ""
	stopTimer(s.expiry)
	c.arm(s, RefreshEvery)
	c.up(s, era, len(tools))
	c.rebuild()
}

func (c *Catalog) running(s *server) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.servers[s.cfg.Instance] == s
}

// rebuild recomputes every entry from the servers' latest listings, held
// under c.mu. Servers go by instance name, a server's tools by local name.
// Local names are unique across servers once stored; one an earlier server
// shows still counts as a duplicate. A withdrawn server's tools are
// unavailable and a changed tool is withdrawn. Of the rest, each server
// offers 64 and the catalog 128: the owner's enabledDefault tools first, then
// by local name.
func (c *Catalog) rebuild() {
	names := slices.Sorted(maps.Keys(c.servers))
	taken := map[string]bool{}
	var all []Entry
	for _, name := range names {
		s := c.servers[name]
		items := slices.Clone(s.items)
		// tools without a local name last
		slices.SortStableFunc(items, func(a, b Entry) int {
			return cmp.Or(cmp.Compare(unnamed(a), unnamed(b)), cmp.Compare(a.Local, b.Local), cmp.Compare(a.Remote, b.Remote))
		})
		var open []int
		for _, e := range items {
			e.Override = s.overrides[e.Remote]
			switch {
			case e.Local == "":
			case taken[e.Local]:
				e.Skip = SkipDuplicate
			default:
				taken[e.Local] = true
			}
			switch {
			case e.Skip != "":
			case s.state.Withdrawn:
				e.Skip = SkipUnavailable
			case e.Override.Changed:
				e.Skip = SkipChanged
			default:
				open = append(open, len(all))
			}
			all = append(all, e)
		}
		capAt(all, open, maxPerServer)
	}
	var open []int
	for i, e := range all {
		if e.Skip == "" {
			open = append(open, i)
		}
	}
	capAt(all, open, maxOffered)
	c.entries = slices.Clip(all)
	c.offered = nil
	for _, e := range all {
		if e.Skip == "" {
			c.offered = append(c.offered, e)
		}
	}
	c.logSummaries(names)
}

// capAt skips as cap the entries of all at open past the first n, ranked
// enabledDefault first, then by local name.
func capAt(all []Entry, open []int, n int) {
	slices.SortStableFunc(open, func(a, b int) int {
		ea, eb := all[a], all[b]
		return cmp.Or(cmp.Compare(rank(ea), rank(eb)), cmp.Compare(ea.Local, eb.Local))
	})
	for _, i := range open[min(len(open), n):] {
		all[i].Skip = SkipCap
	}
}

func rank(e Entry) int {
	if e.Override.EnabledDefault {
		return 0
	}
	return 1
}

func unnamed(e Entry) int {
	if e.Local == "" {
		return 1
	}
	return 0
}

// logSummaries logs each server's counts once they differ from the last
// logged, and publishes them once they differ from the last published:
// instance names, counts and skip reasons only.
func (c *Catalog) logSummaries(names []string) {
	type tally struct {
		tools, offered, changed int
		skipped                 map[string]int
	}
	per := map[string]*tally{}
	for _, name := range names {
		per[name] = &tally{skipped: map[string]int{}}
	}
	for _, e := range c.entries {
		t := per[e.Instance]
		t.tools++
		switch e.Skip {
		case "":
			t.offered++
		case SkipChanged:
			t.changed++
		default:
			t.skipped[e.Skip]++
		}
	}
	for _, name := range names {
		s, t := c.servers[name], per[name]
		skipped := t.tools - t.offered - t.changed
		c.counted(s, LiveTools{Instance: name, Total: t.tools, Offered: t.offered, Changed: t.changed, Skipped: skipped})
		// fmt prints a map sorted by key
		sum := fmt.Sprint(t.tools, t.offered, t.changed, t.skipped)
		if sum == s.logged {
			continue
		}
		s.logged = sum
		c.log.Info("mcp_catalog", "instance", name, "tools", t.tools, "offered", t.offered, "changed", t.changed, "skipped", skipped)
		for _, r := range slices.Sorted(maps.Keys(t.skipped)) {
			c.log.Info("mcp_tools_skipped", "instance", name, "reason", r, "count", t.skipped[r])
		}
	}
}
