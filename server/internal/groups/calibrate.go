package groups

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// Commands runs a capability command on a device and waits for its result (commands.Service).
type Commands interface {
	Deliver(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error)
}

// calibrateWait is how long calibrate-group waits for the leader: mics mode with eight members and four rounds takes
// about 27 s, and the device itself gives up at 50 s
const calibrateWait = 55 * time.Second

// unstableSpreadUs: a member whose rounds spread past this is not trusted, whatever its code says
const unstableSpreadUs = 500

const (
	calibrationToleranceUs       = 2000
	stereoCalibrationToleranceUs = 250
)

var (
	// ErrNoLeader: no member can lead the group now (none online with media.group)
	ErrNoLeader = errors.New("groups: no leader")
	// ErrNoCommands: the service runs no calibration
	ErrNoCommands = errors.New("groups: no commands")
)

// CalibrationRefusedError is a calibration the leader refused or could not run: a token (not_leader, playing, busy,
// gated, no_capture, too_few, refused, unavailable, timeout, device_offline, failed).
type CalibrationRefusedError struct{ Code string }

func (e *CalibrationRefusedError) Error() string { return "groups: calibration refused: " + e.Code }

// CalibrationMember is one member of a calibration's result: the leader's measure, and the trim the member has now
// (the new one when the calibration applied). in listener mode the speakers' own measure of the same run may come
// with it (Mics*), and PathUs, the acoustic path to the listener relative to the others: informational, never applied.
type CalibrationMember struct {
	DeviceID    string
	Code        string
	OffsetUs    int
	Confidence  float64
	SpreadUs    int
	Ci95Us      int
	Rounds      int
	MicChannels int
	Ultrasonic  string
	TrimUs      int
	Output      *capschema.MediaGroupOutput
	// listener mode, when the speakers measured the run too
	MicsCode     string
	MicsOffsetUs int
	MicsSpreadUs int
	PathUs       *int
}

// Calibration is what calibrate-group and apply-group-calibration answer. Applied: every member was measured and the
// trims were stored. Mode is mics or listener; a listener calibration that was scheduled (code scheduled) carries its
// id, when its first chirp sounds, its rounds, its chirp and each member's slots.
type Calibration struct {
	Code       string
	Mode       string
	ResidualUs int
	Applied    bool
	Members    []CalibrationMember
	// listener mode, code scheduled (Rounds: any answer from a leader with rounds)
	CalibrationID string
	StartsInMs    int
	Rounds        int
	Chirp         *capschema.MediaGroupChirp
	Probe         *capschema.MediaGroupProbe
	Slots         []capschema.MediaGroupCalibrationSlot
	// listener mode, applied: the speakers' own solve's residual, when they measured the run
	MicsResidualUs *int
}

// Boost is the owner's louder chirp for a member heard faintly before: GainDb 0..14 over the usual level, which stays
// under the device's own volume (rule 7).
type Boost struct {
	DeviceID string
	GainDb   int
}

// ErrUnknownMode: calibrate takes mics or listener
var ErrUnknownMode = errors.New("groups: unknown calibration mode")

