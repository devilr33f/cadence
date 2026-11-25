package groups

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
)

// a listener calibration is applied within this of its scheduling, once: the browser records through every round
// (four rounds of eight members take about 40 s), then posts what it heard
const listenerTTL = 120 * time.Second

// listenerResultWait is how long an apply waits for the speakers' own measure of the run: past it, the apply goes on
// without it
const listenerResultWait = 10 * time.Second

// ErrExpired: no listener calibration has the id now (never made, applied already, or past listenerTTL)
var ErrExpired = errors.New("groups: calibration expired")

// pendingCalibration is a scheduled listener calibration: its group, leader, run and slots, numbers and ids only
// (rule 8)
type pendingCalibration struct {
	groupID string
	leader  string
	runID   uint32
	rounds  int
	slots   []capschema.MediaGroupCalibrationSlot
	outputs map[string]capschema.MediaGroupOutput
	expires time.Time
}

// Heard is one member's chirp as the owner's browser measured it: code ok, weak, missed or unstable; ArrivalUs is its
// arrival less the leader's (the first slot's), us, the median of the rounds kept; SpreadUs their spread.
type Heard struct {
	DeviceID   string
	Code       string
	ArrivalUs  int
	Confidence float64
	SpreadUs   int
	Rounds     int
}

// scheduled keeps a listener calibration the leader scheduled and answers its schedule, with each member's trim now
func (s *Service) scheduled(g Group, leader string, r capschema.MediaGroupCalibration,
	outputs map[string]capschema.MediaGroupOutput) (Calibration, error) {
	slots, ok := groupSlots(g, r)
	if r.Code != "scheduled" || !ok {
		return Calibration{}, &CalibrationRefusedError{Code: "failed"}
	}
	id, err := calibrationID()
	if err != nil {
		return Calibration{}, err
	}
	now := s.d.Now()
	s.mu.Lock()
	for k, p := range s.pending {
		// a group's newer calibration replaces its earlier one: its chirps have sounded or never will
		if p.groupID == g.ID || !now.Before(p.expires) {
			delete(s.pending, k)
		}
	}
	s.pending[id] = pendingCalibration{groupID: g.ID, leader: leader, runID: r.RunID, rounds: max(r.Rounds, 1), slots: slots,
		outputs: outputs, expires: now.Add(listenerTTL)}
	s.mu.Unlock()
	out := Calibration{Code: "scheduled", Mode: capschema.GroupCalibrateModeListener, CalibrationID: id,
		StartsInMs: r.StartsInMs, Rounds: max(r.Rounds, 1), Chirp: r.Chirp, Probe: r.Probe, Slots: slots}
	slotted := map[string]bool{}
	for _, sl := range slots {
		slotted[sl.DeviceID] = true
	}
	for _, m := range g.Members {
		code := "scheduled"
		if !slotted[m.DeviceID] {
			// not connected to the leader: it plays no chirp, and its trim stays
			code = "offline"
		}
		out.Members = append(out.Members, CalibrationMember{DeviceID: m.DeviceID, Code: code,
			TrimUs: measuredTrim(m, outputs), Output: measuredOutput(outputs, m.DeviceID)})
	}
	s.d.Log.Info("group_calibration_scheduled", "group_id", g.ID, "slots", len(slots), "rounds", out.Rounds,
		"starts_in_ms", r.StartsInMs)
	return out, nil
}

// groupSlots are the leader's slots, canonical: each a member of the group, once per round, in a round the schedule
// has. not ok for none or any other
func groupSlots(g Group, r capschema.MediaGroupCalibration) ([]capschema.MediaGroupCalibrationSlot, bool) {
	member := map[string]bool{}
	for _, m := range g.Members {
		member[m.DeviceID] = true
	}
	rounds := max(r.Rounds, 1)
	if len(r.Slots) > capschema.GroupCalibrateMaxSlots {
		return nil, false
	}
	out := make([]capschema.MediaGroupCalibrationSlot, 0, len(r.Slots))
	type key struct {
		id    string
		round int
	}
	seen := map[key]bool{}
	for _, sl := range r.Slots {
		id, ok := canonicalID(sl.DeviceID)
		k := key{id, sl.Round}
		if !ok || !member[id] || seen[k] || sl.OffsetMs < 0 || sl.Round < 0 || sl.Round >= rounds {
			return nil, false
		}
		seen[k] = true
		out = append(out, capschema.MediaGroupCalibrationSlot{DeviceID: id, OffsetMs: sl.OffsetMs, Round: sl.Round, Code: sl.Code})
	}
	return out, len(out) > 0
}

