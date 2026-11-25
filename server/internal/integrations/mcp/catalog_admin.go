package mcp

import "context"

// Health is a running server's state and its client's status, false when it
// is not running.
func (c *Catalog) Health(instance string) (State, Status, bool) {
	c.mu.Lock()
	s := c.servers[instance]
	var st State
	if s != nil {
		st = s.state
	}
	c.mu.Unlock()
	if s == nil {
		return State{}, Status{}, false
	}
	return st, s.cfg.Client.Status(), true
}

// Acknowledge acknowledges a running server's tools at the hashes given,
// remote name to hash, or with all every changed tool at its latest hash,
// each or none, and applies them to the catalog at once. ErrToolNotFound
// names a remote the server does not list under a local name, ErrStaleAck a
// hash that is not the latest one listed. Like Override it waits for a
// listing under way, until ctx ends.
func (c *Catalog) Acknowledge(ctx context.Context, instance string, hashes map[string]string, all bool) error {
	if !c.lockListing(ctx) {
		return ctx.Err()
	}
	defer c.unlockListing()
	c.mu.Lock()
	s := c.servers[instance]
	listed := map[string]Override{}
	if s != nil {
		for _, e := range s.items {
			if e.Local != "" {
				listed[e.Remote] = s.overrides[e.Remote]
			}
		}
	}
	c.mu.Unlock()
	if all {
		hashes = map[string]string{}
		for remote, o := range listed {
			if o.Changed {
				hashes[remote] = o.Hash
			}
		}
	}
	for remote := range hashes {
		if _, ok := listed[remote]; !ok {
			return ErrToolNotFound
		}
	}
	if len(hashes) == 0 {
		return nil
	}
	got, err := c.store.ack(ctx, instance, hashes)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if s != nil && c.servers[instance] == s {
		for remote, o := range got {
			s.overrides[remote] = o
		}
		c.rebuild()
	}
	return nil
}
