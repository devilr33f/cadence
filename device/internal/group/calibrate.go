package group

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"math"
	"sort"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// calibrateWait is how long a calibrate command may run: mics mode with eight members and four rounds takes about
// 27 s, so this is past it with room (the server waits a little longer still)
const calibrateWait = 50 * time.Second

// calibrate runs the group's acoustic calibration on audiod, the group's leader: the owner's, from the dashboard. a
// command another device's voice turn vouched for is refused (rule 7: only the owner makes a group chirp), so is one
// with args other than a mode, a boost and (listener_result alone) a run id. a calibration that ran answers its
// result, whatever its code; one refused before it ran fails with the code alone. a listener calibration is answered
// at once with its schedule (code scheduled).
func (g *Group) calibrate(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	if _, vouched := capability.VouchedFrom(ctx); vouched {
		return nil, &capability.CommandError{Code: "refused"}
	}
	req, ok := calibrateRequest(args)
	if !ok {
		return nil, &capability.CommandError{Code: codeInvalid}
	}
	id := rand.Text()
	req.RequestId = id
	wait := make(chan *audiodv1.GroupCalibrated, 1)
	g.mu.Lock()
	send := g.send
	leader := g.cfg.Enabled && g.cfg.Role == capschema.GroupRoleLeader
	if leader && send != nil {
		if g.calibrations == nil {
			g.calibrations = map[string]chan *audiodv1.GroupCalibrated{}
		}
		g.calibrations[id] = wait
	}
	g.mu.Unlock()
	if !leader {
		return nil, &capability.CommandError{Code: "not_leader"}
	}
	if send == nil {
		return nil, &capability.CommandError{Code: "unavailable"}
	}
	defer func() {
		g.mu.Lock()
		delete(g.calibrations, id)
		g.mu.Unlock()
	}()
	env := &audiodv1.Envelope{Body: &audiodv1.Envelope_GroupCalibrate{GroupCalibrate: req}}
	if send(env) != nil {
		return nil, &capability.CommandError{Code: "unavailable"}
	}
	g.log.Info("group_calibrating", "group", g.groupID(), "mode", int(req.GetMode()), "boosts", len(req.GetBoost()))
	select {
	case r := <-wait:
		g.log.Info("group_calibrated", "code", r.GetCode(), "residual_us", r.GetResidualUs(), "members", len(r.GetMembers()))
		return calibration(r)
	case <-ctx.Done():
		return nil, &capability.CommandError{Code: "timeout"}
	}
}

// calibrateRequest reads the calibrate command's args: none, {} or {"mode", "boost", "runId"} as
// capschema.GroupCalibrateArgs says, nothing else
func calibrateRequest(args json.RawMessage) (*audiodv1.GroupCalibrate, bool) {
	t := bytes.TrimSpace(args)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return &audiodv1.GroupCalibrate{}, true
	}
	var a capschema.MediaGroupCalibrateArgs
	var fields map[string]json.RawMessage
	if decodeObject(t, &a) != nil || json.Unmarshal(t, &fields) != nil {
		return nil, false
	}
	for _, k := range []string{"mode", "boost", "runId", "toleranceUs", "priors"} {
		if v, ok := fields[k]; ok && bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return nil, false
		}
	}
	out := &audiodv1.GroupCalibrate{}
	switch a.Mode {
	case "", capschema.GroupCalibrateModeMics:
		out.Mode = audiodv1.GroupCalibrateMode_GROUP_CALIBRATE_MODE_UNSPECIFIED
	case capschema.GroupCalibrateModeListener:
		out.Mode = audiodv1.GroupCalibrateMode_GROUP_CALIBRATE_MODE_LISTENER
	case capschema.GroupCalibrateModeListenerResult:
		out.Mode = audiodv1.GroupCalibrateMode_GROUP_CALIBRATE_MODE_LISTENER_RESULT
	default:
		return nil, false
	}
	result := out.Mode == audiodv1.GroupCalibrateMode_GROUP_CALIBRATE_MODE_LISTENER_RESULT
	if result != (a.RunID != nil) || (result && (a.Boost != nil || a.Priors != nil || a.ToleranceUs != nil)) {
		return nil, false
	}
	if a.RunID != nil {
		out.RunId = *a.RunID
	}
	if a.ToleranceUs != nil {
		out.ToleranceUs = *a.ToleranceUs
	}
	if len(a.Boost) > capschema.GroupMaxMembers {
		return nil, false
	}
	ids := make([]string, 0, len(a.Boost))
	for id, db := range a.Boost {
		if id == "" || len(id) > 64 || db < 0 || db > capschema.GroupBoostMaxDb {
			return nil, false
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out.Boost = append(out.Boost, &audiodv1.GroupCalibrateBoost{MemberId: id, GainDb: uint32(a.Boost[id])}) //nolint:gosec // bounded above
	}
	if len(a.Priors) > capschema.GroupMaxMembers {
		return nil, false
	}
	ids = ids[:0]
	for id, prior := range a.Priors {
		if id == "" || len(id) > 64 || prior.PriorUs < -2_000_000_000 || prior.PriorUs > 2_000_000_000 ||
			prior.PriorSpanUs < 0 || prior.PriorSpanUs > 4_000_000_000 {
			return nil, false
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		prior := a.Priors[id]
		out.Priors = append(out.Priors, &audiodv1.GroupCalibratePrior{MemberId: id,
			PriorUs: int32(prior.PriorUs), PriorSpanUs: uint32(prior.PriorSpanUs)}) //nolint:gosec // bounded above
	}
	return out, true
}

func (g *Group) groupID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cfg.GroupID
}