func calibrationID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// take removes and answers the group's pending calibration by id, while it has not expired
func (s *Service) take(groupID, id string) (pendingCalibration, bool) {
	now := s.d.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[id]
	if !ok || p.groupID != groupID {
		return pendingCalibration{}, false
	}
	delete(s.pending, id)
	return p, now.Before(p.expires)
}

// firstSlots is each member's round-0 slot: the one its arrival is measured against
func firstSlots(slots []capschema.MediaGroupCalibrationSlot) []capschema.MediaGroupCalibrationSlot {
	out := make([]capschema.MediaGroupCalibrationSlot, 0, len(slots))
	for _, sl := range slots {
		if sl.Round == 0 {
			out = append(out, sl)
		}
	}
	return out
}

// ApplyListener applies what the owner's browser heard of a listener calibration: each member's offset is its arrival
// less its round-0 slot, and each member's trim the negation of its offset less the slots' mean. every member must
// have had a slot and been heard (ok, its rounds within unstableSpreadUs), else code incomplete; a trim past the
// bounds gives out_of_range; neither applies anything. the speakers' own measure of the run is fetched from the
// leader and shown beside it, never applied. the id is used once, whatever the outcome. only the applied trims and
// their ultrasonic result tokens are kept (rule 8).
func (s *Service) ApplyListener(ctx context.Context, groupID, id string, heard []Heard) (Calibration, error) {
	gid, ok := canonicalID(groupID)
	if !ok {
		return Calibration{}, ErrNotFound
	}
	by := map[string]Heard{}
	for i, h := range heard {
		dev, ok := canonicalID(h.DeviceID)
		if !ok {
			return Calibration{}, invalid("/body/members/" + strconv.Itoa(i) + "/deviceId")
		}
		if _, dup := by[dev]; dup {
			return Calibration{}, invalid("/body/members/" + strconv.Itoa(i) + "/deviceId")
		}
		h.DeviceID = dev
		if h.Code == "ok" && h.SpreadUs > unstableSpreadUs {
			h.Code = "unstable"
		}
		by[dev] = h
	}
	p, ok := s.take(gid, id)
	if !ok {
		return Calibration{}, ErrExpired
	}
	g, err := s.Get(ctx, gid)
	if err != nil {
		return Calibration{}, err
	}
	first := firstSlots(p.slots)
	slotted := map[string]bool{}
	for _, sl := range first {
		slotted[sl.DeviceID] = true
	}
	code, offsets := listenerOffsets(first, by)
	// a member without a slot (not connected to the leader) was not measured: centring the others on their own mean
	// would leave it out of step with them, so nothing applies, as in mics mode
	for _, m := range g.Members {
		if !slotted[m.DeviceID] {
			code, offsets = "incomplete", nil
			break
		}
	}
	next := map[string]int{}
	if code == "ok" {
		for id, us := range offsets {
			if -us < capschema.GroupTrimMinUs || -us > capschema.GroupTrimMaxUs {
				code = "out_of_range"
				next = map[string]int{}
				break
			}
			next[id] = -us
		}
	}
	mics := s.listenerResult(ctx, p)
	out := Calibration{Code: code, Mode: capschema.GroupCalibrateModeListener, Rounds: p.rounds}
	if len(next) > 0 {
		capabilities := map[string]string{}
		if mics != nil {
			capabilities = ultrasonicResults(*mics)
		}
		if err := s.storeTrims(ctx, g.ID, next, p.outputs, capabilities); err != nil {
			return Calibration{}, err
		}
		out.Applied = true
	}
	for _, m := range g.Members {
		cm := CalibrationMember{DeviceID: m.DeviceID, Code: "offline", TrimUs: measuredTrim(m, p.outputs),
			Output: measuredOutput(p.outputs, m.DeviceID)}
		if slotted[m.DeviceID] {
			cm.Code = "missed"
			if h, ok := by[m.DeviceID]; ok {
				cm.Code, cm.Confidence, cm.SpreadUs, cm.Rounds = h.Code, h.Confidence, max(h.SpreadUs, 0), max(h.Rounds, 0)
			}
			cm.OffsetUs = offsets[m.DeviceID]
		}
		if us, ok := next[m.DeviceID]; ok {
			cm.TrimUs = us
		}
		out.Members = append(out.Members, cm)
	}
	if mics != nil {
		withMics(&out, *mics, by, first)
	}
	s.d.Log.Info("group_calibrated", "group_id", g.ID, "mode", out.Mode, "code", code, "members", len(first),
		"slots", len(p.slots), "speaker_mics", mics != nil, "applied", out.Applied)
	if out.Applied {
		if _, err := s.changed(ctx, g.ID); err != nil {
			return Calibration{}, err
		}
	}
	return out, nil
}

