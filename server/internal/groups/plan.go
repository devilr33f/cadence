package groups

import (
	"encoding/hex"
	"slices"

	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
)

// screen boards lead only when no speaker can: a speaker's audiod is the one the group is tuned on
var screenBoards = []string{"android-tv", "rpi", "sim-tv"}

// Facts are what the plan knows of a device now.
type Facts struct {
	Model, Room string
	Online      bool
	// Capable: the device reports media.group
	Capable bool
	// Addresses are its lan addresses from its link's hello, while it is linked
	Addresses []string
	// Output is the active output in its last media.group state
	Output *capschema.MediaGroupOutput
}

// keyed is a group with its key, opened
type keyed struct {
	Group
	Key []byte
}

// Desired is one device's media.group config and the key it holds ("" none).
type Desired struct {
	Config capschema.MediaGroup
	Key    string
}

// Plan is every capable device's desired media.group config, and each group's elected stream source (its leader, "" for
// none) and clock master.
type Plan struct {
	Devices map[string]Desired
	Leaders map[string]string
	Clocks  map[string]string
	// Active are the groups whose leader has an online member to stream to
	Active map[string]bool
}

var disabled = Desired{Config: capschema.MediaGroup{Enabled: false}}

// eligible: the device can lead now
func eligible(f Facts, ok bool) bool { return ok && f.Online && f.Capable && len(f.Addresses) > 0 }

// elect picks the group's clock master, which leads unless another member plays a source (pickSource): the preferred one while it can lead, else the first member that can by position,
// speakers before screens. A leader needs a lan address too: its members dial it.
func elect(g Group, facts map[string]Facts) string {
	if g.PreferredLeader != "" && slices.Contains(memberIDs(g.Members), g.PreferredLeader) {
		if f, ok := facts[g.PreferredLeader]; eligible(f, ok) {
			return g.PreferredLeader
		}
	}
	for _, screens := range []bool{false, true} {
		for _, m := range g.Members {
			f, ok := facts[m.DeviceID]
			if eligible(f, ok) && slices.Contains(screenBoards, f.Model) == screens {
				return m.DeviceID
			}
		}
	}
	return ""
}

// budget is the clock budget (us) of the group's kind and rooms
func budget(g Group, facts map[string]Facts) int {
	if g.Kind == KindStereoPair {
		return capschema.GroupBudgetPairUs
	}
	rooms := make([]string, 0, len(g.Members))
	for _, m := range g.Members {
		rooms = append(rooms, facts[m.DeviceID].Room)
	}
	if sameRoom(rooms) {
		return capschema.GroupBudgetRoomUs
	}
	return capschema.GroupBudgetHouseUs
}

// configTrims strips the server's calibration metadata from media.group's device config.
func configTrims(trims []OutputTrim) []capschema.MediaGroupTrim {
	if len(trims) == 0 {
		return nil
	}
	out := make([]capschema.MediaGroupTrim, 0, len(trims))
	for _, t := range trims {
		out = append(out, capschema.MediaGroupTrim{OutputID: t.OutputID, TrimUs: t.TrimUs})
	}
	return out
}

// trackingMembers are the active outputs whose last applied calibration proved that ultrasound reaches a microphone.
func trackingMembers(g Group, facts map[string]Facts) []string {
	var out []string
	for _, m := range g.Members {
		output := facts[m.DeviceID].Output
		if output == nil {
			continue
		}
		for _, t := range m.Trims {
			if t.OutputID == output.ID && t.Ultrasonic == "ok" {
				out = append(out, m.DeviceID)
				break
			}
		}
	}
	return out
}

// compute is every capable device's desired media.group config: a group's leader (its stream source, sources[id], else
// its clock master) streams to its online members, each member dials the leader's first lan address. When the leader
// is not the clock master, the leader and every other member dial the master's first address for the clock (clock),
// and the master serves it as a member (clockMaster). A member whose group elected no leader, a leader with no online
// member and a device in no group are disabled. Offline members keep their member config, so a link that drops and
// comes back adds no generation.
func compute(groups []keyed, facts map[string]Facts, sources map[string]string) Plan {
	p := Plan{Devices: map[string]Desired{}, Leaders: map[string]string{}, Clocks: map[string]string{}, Active: map[string]bool{}}
	for id, f := range facts {
		if f.Capable {
			p.Devices[id] = disabled
		}
	}
	for _, g := range groups {
		clock := elect(g.Group, facts)
		leader := clock
		if src := sources[g.ID]; src != "" && slices.Contains(memberIDs(g.Members), src) {
			if f, ok := facts[src]; eligible(f, ok) {
				leader = src
			}
		}
		p.Leaders[g.ID], p.Clocks[g.ID] = leader, clock
		if leader == "" {
			continue
		}
		var online []string
		for _, m := range g.Members {
			if f, ok := facts[m.DeviceID]; ok && m.DeviceID != leader && f.Online && f.Capable {
				online = append(online, m.DeviceID)
			}
		}
		if len(online) == 0 {
			continue
		}
		p.Active[g.ID] = true
		key := hex.EncodeToString(g.Key)
		us := budget(g.Group, facts)
		tracked := trackingMembers(g.Group, facts)
		trackingOn := g.TrackingEnabled && len(tracked) >= minMembers
		base := func(m Member) capschema.MediaGroup {
			return capschema.MediaGroup{
				Enabled: true, GroupID: g.ID, Channel: m.Channel, LatencyMs: g.LatencyMs, TrimMs: m.TrimMs, TrimUs: m.TrimUs,
				Trims: configTrims(m.Trims), Tracking: &capschema.MediaGroupTrackingConfig{Enabled: trackingOn},
				BudgetUs: us, BitrateKbps: g.BitrateKbps, PCM: g.PCM, WaitMs: g.WaitMs,
				// pcm has no bitrate to tune
				AutoLatency: g.AutoLatency, AutoBitrate: g.AutoBitrate && !g.PCM, AutoWait: g.AutoWait,
			}
		}
		host := facts[leader].Addresses[0]
		var clockAt *capschema.MediaGroupLeader
		if clock != leader {
			clockAt = &capschema.MediaGroupLeader{Host: facts[clock].Addresses[0], Port: capschema.GroupDefaultPort}
		}
		for _, m := range g.Members {
			if f, ok := facts[m.DeviceID]; !ok || !f.Capable {
				continue
			}
			cfg := base(m)
			switch m.DeviceID {
			case leader:
				cfg.Role, cfg.Port, cfg.Members, cfg.Clock = capschema.GroupRoleLeader, capschema.GroupDefaultPort, online, clockAt
				if trackingOn {
					cfg.Tracking.Members = slices.Clone(tracked)
				}
			case clock:
				cfg.Role, cfg.ClockMaster = capschema.GroupRoleMember, true
				cfg.Leader = &capschema.MediaGroupLeader{Host: host, Port: capschema.GroupDefaultPort}
			default:
				cfg.Role, cfg.Clock = capschema.GroupRoleMember, clockAt
				cfg.Leader = &capschema.MediaGroupLeader{Host: host, Port: capschema.GroupDefaultPort}
			}
			p.Devices[m.DeviceID] = Desired{Config: cfg, Key: key}
		}
	}
	return p
}

// canonicalID is a uuid's canonical spelling, which the hub and the store key devices by
func canonicalID(s string) (string, bool) {
	var id pgtype.UUID
	if id.Scan(s) != nil {
		return "", false
	}
	return id.String(), true
}
