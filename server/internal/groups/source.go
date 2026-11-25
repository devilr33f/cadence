package groups

import (
	"slices"
	"time"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
)

const (
	// a group's stream source that went idle keeps the lead this long: a pause or the next episode does not move it
	sourceGrace = capschema.GroupSourceGraceMs * time.Millisecond
	// the stream source moves at most once per this window, so a flapping localSource cannot bounce the group
	sourceHold = 10 * time.Second
)

// why a group's stream source moved, as group_leader_moved spells it
const (
	reasonSource    = "source"
	reasonGrace     = "grace"
	reasonPreferred = "preferred"
	reasonFlapping  = "flapping"
)

// sourceSeen is a device's localSource as the server last saw it, and when it was last raised and dropped (memory
// only: a restart forgets the times, and the device's stored state says again whether it is up)
type sourceSeen struct {
	local           bool
	raised, dropped time.Time
}

// see is s after the device reported local at now
func (s sourceSeen) see(local bool, now time.Time) sourceSeen {
	switch {
	case local && !s.local:
		s.raised = now
	case !local && s.local:
		s.dropped = now
	}
	s.local = local
	return s
}

// lead is a group's stream source as last elected: when it moved, and a move held back since (logged once)
type lead struct {
	source  string
	movedAt time.Time
	held    string
}

// choice is the stream source pickSource elects: why, the source it would have moved to were it not held back
// (flapping), and when the choice may change with nothing reported (a grace or a hold that ends; zero for never)
type choice struct {
	source, reason, want string
	wake                 time.Time
}

// pickSource elects the group's stream source (the config's leader) beside its clock master: the member that plays a
// source of its own (localSource) and raised it last; else the last stream source while it has been idle less than
// the grace; else the clock master. A move to a source away from a lead that can still lead waits until sourceHold has
// passed since the last move. The clock master never moves for a source; "" when nobody can lead.
func pickSource(g Group, facts map[string]Facts, clock string, seen map[string]sourceSeen, last lead, now time.Time) choice {
	if clock == "" {
		return choice{}
	}
	ids := memberIDs(g.Members)
	can := func(id string) bool {
		f, ok := facts[id]
		return id != "" && slices.Contains(ids, id) && eligible(f, ok)
	}
	c := choice{source: clock, reason: reasonPreferred}
	var latest time.Time
	for _, id := range ids {
		if s := seen[id]; s.local && can(id) && (c.reason != reasonSource || s.raised.After(latest)) {
			c.source, c.reason, latest = id, reasonSource, s.raised
		}
	}
	cur := last.source
	if c.reason != reasonSource && cur != clock && can(cur) {
		if dropped := seen[cur].dropped; !dropped.IsZero() && now.Sub(dropped) < sourceGrace {
			c.source, c.reason, c.wake = cur, reasonGrace, dropped.Add(sourceGrace)
		}
	}
	// only a source is held back: the clock master takes over at once when its turn comes (a member back online, a
	// grace over, a preferred leader set)
	if c.reason == reasonSource && c.source != cur && can(cur) && !last.movedAt.IsZero() && now.Sub(last.movedAt) < sourceHold {
		return choice{source: cur, reason: reasonFlapping, want: c.source, wake: last.movedAt.Add(sourceHold)}
	}
	return c
}

// electSources elects every group's stream source from what the devices last reported, keeps the leads, and logs each
// move; it answers the sources by group and the next time a choice may change with nothing reported (zero: never).
// callers hold mu
func (s *Service) electSourcesLocked(groups []keyed, facts map[string]Facts, now time.Time) (map[string]string, time.Time) {
	sources := map[string]string{}
	var wake time.Time
	leads := map[string]lead{}
	for _, g := range groups {
		last := s.leads[g.ID]
		c := pickSource(g.Group, facts, elect(g.Group, facts), s.seen, last, now)
		if !c.wake.IsZero() && (wake.IsZero() || c.wake.Before(wake)) {
			wake = c.wake
		}
		sources[g.ID] = c.source
		next := last
		switch {
		case c.reason == reasonFlapping:
			if last.held != c.want {
				s.d.Log.Info("group_leader_held", "group_id", g.ID, "from", last.source, "to", c.want, "reason", reasonFlapping)
			}
			next.held = c.want
		case c.source != last.source:
			next = lead{source: c.source}
			// a first election (a new group, a restart, a group that came back online) is no move
			if last.source != "" && c.source != "" {
				s.d.Log.Info("group_leader_moved", "group_id", g.ID, "from", last.source, "to", c.source, "reason", c.reason)
				next.movedAt = now
			}
		default:
			next.held = ""
		}
		leads[g.ID] = next
	}
	s.leads = leads
	return sources, wake
}

// wakeAt has the service recompute at t, when a grace or a hold ends; zero cancels. callers hold mu
func (s *Service) wakeAtLocked(t, now time.Time) {
	if t.IsZero() {
		if s.wake != nil {
			s.wake.Stop()
		}
		return
	}
	// a little past the instant, so the recompute sees it passed
	d := t.Sub(now) + 10*time.Millisecond
	if s.wake == nil {
		s.wake = time.AfterFunc(d, s.Poke)
		return
	}
	s.wake.Reset(d)
}
