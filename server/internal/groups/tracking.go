package groups

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

const trackingCorrectionCooldown = 60 * time.Second

type trackingMember struct {
	offsetUs, ci95Us, cycles int
	correctionID             uint32
	lastAttempt              time.Time
	lastCorrectionUs         int
	lastCorrectionAt         *time.Time
}

type trackingSnapshot struct {
	leader  string
	state   string
	members map[string]*trackingMember
	// an idle/off report between tracking reports lets a restarted leader's counters begin again
	paused bool
}

type trackingCorrection struct {
	deviceID     string
	correction   int
	correctionID uint32
}

var trackingStates = map[string]bool{
	"off": true, "idle": true, "tracking": true, "needs_calibration": true, "unsupported": true,
}

// ingestTracking keeps the elected leader's latest state and claims each correction id once. The store work runs
// after the device callback returns (devices.OnState must not block).
func (s *Service) ingestTracking(groupID, leader string, in capschema.MediaGroupTracking) []trackingCorrection {
	if !trackingStates[in.State] {
		return nil
	}
	now := s.d.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leaders[groupID] != leader {
		return nil
	}
	snap := s.tracking[groupID]
	if snap.leader != leader {
		snap = trackingSnapshot{leader: leader, members: map[string]*trackingMember{}}
	}
	if snap.members == nil {
		snap.members = map[string]*trackingMember{}
	}
	memberOf := map[string]bool{}
	for _, id := range s.members[groupID] {
		memberOf[id] = true
	}
	var corrections []trackingCorrection
	for _, m := range in.Members {
		id, ok := canonicalID(m.DeviceID)
		if !ok || !memberOf[id] || len(snap.members) >= capschema.GroupMaxMembers && snap.members[id] == nil {
			continue
		}
		tm := snap.members[id]
		if tm == nil {
			tm = &trackingMember{}
			snap.members[id] = tm
		}
		if snap.paused && m.CorrectionID <= tm.correctionID {
			// cadenced reports idle while a new audiod takes over; its counter can then legitimately restart
			tm.correctionID = 0
		}
		tm.offsetUs, tm.ci95Us, tm.cycles = m.OffsetUs, max(m.Ci95Us, 0), min(max(m.Cycles, 0), 6)
		if m.CorrectionID > tm.correctionID {
			tm.correctionID = m.CorrectionID
			if m.CorrectionUs != 0 && (tm.lastAttempt.IsZero() || now.Sub(tm.lastAttempt) >= trackingCorrectionCooldown) {
				tm.lastAttempt = now
				corrections = append(corrections, trackingCorrection{deviceID: id, correction: m.CorrectionUs, correctionID: m.CorrectionID})
			}
		}
	}
	snap.state, snap.paused = in.State, in.State != "tracking"
	s.tracking[groupID] = snap
	return corrections
}

// trackingView is the group's in-memory tracking snapshot in stable member order. Disabling the opt-in hides any stale
// leader report at once, including its microphone-listening state.
func (s *Service) trackingView(g Group) TrackingView {
	out := TrackingView{State: "off", Members: []TrackingMemberView{}}
	if !g.TrackingEnabled {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, ok := s.tracking[g.ID]
	if !ok {
		return out
	}
	out.State = snap.state
	for _, member := range g.Members {
		m := snap.members[member.DeviceID]
		if m == nil {
			continue
		}
		v := TrackingMemberView{DeviceID: member.DeviceID, OffsetUs: m.offsetUs, Ci95Us: m.ci95Us, Cycles: m.cycles,
			LastCorrectionUs: m.lastCorrectionUs}
		if m.lastCorrectionAt != nil {
			at := *m.lastCorrectionAt
			v.LastCorrectionAt = &at
		}
		out.Members = append(out.Members, v)
	}
	return out
}

func (s *Service) applyTrackingCorrections(groupID, leader string, corrections []trackingCorrection) {
	ctx, cancel := context.WithTimeout(context.Background(), recomputeTimeout)
	defer cancel()
	for _, c := range corrections {
		s.applyTrackingCorrection(ctx, groupID, leader, c)
	}
}

// applyTrackingCorrection moves the active output's stored trim; the next config push slews it live on the device.
func (s *Service) applyTrackingCorrection(ctx context.Context, groupID, leader string, c trackingCorrection) {
	g, err := s.Get(ctx, groupID)
	if err != nil {
		s.d.Log.Warn("group_tracking_correction_failed", "group_id", groupID, "device_id", c.deviceID, "code", "store")
		return
	}
	s.mu.Lock()
	current := s.leaders[groupID]
	s.mu.Unlock()
	if !g.TrackingEnabled || current != leader {
		return
	}
	st, ok := s.Status(ctx, c.deviceID)
	if !ok || st.Output == nil {
		return
	}
	var member *Member
	for i := range g.Members {
		if g.Members[i].DeviceID == c.deviceID {
			member = &g.Members[i]
			break
		}
	}
	if member == nil {
		return
	}
	var trim *OutputTrim
	for i := range member.Trims {
		if member.Trims[i].OutputID == st.Output.ID && member.Trims[i].Ultrasonic == "ok" {
			trim = &member.Trims[i]
			break
		}
	}
	if trim == nil {
		return
	}
	next := trim.TrimUs + c.correction
	if next < capschema.GroupTrimMinUs || next > capschema.GroupTrimMaxUs {
		s.d.Log.Warn("group_tracking_correction", "group_id", groupID, "device_id", c.deviceID, "output_id", trim.OutputID,
			"correction_us", c.correction, "trim_us", trim.TrimUs, "code", "out_of_range")
		return
	}
	gid, _ := parseID(groupID)
	dev, _ := parseID(c.deviceID)
	stored, err := s.q.ApplyGroupTrackingCorrection(ctx, db.ApplyGroupTrackingCorrectionParams{
		GroupID: gid, DeviceID: dev, OutputID: trim.OutputID, CorrectionUs: int32(c.correction), //nolint:gosec // bounded by the state schema
	})
	if errors.Is(err, pgx.ErrNoRows) {
		s.d.Log.Warn("group_tracking_correction", "group_id", groupID, "device_id", c.deviceID, "output_id", trim.OutputID,
			"correction_us", c.correction, "trim_us", trim.TrimUs, "code", "out_of_range")
		return
	}
	if err != nil {
		s.d.Log.Warn("group_tracking_correction_failed", "group_id", groupID, "device_id", c.deviceID, "code", "store")
		return
	}
	now := s.d.Now()
	s.mu.Lock()
	snap := s.tracking[groupID]
	if snap.leader == leader && snap.members[c.deviceID] != nil && snap.members[c.deviceID].correctionID == c.correctionID {
		m := snap.members[c.deviceID]
		m.lastCorrectionUs, m.lastCorrectionAt = c.correction, &now
	}
	s.mu.Unlock()
	s.d.Log.Info("group_tracking_correction", "group_id", groupID, "device_id", c.deviceID, "output_id", trim.OutputID,
		"correction_us", c.correction, "trim_us", int(stored), "code", "ok")
	if _, err := s.changed(ctx, groupID); err != nil {
		s.d.Log.Warn("group_tracking_correction_failed", "group_id", groupID, "device_id", c.deviceID, "code", "config")
	}
}
