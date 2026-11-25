package groups

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// at most this many lan addresses are kept per device, as the hello carries
const maxAddresses = 4

// Linked keeps the device's lan addresses from its link's hello and recomputes: it runs under the gateway's lifecycle
// lock, so the recompute runs later, on the service's own goroutine.
func (s *Service) Linked(deviceID string, addresses []string) {
	if id, ok := canonicalID(deviceID); ok {
		s.mu.Lock()
		s.addrs[id] = slices.Clone(addresses[:min(len(addresses), maxAddresses)])
		s.mu.Unlock()
	}
	s.Poke()
}

// Closed forgets the device's addresses as its link ends, and recomputes.
func (s *Service) Closed(deviceID string) {
	if id, ok := canonicalID(deviceID); ok {
		s.mu.Lock()
		delete(s.addrs, id)
		for groupID, snap := range s.tracking {
			if snap.leader == id {
				delete(s.tracking, groupID)
			}
		}
		s.mu.Unlock()
	}
	s.Poke()
}

// Poke asks for a recompute on the service's goroutine; it never blocks.
func (s *Service) Poke() {
	select {
	case s.poke <- struct{}{}:
	default:
	}
}

// Run recomputes once, then on every poke, until ctx ends or stop is called.
func (s *Service) Run(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.Poke()
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.poke:
				s.Recompute(ctx)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// Recompute computes every capable device's media.group config from the stored groups and what is known of the
// devices now, and ensures it with the group's key: an unchanged one adds no generation. A group whose elected
// leader changed is told as group_changed.
func (s *Service) Recompute(ctx context.Context) {
	s.recomputing.Lock()
	defer s.recomputing.Unlock()
	ctx, cancel := context.WithTimeout(ctx, recomputeTimeout)
	defer cancel()
	failed := func(reason string) { s.d.Log.Warn("group_recompute_failed", "reason", reason) }
	groups, sealed, err := s.list(ctx)
	if err != nil {
		failed("store")
		return
	}
	list, err := s.d.List(ctx)
	if err != nil {
		failed("devices")
		return
	}
	var ks []keyed
	for _, g := range groups {
		if s.lostDeleted(g, list) {
			// a deleted device held the key: the group takes a new one, as for any membership change
			next, err := s.newKey(g.ID)
			if err == nil {
				_, err = s.q.SetGroupKey(ctx, db.SetGroupKeyParams{ID: uuidOrNull(g.ID), KeySealed: next})
			}
			if err != nil {
				failed("store")
				return
			}
			sealed[g.ID] = next
			s.log("group_key_changed", g.ID, len(g.Members))
		}
		key, err := s.open(g.ID, sealed[g.ID])
		if err != nil {
			s.d.Log.Error("group_key_failed", "group_id", g.ID)
			continue
		}
		ks = append(ks, keyed{Group: g, Key: key})
	}
	facts := map[string]Facts{}
	s.mu.Lock()
	addrs := map[string][]string{}
	for k, v := range s.addrs {
		addrs[k] = v
	}
	s.mu.Unlock()
	unknown := map[string]bool{}
	local := map[string]bool{}
	for _, d := range list {
		var output *capschema.MediaGroupOutput
		if raw, ok := s.d.Devices.State(ctx, d.ID, capschema.MediaGroupID); ok {
			var st capschema.MediaGroupStatus
			if json.Unmarshal(raw, &st) == nil {
				local[d.ID], output = st.LocalSource, st.Output
			}
		}
		_, err := s.d.Devices.Desired(ctx, d.ID, capschema.MediaGroupID)
		if err != nil && !errors.Is(err, devices.ErrUnknownCapability) {
			// unread: neither changed nor counted as able to lead
			unknown[d.ID] = true
		}
		online := s.d.Online(d.ID)
		f := Facts{Model: d.Model, Room: d.Room, Online: online, Capable: err == nil, Output: output}
		if online {
			f.Addresses = addrs[d.ID]
		}
		facts[d.ID] = f
	}
	now := s.d.Now()
	s.mu.Lock()
	seen := make(map[string]sourceSeen, len(list))
	for _, d := range list {
		seen[d.ID] = s.seen[d.ID].see(local[d.ID], now)
	}
	s.seen = seen
	sources, wake := s.electSourcesLocked(ks, facts, now)
	s.wakeAtLocked(wake, now)
	s.mu.Unlock()
	plan := compute(ks, facts, sources)
	for _, d := range list {
		want, ok := plan.Devices[d.ID]
		if !ok || unknown[d.ID] {
			continue
		}
		cfg, err := json.Marshal(want.Config)
		if err != nil {
			continue
		}
		changed, err := s.d.Devices.Ensure(ctx, d.ID, capschema.MediaGroupID, cfg, capschema.MediaGroupKeySecret, want.Key)
		var invalid *devices.ConfigInvalidError
		switch {
		case errors.Is(err, devices.ErrUnknownCapability):
		case errors.As(err, &invalid):
			s.d.Log.Warn("group_config_failed", "device_id", d.ID, "reason", "invalid")
		case err != nil:
			s.d.Log.Warn("group_config_failed", "device_id", d.ID, "reason", "store")
		case changed:
			s.d.Log.Info("group_config_set", "device_id", d.ID, "group_id", want.Config.GroupID, "role", want.Config.Role,
				"enabled", want.Config.Enabled)
		}
	}
	s.remember(groups, plan)
}

// lostDeleted says a member the last recompute saw in the group is gone with its device
func (s *Service) lostDeleted(g Group, list []Device) bool {
	s.mu.Lock()
	prev := s.members[g.ID]
	s.mu.Unlock()
	now := memberIDs(g.Members)
	for _, id := range prev {
		if !slices.Contains(now, id) && !slices.ContainsFunc(list, func(d Device) bool { return d.ID == id }) {
			return true
		}
	}
	return false
}

func (s *Service) open(groupID string, sealed []byte) ([]byte, error) {
	if s.d.Box == nil {
		return nil, errors.New("groups: no secrets box")
	}
	return s.d.Box.Open(sealed, keyAAD(groupID))
}

// remember keeps the plan's leaders, clock masters and memberships for routing, and tells the groups whose leader or
// clock master changed
func (s *Service) remember(groups []Group, plan Plan) {
	memberOf := map[string]string{}
	members := map[string][]string{}
	follow := map[string]bool{}
	for _, g := range groups {
		ids := memberIDs(g.Members)
		members[g.ID] = ids
		follow[g.ID] = g.FollowVideo
		for _, id := range ids {
			memberOf[id] = g.ID
		}
	}
	s.mu.Lock()
	var moved []Group
	for _, g := range groups {
		if snap, ok := s.tracking[g.ID]; ok && (!g.TrackingEnabled || snap.leader != plan.Leaders[g.ID]) {
			delete(s.tracking, g.ID)
		}
		if was, ok := s.leaders[g.ID]; ok && (was != plan.Leaders[g.ID] || s.clocks[g.ID] != plan.Clocks[g.ID]) {
			moved = append(moved, g)
		}
	}
	s.memberOf, s.members, s.leaders, s.clocks, s.active, s.follow = memberOf, members, plan.Leaders, plan.Clocks, plan.Active, follow
	s.mu.Unlock()
	for _, g := range moved {
		s.publishGroup(g)
	}
}

// VideoGroup is a group whose screens follow a video (followVideo, multiroom P4) as the last recompute left it.
type VideoGroup struct {
	ID string
	// Leader is the stream source, "" for none
	Leader  string
	Members []string
	// Active: the leader streams to at least one online member
	Active bool
}

// VideoGroups answers every group with followVideo on, in no order.
func (s *Service) VideoGroups() []VideoGroup {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []VideoGroup
	for id, on := range s.follow {
		if on {
			out = append(out, VideoGroup{ID: id, Leader: s.leaders[id], Members: slices.Clone(s.members[id]), Active: s.active[id]})
		}
	}
	return out
}

// Leader is the group's elected leader as last computed, "" for none.
func (s *Service) Leader(groupID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leaders[groupID]
}

// Clock is the group's elected clock master as last computed, "" for none.
func (s *Service) Clock(groupID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clocks[groupID]
}

// Route answers the leader a media command for deviceID goes to: a member of an active group plays what its leader
// plays. ok is false for a leader, a device in no group or one whose group streams to nobody.
func (s *Service) Route(deviceID, capability string) (string, bool) {
	if capability != "media.url" && capability != "media.player" {
		return "", false
	}
	id, ok := canonicalID(deviceID)
	if !ok {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.memberOf[id]
	if !ok || !s.active[g] {
		return "", false
	}
	leader := s.leaders[g]
	return leader, leader != "" && leader != id
}

// Followers answers the other members of the active group deviceID streams to as its leader, nil when it leads none:
// the devices whose session is the leader's (a shared cover, multiroom).
func (s *Service) Followers(deviceID string) []string {
	id, ok := canonicalID(deviceID)
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.memberOf[id]
	if !ok || !s.active[g] || s.leaders[g] != id {
		return nil
	}
	var out []string
	for _, m := range s.members[g] {
		if m != id {
			out = append(out, m)
		}
	}
	return out
}

// Send is a capability command's send (commands.Service.Send).
type Send func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error)

// Intercept is a commands interceptor that sends a group member's media commands to its leader, through send (the
// whole chain again, now for the leader).
func (s *Service) Intercept(send Send) func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, bool, error) {
	return func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, bool, error) {
		leader, ok := s.Route(deviceID, capability)
		if !ok {
			return nil, false, nil
		}
		res, err := send(ctx, leader, capability, name, args)
		return res, true, err
	}
}