// Calibrate runs the group's acoustic calibration on its leader (the owner's, from the dashboard: rule 7). mode mics
// (or empty): when every member was measured, each member's trim is stored as the negation of its measured offset and
// the configs are pushed again. mode listener: the members' chirps are scheduled and the answer, code scheduled, says
// when; the owner's browser measures them and apply-group-calibration applies what it heard (ApplyListener). boost
// names members whose chirps play louder. only the applied trims and their ultrasonic result tokens are kept: no
// recording, no arrival (rule 8).
func (s *Service) Calibrate(ctx context.Context, groupID, mode string, boost []Boost) (Calibration, error) {
	if mode == "" {
		mode = capschema.GroupCalibrateModeMics
	}
	if mode != capschema.GroupCalibrateModeMics && mode != capschema.GroupCalibrateModeListener {
		return Calibration{}, ErrUnknownMode
	}
	g, err := s.Get(ctx, groupID)
	if err != nil {
		return Calibration{}, err
	}
	gains, err := boosts(g, boost)
	if err != nil {
		return Calibration{}, err
	}
	if s.d.Commands == nil {
		return Calibration{}, ErrNoCommands
	}
	leader := s.Leader(g.ID)
	if leader == "" {
		return Calibration{}, ErrNoLeader
	}
	// mics mode without a boost sends no mode: a device from before listener mode takes it as it always did
	a := capschema.MediaGroupCalibrateArgs{Boost: gains}
	tolerance := uint32(calibrationToleranceUs)
	if g.Kind == KindStereoPair {
		tolerance = stereoCalibrationToleranceUs
	}
	a.ToleranceUs = &tolerance
	outputs := s.calibrationOutputs(ctx, g)
	a.Priors = calibrationPriors(g, outputs)
	if mode == capschema.GroupCalibrateModeListener {
		a.Mode = mode
	}
	args, _ := json.Marshal(a)
	res, err := s.d.Commands.Deliver(commands.WithWait(ctx, calibrateWait), leader, capschema.MediaGroupID,
		capschema.GroupCalibrateCommand, args)
	if err != nil {
		return Calibration{}, refusal(err)
	}
	var r capschema.MediaGroupCalibration
	if json.Unmarshal(res, &r) != nil || r.Code == "" {
		return Calibration{}, &CalibrationRefusedError{Code: "failed"}
	}
	if mode == capschema.GroupCalibrateModeListener {
		return s.scheduled(g, leader, r, outputs)
	}
	r = settled(r)
	trims := map[string]int{}
	for _, m := range g.Members {
		trims[m.DeviceID] = measuredTrim(m, outputs)
	}
	out := Calibration{Code: r.Code, Mode: mode, ResidualUs: r.ResidualUs, Rounds: max(r.Rounds, 0)}
	next := map[string]int{}
	if r.Code == "ok" {
		next = newTrims(g, r)
	}
	if len(next) > 0 {
		if err := s.storeTrims(ctx, g.ID, next, outputs, ultrasonicResults(r)); err != nil {
			return Calibration{}, err
		}
		out.Applied = true
		for id, us := range next {
			trims[id] = us
		}
	}
	for _, m := range r.Members {
		id, ok := canonicalID(m.DeviceID)
		if !ok {
			continue
		}
		out.Members = append(out.Members, CalibrationMember{DeviceID: id, Code: m.Code, OffsetUs: m.OffsetUs,
			Confidence: m.Confidence, SpreadUs: max(m.SpreadUs, 0), Ci95Us: max(m.Ci95Us, 0), Rounds: max(m.Rounds, 0),
			MicChannels: max(m.MicChannels, 0), Ultrasonic: m.Ultrasonic, TrimUs: trims[id], Output: measuredOutput(outputs, id)})
	}
	s.d.Log.Info("group_calibrated", "group_id", g.ID, "mode", mode, "code", r.Code, "residual_us", r.ResidualUs,
		"members", len(r.Members), "boosts", len(gains), "applied", out.Applied)
	if out.Applied {
		if _, err := s.changed(ctx, g.ID); err != nil {
			return Calibration{}, err
		}
	}
	return out, nil
}

// boosts is the owner's boosts as the leader's args: by member id (the device's id), each a member of the group, once
func boosts(g Group, boost []Boost) (map[string]int, error) {
	if len(boost) == 0 {
		return nil, nil
	}
	member := map[string]bool{}
	for _, m := range g.Members {
		member[m.DeviceID] = true
	}
	out := make(map[string]int, len(boost))
	for i, b := range boost {
		id, ok := canonicalID(b.DeviceID)
		if _, dup := out[id]; !ok || !member[id] || dup {
			return nil, invalid("/body/boost/" + strconv.Itoa(i) + "/deviceId")
		}
		if b.GainDb < 0 || b.GainDb > capschema.GroupBoostMaxDb {
			return nil, invalid("/body/boost/" + strconv.Itoa(i) + "/gainDb")
		}
		out[id] = b.GainDb
	}
	return out, nil
}