// listenerResult is the speakers' own measure of the run, from its leader, or nil: a leader from before rounds (no
// run id), one that no longer holds the run, has no microphones or does not answer in time gives none
func (s *Service) listenerResult(ctx context.Context, p pendingCalibration) *capschema.MediaGroupCalibration {
	if p.runID == 0 || p.leader == "" || s.d.Commands == nil {
		return nil
	}
	run := p.runID
	args, _ := json.Marshal(capschema.MediaGroupCalibrateArgs{Mode: capschema.GroupCalibrateModeListenerResult, RunID: &run})
	wctx, cancel := context.WithTimeout(ctx, listenerResultWait)
	defer cancel()
	res, err := s.d.Commands.Deliver(wctx, p.leader, capschema.MediaGroupID, capschema.GroupCalibrateCommand, args)
	if err != nil {
		code := "failed"
		var refused *CalibrationRefusedError
		if errors.As(refusal(err), &refused) {
			code = refused.Code
		}
		s.d.Log.Info("group_listener_mics", "code", code)
		return nil
	}
	var r capschema.MediaGroupCalibration
	if json.Unmarshal(res, &r) != nil || r.Code == "" || len(r.Members) == 0 {
		s.d.Log.Info("group_listener_mics", "code", "failed")
		return nil
	}
	r = settled(r)
	s.d.Log.Info("group_listener_mics", "code", r.Code, "members", len(r.Members))
	return &r
}

// withMics puts the speakers' own measure beside each member's listener measure; for the members both measured ok
// (two at least), PathUs is the listener's offset less the speakers', re-centred on those members' mean: how much
// longer the sound takes to reach the listener from that member than from the others
func withMics(out *Calibration, mics capschema.MediaGroupCalibration, by map[string]Heard, first []capschema.MediaGroupCalibrationSlot) {
	residual := mics.ResidualUs
	out.MicsResidualUs = &residual
	measured := map[string]capschema.MediaGroupCalibrationMember{}
	for _, m := range mics.Members {
		if id, ok := canonicalID(m.DeviceID); ok {
			measured[id] = m
		}
	}
	// the listener's raw offsets of the members it heard ok, whatever the run's code (the re-centring below takes out
	// any common part)
	listener := map[string]float64{}
	for _, sl := range first {
		if h, ok := by[sl.DeviceID]; ok && h.Code == "ok" {
			listener[sl.DeviceID] = float64(h.ArrivalUs) - float64(sl.OffsetMs)*1000
		}
	}
	diff := map[string]float64{}
	mean := 0.0
	for i := range out.Members {
		cm := &out.Members[i]
		m, ok := measured[cm.DeviceID]
		if !ok {
			continue
		}
		cm.MicsCode, cm.MicsOffsetUs, cm.MicsSpreadUs = m.Code, m.OffsetUs, max(m.SpreadUs, 0)
		cm.Ultrasonic = m.Ultrasonic
		if cm.MicChannels == 0 {
			cm.MicChannels = max(m.MicChannels, 0)
		}
		if l, ok := listener[cm.DeviceID]; ok && m.Code == "ok" {
			diff[cm.DeviceID] = l - float64(m.OffsetUs)
			mean += diff[cm.DeviceID]
		}
	}
	if len(diff) < 2 {
		return
	}
	mean /= float64(len(diff))
	for i := range out.Members {
		if d, ok := diff[out.Members[i].DeviceID]; ok {
			us := int(math.Round(d - mean))
			out.Members[i].PathUs = &us
		}
	}
}

// listenerOffsets is each slot member's offset, us, its arrival less its slot less the slots' mean: code ok, or
// incomplete (and no offset) when a slot member was not heard ok
func listenerOffsets(slots []capschema.MediaGroupCalibrationSlot, by map[string]Heard) (string, map[string]int) {
	raw := make(map[string]float64, len(slots))
	sum := 0.0
	for _, sl := range slots {
		h, ok := by[sl.DeviceID]
		if !ok || h.Code != "ok" {
			return "incomplete", nil
		}
		v := float64(h.ArrivalUs) - float64(sl.OffsetMs)*1000
		raw[sl.DeviceID] = v
		sum += v
	}
	mean := sum / float64(len(slots))
	out := make(map[string]int, len(raw))
	for id, v := range raw {
		out[id] = int(math.Round(v - mean))
	}
	return "ok", out
}