// Named is each group's name and elected leader, for the agent's target resolver.
func (s *Service) Named(ctx context.Context) ([]NamedGroup, error) {
	rows, err := s.q.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]NamedGroup, 0, len(rows))
	for _, r := range rows {
		out = append(out, NamedGroup{Name: r.Name, LeaderID: s.Leader(r.ID.String())})
	}
	return out, nil
}

// NamedGroup is a group as the agent's target resolver reads it.
type NamedGroup struct{ Name, LeaderID string }

// Refs answers each grouped device's group as the device list and detail show it.
func (s *Service) Refs(ctx context.Context) (map[string]devices.GroupRef, error) {
	groups, _, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]devices.GroupRef{}
	for _, g := range groups {
		ref := devices.GroupRef{ID: g.ID, Name: g.Name, Kind: g.Kind, LeaderID: s.Leader(g.ID), Members: memberIDs(g.Members)}
		for _, id := range ref.Members {
			out[id] = ref
		}
	}
	return out, nil
}

// StateChanged publishes a member's changed media.group state as group_state (devices.Service.OnState), and recomputes
// when its localSource changed: the group's stream source may move.
func (s *Service) StateChanged(deviceID, capability string, state json.RawMessage) {
	if capability != capschema.MediaGroupID {
		return
	}
	var st capschema.MediaGroupStatus
	if json.Unmarshal(state, &st) != nil {
		return
	}
	s.mu.Lock()
	g, ok := s.memberOf[deviceID]
	was := s.seen[deviceID]
	if ok {
		s.seen[deviceID] = was.see(st.LocalSource, s.d.Now())
	}
	s.mu.Unlock()
	if ok && was.local != st.LocalSource {
		s.Poke()
	}
	var corrections []trackingCorrection
	if ok && st.GroupID == g && st.Role == capschema.GroupRoleLeader {
		corrections = s.ingestTracking(g, deviceID, st.Tracking)
	}
	if ok && s.d.Bus != nil {
		s.d.Bus.Publish(events.Event{Type: events.GroupState, DeviceID: deviceID, At: time.Now(),
			Data: events.GroupStateData{GroupID: g, Status: st}})
	}
	if len(corrections) > 0 {
		go s.applyTrackingCorrections(g, deviceID, corrections)
	}
}

// Status is the device's media.group state as it last reported it.
func (s *Service) Status(ctx context.Context, deviceID string) (*capschema.MediaGroupStatus, bool) {
	raw, ok := s.d.Devices.State(ctx, deviceID, capschema.MediaGroupID)
	if !ok {
		return nil, false
	}
	var st capschema.MediaGroupStatus
	if json.Unmarshal(raw, &st) != nil {
		return nil, false
	}
	return &st, true
}

// Online says whether the device is linked now.
func (s *Service) Online(deviceID string) bool { return s.d.Online(deviceID) }