// settled is a result with every ok member whose rounds spread past unstableSpreadUs made unstable, and an ok result
// with one made incomplete: such a member is not trusted
func settled(r capschema.MediaGroupCalibration) capschema.MediaGroupCalibration {
	members := make([]capschema.MediaGroupCalibrationMember, len(r.Members))
	for i, m := range r.Members {
		if m.Code == "ok" && m.SpreadUs > unstableSpreadUs {
			m.Code = "unstable"
			if r.Code == "ok" {
				r.Code = "incomplete"
			}
		}
		members[i] = m
	}
	r.Members = members
	return r
}

// newTrims is each member's trim from an ok result: the negation of its offset, within the trim's bounds. none when
// the result does not name every member of the group exactly once, measured
func newTrims(g Group, r capschema.MediaGroupCalibration) map[string]int {
	want := map[string]bool{}
	for _, m := range g.Members {
		want[m.DeviceID] = true
	}
	out := map[string]int{}
	for _, m := range r.Members {
		id, ok := canonicalID(m.DeviceID)
		if !ok || !want[id] || m.Code != "ok" {
			return nil
		}
		if _, dup := out[id]; dup {
			return nil
		}
		out[id] = min(max(-m.OffsetUs, capschema.GroupTrimMinUs), capschema.GroupTrimMaxUs)
	}
	if len(out) != len(want) {
		return nil
	}
	return out
}

// ultrasonicResults is the capability round's result by canonical member id; unknown or malformed values stay unknown.
func ultrasonicResults(r capschema.MediaGroupCalibration) map[string]string {
	out := map[string]string{}
	for _, m := range r.Members {
		id, ok := canonicalID(m.DeviceID)
		if !ok {
			continue
		}
		switch m.Ultrasonic {
		case "ok", "weak", "missed", "unsupported":
			out[id] = m.Ultrasonic
		}
	}
	return out
}

func (s *Service) storeTrims(ctx context.Context, groupID string, trims map[string]int,
	outputs map[string]capschema.MediaGroupOutput, ultrasonic map[string]string) error {
	gid, ok := parseID(groupID)
	if !ok {
		return ErrNotFound
	}
	return pgx.BeginFunc(ctx, s.d.Store, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := q.LockGroup(ctx, gid); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		for id, us := range trims {
			dev, _ := parseID(id)
			var n int64
			var err error
			if out, ok := outputs[id]; ok {
				// only the output that sounded in the run changes; the fallback and other routes keep their trims
				n, err = q.SetGroupOutputTrim(ctx, db.SetGroupOutputTrimParams{
					TrimUs: int32(us), GroupID: gid, DeviceID: dev, OutputID: out.ID, Ultrasonic: ultrasonic[id], //nolint:gosec // bounded
				})
			} else {
				// a member from before output reports keeps its trim as the fallback for every output
				n, err = q.SetGroupMemberTrim(ctx, db.SetGroupMemberTrimParams{
					TrimMs: int32(trimMs(us)), TrimUs: int32(us), GroupID: gid, DeviceID: dev, //nolint:gosec // bounded
				})
			}
			if err != nil {
				return err
			}
			if n == 0 {
				// the member left the group while it was measured: nothing of it applies
				return &CalibrationRefusedError{Code: "changed"}
			}
		}
		return nil
	})
}

// refusal is a failed command as the api answers it: its token alone
func refusal(err error) error {
	var device *commands.DeviceError
	switch {
	case errors.As(err, &device):
		return &CalibrationRefusedError{Code: device.Code}
	case errors.Is(err, commands.ErrOffline):
		return &CalibrationRefusedError{Code: "device_offline"}
	case errors.Is(err, commands.ErrTimeout):
		return &CalibrationRefusedError{Code: "timeout"}
	case errors.Is(err, commands.ErrBusy):
		return &CalibrationRefusedError{Code: "busy"}
	}
	return err
}