// calibrated hands audiod's GroupCalibrated to the command waiting for it; another's is dropped
func (g *Group) calibrated(r *audiodv1.GroupCalibrated) {
	g.mu.Lock()
	wait, ok := g.calibrations[r.GetRequestId()]
	delete(g.calibrations, r.GetRequestId())
	g.mu.Unlock()
	if ok {
		wait <- r
	}
}

func ultrasonicToken(token string) string {
	switch token {
	case "ok", "weak", "missed", "unsupported":
		return token
	default:
		return ""
	}
}

// calibration is a GroupCalibrated as the command's result: tokens, ids and numbers alone
func calibration(r *audiodv1.GroupCalibrated) (json.RawMessage, error) {
	code := r.GetCode()
	if !errorToken.MatchString(code) {
		code = "failed"
	}
	if code == codeScheduled && len(r.GetSlots()) > 0 {
		return schedule(r)
	}
	if len(r.GetMembers()) == 0 {
		return nil, &capability.CommandError{Code: code}
	}
	out := capschema.MediaGroupCalibration{Code: code, ResidualUs: unsigned(r.GetResidualUs()),
		RunID: r.GetRunId(), Rounds: unsigned(r.GetRounds()),
		Members: make([]capschema.MediaGroupCalibrationMember, 0, len(r.GetMembers()))}
	for _, m := range r.GetMembers() {
		mc := m.GetCode()
		if !errorToken.MatchString(mc) {
			mc = "failed"
		}
		conf := float64(m.GetConfidence())
		if math.IsNaN(conf) || conf < 0 {
			conf = 0
		}
		out.Members = append(out.Members, capschema.MediaGroupCalibrationMember{
			DeviceID: m.GetMemberId(), Code: mc, OffsetUs: int(m.GetOffsetUs()), Confidence: math.Min(conf, 1),
			SpreadUs: unsigned(m.GetSpreadUs()), Ci95Us: unsigned(m.GetCi95Us()), Rounds: unsigned(m.GetRounds()),
			MicChannels: unsigned(m.GetMicChannels()), Ultrasonic: ultrasonicToken(m.GetUltrasonic()),
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, &capability.CommandError{Code: "failed"}
	}
	return b, nil
}

// a listener calibration's answer: scheduled, with when its first chirp starts and each member's slots
const codeScheduled = "scheduled"

// schedule is a listener calibration's GroupCalibrated as the command's result: ids and numbers alone
func schedule(r *audiodv1.GroupCalibrated) (json.RawMessage, error) {
	out := capschema.MediaGroupCalibration{Code: codeScheduled, StartsInMs: unsigned(r.GetStartsInMs()),
		RunID: r.GetRunId(), Rounds: unsigned(r.GetRounds()), Members: []capschema.MediaGroupCalibrationMember{}}
	if c := r.GetChirp(); c != nil {
		out.Chirp = &capschema.MediaGroupChirp{F0Hz: unsigned(c.GetF0Hz()), F1Hz: unsigned(c.GetF1Hz()),
			DurationUs: unsigned(c.GetDurationUs()), FadeUs: unsigned(c.GetFadeUs())}
	}
	if p := r.GetProbe(); p != nil {
		kind := "unspecified"
		if p.GetKind() == audiodv1.GroupProbeKind_GROUP_PROBE_KIND_GOLD {
			kind = "gold"
		}
		out.Probe = &capschema.MediaGroupProbe{Kind: kind, BandLoHz: unsigned(p.GetBandLoHz()),
			BandHiHz: unsigned(p.GetBandHiHz()), FadeUs: unsigned(p.GetFadeUs())}
	}
	for _, s := range r.GetSlots() {
		if len(out.Slots) == capschema.GroupCalibrateMaxSlots {
			break
		}
		if id := s.GetMemberId(); id == "" || len(id) > 64 {
			continue
		}
		out.Slots = append(out.Slots, capschema.MediaGroupCalibrationSlot{DeviceID: s.GetMemberId(),
			OffsetMs: unsigned(s.GetOffsetMs()), Round: unsigned(s.GetRound()), Code: s.GetCode()})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, &capability.CommandError{Code: "failed"}
	}
	return b, nil
}
