package group

import (
	"math"
	"regexp"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

const (
	stateOff        = "off"
	stateConnecting = "connecting"
)

// audiod's states as media.group's state spells them; an unknown one is connecting
var stateNames = map[audiodv1.GroupState]string{
	audiodv1.GroupState_GROUP_STATE_OFF:        stateOff,
	audiodv1.GroupState_GROUP_STATE_IDLE:       "idle",
	audiodv1.GroupState_GROUP_STATE_CONNECTING: stateConnecting,
	audiodv1.GroupState_GROUP_STATE_SYNCING:    "syncing",
	audiodv1.GroupState_GROUP_STATE_BUFFERING:  "buffering",
	audiodv1.GroupState_GROUP_STATE_PLAYING:    "playing",
	audiodv1.GroupState_GROUP_STATE_REJOINING:  "rejoining",
	audiodv1.GroupState_GROUP_STATE_FAILED:     "failed",
}

// an error audiod reports is kept only as a token
var errorToken = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

func stateName(s audiodv1.GroupState) string {
	if n, ok := stateNames[s]; ok {
		return n
	}
	return stateConnecting
}

func roleName(r audiodv1.GroupRole) string {
	switch r {
	case audiodv1.GroupRole_GROUP_ROLE_LEADER:
		return capschema.GroupRoleLeader
	case audiodv1.GroupRole_GROUP_ROLE_MEMBER:
		return capschema.GroupRoleMember
	default:
		return ""
	}
}

// fromStatus is audiod's GroupStatus as media.group's state: numbers and tokens alone, within the schema's bounds
func fromStatus(st *audiodv1.GroupStatus) capschema.MediaGroupStatus {
	out := capschema.MediaGroupStatus{
		GroupID: st.GetGroupId(), Role: roleName(st.GetRole()), State: stateName(st.GetState()), Synced: st.GetSynced(),
		ClockErrorUs: unsigned(st.GetClockErrorUs()), QueueMs: unsigned(st.GetQueueMs()), SteerPpm: int(st.GetSteerPpm()),
		Rejoins: unsigned(st.GetRejoins()), Underruns: unsigned(st.GetUnderruns()),
		PlacementErrorUs: int(st.GetPlacementErrorUs()), LatencyMs: unsigned(st.GetLatencyMs()),
		BitrateKbps: unsigned(st.GetBitrateKbps()), WaitMs: unsigned(st.GetWaitMs()),
	}
	if errorToken.MatchString(st.GetError()) {
		out.Error = st.GetError()
	}
	for _, m := range st.GetMembers() {
		if len(out.Members) == capschema.GroupMaxMembers {
			break
		}
		if id := m.GetMemberId(); id == "" || len(id) > 64 {
			continue
		}
		out.Members = append(out.Members, capschema.MediaGroupMemberStatus{
			DeviceID: m.GetMemberId(), State: stateName(m.GetState()), Connected: m.GetConnected(), Synced: m.GetSynced(),
			ClockErrorUs: unsigned(m.GetClockErrorUs()), QueueMs: unsigned(m.GetQueueMs()), Rejoins: unsigned(m.GetRejoins()),
			PlacementErrorUs: int(m.GetPlacementErrorUs()),
		})
	}
	return out
}

// trackingState is GroupTracked as media.group's public tracking object; off and idle are local until the leader's
// first report arrives. callers hold the group lock
func trackingState(st *audiodv1.GroupTracked, cfg capschema.MediaGroup) capschema.MediaGroupTracking {
	out := capschema.MediaGroupTracking{State: stateOff, Members: []capschema.MediaGroupTrackingMember{}}
	if !cfg.Enabled || cfg.Tracking == nil || !cfg.Tracking.Enabled {
		return out
	}
	out.State = "idle"
	if st == nil || st.GetGroupId() != cfg.GroupID {
		return out
	}
	switch st.GetState() {
	case "off", "idle", "tracking", "needs_calibration", "unsupported":
		out.State = st.GetState()
	}
	for _, m := range st.GetMembers() {
		if len(out.Members) == capschema.GroupMaxMembers {
			break
		}
		if id := m.GetMemberId(); id != "" && len(id) <= 64 {
			correction := int(m.GetCorrectionUs())
			correction = max(-5000, min(correction, 5000))
			out.Members = append(out.Members, capschema.MediaGroupTrackingMember{
				DeviceID: id, OffsetUs: int(m.GetOffsetUs()), Ci95Us: unsigned(m.GetCi95Us()),
				Cycles: min(unsigned(m.GetCycles()), 6), CorrectionUs: correction, CorrectionID: m.GetCorrectionId(),
			})
		}
	}
	return out
}

// unsigned keeps a uint32 within an int on a 32-bit build (midi's armv7)
func unsigned(v uint32) int { return int(min(uint64(v), math.MaxInt32)) }
