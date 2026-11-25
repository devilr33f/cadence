package groups

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
)

const tag = "groups"

// GroupMemberRequest is one member of a group's create or update.
type GroupMemberRequest struct {
	DeviceID string `json:"deviceId" doc:"the device's id; a device is in one group at most"`
	Channel  string `json:"channel,omitempty" enum:"both,left,right,mono" doc:"what the device plays of the stereo stream; both by default. A stereo pair has one left and one right"`
	TrimMs   int    `json:"trimMs,omitempty" minimum:"-500" maximum:"500" doc:"the device's own delay, ms, against the group: an acoustic correction"`
	TrimUs   int    `json:"trimUs,omitempty" minimum:"-500000" maximum:"500000" doc:"the same delay to the microsecond (a calibration's); when set, trimMs is not read"`
}

// GroupRequest is a group as create-group and update-group take it; an update replaces it whole.
type GroupRequest struct {
	Name            string               `json:"name" maxLength:"256" doc:"trimmed of white space, then 1–64 characters with no control or format character; no other group's name, in any case. The agent's device argument matches it"`
	Kind            string               `json:"kind" enum:"group,stereo_pair" doc:"group: 2–8 devices; stereo_pair: two speakers, one left and one right, held to 100 us"`
	PreferredLeader string               `json:"preferredLeader,omitempty" doc:"the member that leads while it is online; else the first online member by position, speakers before screens"`
	LatencyMs       *int                 `json:"latencyMs,omitempty" minimum:"200" maximum:"5000" doc:"how far behind the leader's clock the group plays, ms; 1000 by default"`
	BitrateKbps     *int                 `json:"bitrateKbps,omitempty" minimum:"64" maximum:"510" doc:"the stream's opus bitrate; 256 by default"`
	PCM             bool                 `json:"pcm,omitempty" doc:"stream uncompressed pcm instead of opus"`
	WaitMs          *int                 `json:"waitMs,omitempty" minimum:"0" maximum:"10000" doc:"how long a start waits for every member, ms; 3000 by default"`
	AutoLatency     *bool                `json:"autoLatency,omitempty" doc:"the leader tunes the latency itself, up when a member falls behind and down while every one keeps a margin; latencyMs is where it starts. On by default"`
	AutoBitrate     *bool                `json:"autoBitrate,omitempty" doc:"the leader steps the opus bitrate down while a member falls behind and back up once it keeps up; bitrateKbps is the most it uses. Not with pcm. On by default"`
	AutoWait        *bool                `json:"autoWait,omitempty" doc:"the leader sets a start's wait from how long the members took to be ready; waitMs is the most it waits. On by default"`
	FollowVideo     *bool                `json:"followVideo,omitempty" doc:"a video started on one of the group's screens opens on its other screens too, muted, their picture following the group's sound. On by default"`
	TrackingEnabled bool                 `json:"trackingEnabled,omitempty" doc:"opt into ultrasonic live tracking while music plays; off by default. At least two members' active outputs need an applied calibration with ultrasonic ok"`
	Members         []GroupMemberRequest `json:"members" maxItems:"16" doc:"the members in their order"`
}

// JoinGroupRequest names the devices that join the device's group (Home Assistant's join).
type JoinGroupRequest struct {
	Members []string `json:"members" minItems:"1" maxItems:"8" doc:"device ids; each leaves the group it was in"`
}

// GroupOutputTrimView is one output's trim and the ultrasonic capability its last applied calibration measured.
type GroupOutputTrimView struct {
	OutputID   string `json:"outputId"`
	TrimUs     int    `json:"trimUs" minimum:"-500000" maximum:"500000"`
	Ultrasonic string `json:"ultrasonic,omitempty" enum:"ok,weak,missed,unsupported" doc:"the last applied calibration's ultrasonic result for this output; absent when unknown"`
}

// GroupMemberView is one member as a group shows it.
type GroupMemberView struct {
	DeviceID string                `json:"deviceId"`
	Channel  string                `json:"channel" enum:"both,left,right,mono"`
	TrimMs   int                   `json:"trimMs" doc:"trimUs rounded to the millisecond"`
	TrimUs   int                   `json:"trimUs" doc:"the fallback trim for an output with no entry in trims, us"`
	Trims    []GroupOutputTrimView `json:"trims,omitempty" nullable:"false" doc:"the calibrated trim and ultrasonic result of each output id; cadenced applies the active trim, else trimUs"`
	Online   bool                  `json:"online" doc:"linked to the server now"`
	// Status is get-group's alone
	Status *capschema.MediaGroupStatus `json:"status,omitempty" doc:"get-group only: the member's media.group state as it last reported it; a leader's lists its view of each member"`
}

// GroupTuningView is what the group's leader plays with now, as its media.group state last reported it: with an auto
// flag on, the leader's own choice, else the group's setting.
type GroupTuningView struct {
	LatencyMs   int `json:"latencyMs" doc:"the latency in force, ms"`
	BitrateKbps int `json:"bitrateKbps" doc:"the opus bitrate in force; 0 with pcm"`
	WaitMs      int `json:"waitMs" doc:"the start wait in force, ms"`
}

// TrackingMemberView is one tracked member's latest smoothed measurement and last correction the server applied.
type TrackingMemberView struct {
	DeviceID         string     `json:"deviceId"`
	OffsetUs         int        `json:"offsetUs" doc:"the smoothed group offset, us"`
	Ci95Us           int        `json:"ci95Us" minimum:"0" doc:"the 95% confidence interval's half-width, us"`
	Cycles           int        `json:"cycles" minimum:"0" maximum:"6"`
	LastCorrectionUs int        `json:"lastCorrectionUs,omitempty" doc:"the newest correction the server applied to this member's active output, us"`
	LastCorrectionAt *time.Time `json:"lastCorrectionAt,omitempty" doc:"when that correction was stored and pushed"`
}

// TrackingView is the leader's live ultrasonic tracking state, kept in memory only.
type TrackingView struct {
	State   string               `json:"state" enum:"off,idle,tracking,needs_calibration,unsupported"`
	Members []TrackingMemberView `json:"members" nullable:"false"`
}

// GroupView is a group as the api answers it.
type GroupView struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Kind            string            `json:"kind" enum:"group,stereo_pair"`
	PreferredLeader string            `json:"preferredLeader" doc:"empty for none"`
	LeaderID        string            `json:"leaderId" doc:"the member elected to lead now (the stream source: the member playing a source of its own, else the clock master); empty while none can (no member online with media.group and a lan address)"`
	ClockLeaderID   string            `json:"clockLeaderId" doc:"the member that serves the group clock (the clock master): leaderId too unless another member plays a source of its own; empty while none can lead"`
	Active          bool              `json:"active" doc:"the leader streams to at least one online member"`
	LatencyMs       int               `json:"latencyMs"`
	BitrateKbps     int               `json:"bitrateKbps"`
	PCM             bool              `json:"pcm"`
	WaitMs          int               `json:"waitMs"`
	AutoLatency     bool              `json:"autoLatency" doc:"the leader tunes the latency; latencyMs is where it starts"`
	AutoBitrate     bool              `json:"autoBitrate" doc:"the leader tunes the bitrate; bitrateKbps is its ceiling"`
	AutoWait        bool              `json:"autoWait" doc:"the leader tunes the start wait; waitMs is its ceiling"`
	FollowVideo     bool              `json:"followVideo" doc:"a video started on one of the group's screens opens on its other screens too"`
	TrackingEnabled bool              `json:"trackingEnabled" doc:"the group's ultrasonic live-tracking opt-in; off by default"`
	Tracking        TrackingView      `json:"tracking" doc:"the leader's in-memory tracking state and each member's latest measurements"`
	Tuning          *GroupTuningView  `json:"tuning,omitempty" doc:"what the leader plays with now; absent while no leader reports it"`
	Members         []GroupMemberView `json:"members" nullable:"false"`
	CreatedAt       time.Time         `json:"createdAt"`
	UpdatedAt       time.Time         `json:"updatedAt"`
}

type idIn struct {
	ID string `path:"id"`
}

type createIn struct{ Body GroupRequest }

type updateIn struct {
	ID   string `path:"id"`
	Body GroupRequest
}

type joinIn struct {
	ID   string `path:"id"`
	Body JoinGroupRequest
}

type groupOut struct{ Body GroupView }

// CalibrationMemberView is one member of a calibration.
type CalibrationMemberView struct {
	DeviceID     string                      `json:"deviceId"`
	Code         string                      `json:"code" doc:"ok; no_self (it did not hear its own probe), isolated (no pair links it to the others), unstable (its rounds disagree, or spread past 0.5 ms), missed (its own coded probe was not found), no_answer (nothing arrived by its deadline), analysis_timeout (it recorded but its analysis exceeded its budget), offline, weak, or its own refusal (busy: another source plays on it; gated: its volume does not hold yet; no_capture, muted, unsynced, epoch)"`
	OffsetUs     int                         `json:"offsetUs" doc:"its output latency less the measured members' mean, us, its trim left out"`
	Confidence   float64                     `json:"confidence" minimum:"0" maximum:"1" doc:"how far its chirps stood above the room's noise"`
	SpreadUs     int                         `json:"spreadUs" minimum:"0" doc:"how far its rounds spread around their median, us (0 from a leader before rounds)"`
	Ci95Us       int                         `json:"ci95Us" minimum:"0" doc:"the two-sided 95% confidence interval's half-width around offsetUs, us; 0 from a legacy run"`
	Rounds       int                         `json:"rounds" minimum:"0" doc:"the rounds its measure kept (0 from a leader before rounds)"`
	MicChannels  int                         `json:"micChannels" minimum:"0" doc:"the microphone channels it analysed (0: none, or not reported)"`
	Ultrasonic   string                      `json:"ultrasonic,omitempty" enum:"ok,weak,missed,unsupported" doc:"the ultrasonic capability round's result; absent from a legacy run"`
	TrimUs       int                         `json:"trimUs" doc:"the output's trim now: -offsetUs when the calibration applied, else the one it had"`
	Output       *capschema.MediaGroupOutput `json:"output,omitempty" doc:"the active output snapshotted when the run started; the trim applies to its id. Absent for a legacy member's fallback trim"`
	MicsCode     string                      `json:"micsCode,omitempty" doc:"listener, applied: the speakers' own measure of the same run, its code for this member (as mics mode's); absent when the speakers measured nothing"`
	MicsOffsetUs int                         `json:"micsOffsetUs,omitempty" doc:"listener, applied: the speakers' own offset for this member, us (informational, never applied)"`
	MicsSpreadUs int                         `json:"micsSpreadUs,omitempty" doc:"listener, applied: the spread of the speakers' own measure, us"`
	PathUs       *int                        `json:"pathUs,omitempty" doc:"listener, applied: offsetUs less micsOffsetUs, re-centred on the members both measured ok (two at least): how much longer its sound takes to reach the listener than the others', us"`
}

// CalibrationSlotView is one member's chirp of one round in a listener calibration.
type CalibrationSlotView struct {
	DeviceID string `json:"deviceId"`
	OffsetMs int    `json:"offsetMs" doc:"when its probe starts, ms after the first one (the leader's of round 0, at 0)"`
	Round    int    `json:"round" minimum:"0" doc:"its round, from 0; every member plays once a round, the leader first"`
	Code     uint32 `json:"code" minimum:"0" doc:"the gold code of this slot's coded probe; 0 is a valid code, and legacy chirps ignore it"`
}

// CalibrationChirpView is the sweep a listener calibration's members play.
type CalibrationChirpView struct {
	F0Hz       int `json:"f0Hz" doc:"where the exponential sweep starts, Hz"`
	F1Hz       int `json:"f1Hz" doc:"where it ends, Hz"`
	DurationUs int `json:"durationUs"`
	FadeUs     int `json:"fadeUs" doc:"its raised-cosine fades, us"`
}

// CalibrationProbeView is the coded probe a listener calibration's members play.
type CalibrationProbeView struct {
	Kind     string `json:"kind" enum:"gold,unspecified" doc:"gold: a member-specific gold-coded probe"`
	BandLoHz int    `json:"bandLoHz" doc:"the coded probe's lower band edge, Hz"`
	BandHiHz int    `json:"bandHiHz" doc:"the coded probe's upper band edge, Hz"`
	FadeUs   int    `json:"fadeUs" doc:"its raised-cosine fades, us"`
}

// CalibrationView is calibrate-group's and apply-group-calibration's answer.
type CalibrationView struct {
	Code           string                  `json:"code" doc:"ok: every member measured, and the trims applied; incomplete: some member could not be measured (see each one's code), nothing applied; inconsistent: the pairs disagree by more than a millisecond (a reflection, a moved speaker), nothing applied; out_of_range (listener): a trim would pass 500 ms, nothing applied; scheduled (listener): the chirps are on their way, see slots"`
	Mode           string                  `json:"mode" enum:"mics,listener" doc:"mics: the speakers' own microphones; listener: the owner's browser where they listen"`
	ResidualUs     int                     `json:"residualUs" doc:"how far the pairs disagree with the solution, us (0 with two members, and in listener mode)"`
	Applied        bool                    `json:"applied" doc:"the members' trims were stored and pushed"`
	Members        []CalibrationMemberView `json:"members" nullable:"false" doc:"the leader first"`
	CalibrationID  string                  `json:"calibrationId,omitempty" doc:"scheduled alone: the id apply-group-calibration takes, once, within 120 s"`
	StartsInMs     int                     `json:"startsInMs,omitempty" doc:"scheduled alone: the first chirp starts this long after the leader answered, ms"`
	Rounds         int                     `json:"rounds,omitempty" doc:"how many times every member chirped: scheduled (1 from a leader before rounds), and the results of a run with rounds"`
	Chirp          *CalibrationChirpView   `json:"chirp,omitempty" doc:"scheduled alone: the legacy sweep; absent from a leader before rounds, which plays 1-6 kHz, 100 ms, 5 ms fades"`
	Probe          *CalibrationProbeView   `json:"probe,omitempty" doc:"scheduled with calibration version 2: the coded probe's shape; each slot's code identifies its member"`
	Slots          []CalibrationSlotView   `json:"slots,omitempty" doc:"scheduled alone: each member's probe of each round, round-major, the leader first in each; a member not connected to the leader has none"`
	MicsResidualUs *int                    `json:"micsResidualUs,omitempty" doc:"listener, applied: the residual of the speakers' own solve of the same run, when they measured it"`
}

type calibrationOut struct{ Body CalibrationView }

// CalibrateBoostRequest is a louder chirp for a member heard faintly before.
type CalibrateBoostRequest struct {
	DeviceID string `json:"deviceId" doc:"a member of the group"`
	GainDb   int    `json:"gainDb" minimum:"0" maximum:"14" doc:"how much louder its chirps play, dB; they stay under the device's own volume (at most 6 dB under it), which never changes"`
}

// CalibrateGroupRequest is calibrate-group's optional body.
type CalibrateGroupRequest struct {
	Mode  string                  `json:"mode,omitempty" enum:"mics,listener" doc:"mics (the default): every speaker records its own microphone and the trims apply at once; listener: every member plays its chirps at its slots, the answer (code scheduled) says when, and the owner's browser posts what it heard to apply-group-calibration"`
	Boost []CalibrateBoostRequest `json:"boost,omitempty" maxItems:"8" doc:"members whose chirps play louder, each once"`
}

type calibrateIn struct {
	ID   string `path:"id"`
	Body *CalibrateGroupRequest
}

// HeardMemberRequest is one member's chirp as the owner's browser heard it.
type HeardMemberRequest struct {
	DeviceID   string  `json:"deviceId"`
	Code       string  `json:"code" enum:"ok,weak,missed,unstable" doc:"ok: heard; weak: too faint over the room's noise; missed: not heard where it was expected; unstable: heard, but its rounds disagree"`
	ArrivalUs  int     `json:"arrivalUs" minimum:"-60000000" maximum:"60000000" doc:"its arrival less its round-0 slot's place in the leader's round-0 frame (the median of the rounds kept), us; the leader's own is about 0"`
	Confidence float64 `json:"confidence" minimum:"0" maximum:"1" doc:"how far its chirp stood above the room's noise"`
	SpreadUs   int     `json:"spreadUs,omitempty" minimum:"0" maximum:"60000000" doc:"the standard deviation of the rounds kept, us; an ok one past 500 is taken as unstable"`
	Rounds     int     `json:"rounds,omitempty" minimum:"0" maximum:"32" doc:"the rounds kept"`
}

// ApplyCalibrationRequest is what the owner's browser heard of a listener calibration: numbers only, no audio.
type ApplyCalibrationRequest struct {
	Members []HeardMemberRequest `json:"members" maxItems:"8" nullable:"false"`
}

type applyIn struct {
	ID            string `path:"id"`
	CalibrationID string `path:"calibrationId" maxLength:"64"`
	Body          ApplyCalibrationRequest
}

func calibrationView(c Calibration) CalibrationView {
	v := CalibrationView{Code: c.Code, Mode: c.Mode, ResidualUs: c.ResidualUs, Applied: c.Applied,
		Members: make([]CalibrationMemberView, 0, len(c.Members)), CalibrationID: c.CalibrationID, StartsInMs: c.StartsInMs,
		Rounds: c.Rounds, MicsResidualUs: c.MicsResidualUs}
	if c.Chirp != nil {
		ch := CalibrationChirpView(*c.Chirp)
		v.Chirp = &ch
	}
	if c.Probe != nil {
		probe := CalibrationProbeView(*c.Probe)
		v.Probe = &probe
	}
	for _, m := range c.Members {
		v.Members = append(v.Members, CalibrationMemberView(m))
	}
	for _, sl := range c.Slots {
		v.Slots = append(v.Slots, CalibrationSlotView(sl))
	}
	return v
}

type listOut struct{ Body []GroupView }

func spec(r GroupRequest) Spec {
	s := Spec{Name: r.Name, Kind: r.Kind, PreferredLeader: r.PreferredLeader, LatencyMs: r.LatencyMs,
		BitrateKbps: r.BitrateKbps, WaitMs: r.WaitMs, PCM: r.PCM, AutoLatency: r.AutoLatency, AutoBitrate: r.AutoBitrate,
		AutoWait: r.AutoWait, FollowVideo: r.FollowVideo, TrackingEnabled: r.TrackingEnabled}
	for _, m := range r.Members {
		s.Members = append(s.Members, Member{DeviceID: m.DeviceID, Channel: m.Channel, TrimMs: m.TrimMs, TrimUs: m.TrimUs})
	}
	return s
}

func (s *Service) view(ctx context.Context, g Group, status bool) GroupView {
	v := GroupView{ID: g.ID, Name: g.Name, Kind: g.Kind, PreferredLeader: g.PreferredLeader, LeaderID: s.Leader(g.ID),
		ClockLeaderID: s.Clock(g.ID),
		LatencyMs:     g.LatencyMs, BitrateKbps: g.BitrateKbps, PCM: g.PCM, WaitMs: g.WaitMs, AutoLatency: g.AutoLatency,
		AutoBitrate: g.AutoBitrate, AutoWait: g.AutoWait, FollowVideo: g.FollowVideo, TrackingEnabled: g.TrackingEnabled,
		Tracking: s.trackingView(g), Members: make([]GroupMemberView, 0, len(g.Members)), CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt}
	s.mu.Lock()
	v.Active = s.active[g.ID]
	s.mu.Unlock()
	if v.LeaderID != "" {
		if st, ok := s.Status(ctx, v.LeaderID); ok && st.Role == capschema.GroupRoleLeader && st.GroupID == g.ID && st.LatencyMs > 0 {
			v.Tuning = &GroupTuningView{LatencyMs: st.LatencyMs, BitrateKbps: st.BitrateKbps, WaitMs: st.WaitMs}
		}
	}
	for _, m := range g.Members {
		trims := make([]GroupOutputTrimView, 0, len(m.Trims))
		for _, t := range m.Trims {
			trims = append(trims, GroupOutputTrimView(t))
		}
		mv := GroupMemberView{DeviceID: m.DeviceID, Channel: m.Channel, TrimMs: m.TrimMs, TrimUs: m.TrimUs,
			Trims: trims, Online: s.Online(m.DeviceID)}
		if status {
			mv.Status, _ = s.Status(ctx, m.DeviceID)
		}
		v.Members = append(v.Members, mv)
	}
	return v
}

// failure maps the service's refusals to the api's: codes and paths only
func (s *Service) failure(err error) error {
	var inv *InvalidError
	var in *InGroupError
	switch {
	case errors.As(err, &inv):
		return api.Error(http.StatusUnprocessableEntity, inv.Code, inv.Path)
	case errors.As(err, &in):
		return api.Error(http.StatusConflict, "in_group", in.Path)
	case errors.Is(err, ErrNameTaken):
		return api.Error(http.StatusConflict, "name_taken", "/body/name")
	case errors.Is(err, ErrStereoPair):
		return api.Error(http.StatusConflict, "stereo_pair")
	case errors.Is(err, ErrNotFound), errors.Is(err, devices.ErrNotFound):
		return api.Error(http.StatusNotFound, "not_found")
	case errors.Is(err, ErrNoLeader):
		return api.Error(http.StatusConflict, "no_leader")
	case errors.Is(err, ErrExpired):
		return api.Error(http.StatusNotFound, "expired")
	case errors.Is(err, ErrUnknownMode):
		return api.Error(http.StatusUnprocessableEntity, "invalid", "/body/mode")
	case errors.Is(err, ErrNoCommands):
		return api.Error(http.StatusConflict, "unavailable")
	case errors.Is(err, devices.ErrUnknownCommand), errors.Is(err, devices.ErrUnknownCapability):
		return api.Error(http.StatusConflict, "unsupported")
	}
	var refused *CalibrationRefusedError
	if errors.As(err, &refused) {
		if refused.Code == "timeout" {
			return api.Error(http.StatusGatewayTimeout, "timeout")
		}
		return api.Error(http.StatusConflict, refused.Code)
	}
	s.d.Log.Error("group_failed")
	return api.Error(http.StatusInternalServerError, "internal")
}

// RegisterAdmin mounts the groups' routes and a device's join and leave; s is read only when a route is called.
func RegisterAdmin(a huma.API, s *Service) {
	defer api.Describe(a, docs)
	huma.Register(a, huma.Operation{
		OperationID: "list-groups", Method: http.MethodGet, Path: "/groups", Tags: []string{tag},
	}, func(ctx context.Context, _ *struct{}) (*listOut, error) {
		gs, err := s.List(ctx)
		if err != nil {
			return nil, s.failure(err)
		}
		out := &listOut{Body: make([]GroupView, 0, len(gs))}
		for _, g := range gs {
			out.Body = append(out.Body, s.view(ctx, g, false))
		}
		return out, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "get-group", Method: http.MethodGet, Path: "/groups/{id}", Tags: []string{tag},
	}, func(ctx context.Context, in *idIn) (*groupOut, error) {
		g, err := s.Get(ctx, in.ID)
		if err != nil {
			return nil, s.failure(err)
		}
		return &groupOut{Body: s.view(ctx, g, true)}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "create-group", Method: http.MethodPost, Path: "/groups", Tags: []string{tag},
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *createIn) (*groupOut, error) {
		g, err := s.Create(ctx, spec(in.Body))
		if err != nil {
			return nil, s.failure(err)
		}
		return &groupOut{Body: s.view(ctx, g, false)}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "update-group", Method: http.MethodPut, Path: "/groups/{id}", Tags: []string{tag},
	}, func(ctx context.Context, in *updateIn) (*groupOut, error) {
		g, err := s.Update(ctx, in.ID, spec(in.Body))
		if err != nil {
			return nil, s.failure(err)
		}
		return &groupOut{Body: s.view(ctx, g, false)}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "delete-group", Method: http.MethodDelete, Path: "/groups/{id}", Tags: []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *idIn) (*struct{}, error) {
		if err := s.Delete(ctx, in.ID); err != nil {
			return nil, s.failure(err)
		}
		return &struct{}{}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "calibrate-group", Method: http.MethodPost, Path: "/groups/{id}/calibrate", Tags: []string{tag},
		Description: "Every member plays a short chirp through its group playout, a few rounds (at the device's own level " +
			"less 20 dB, never above it) and records its microphones, in memory only; the leader solves each member's " +
			"output latency from the round trips (distance and microphones cancel), each arrival the median of its rounds. " +
			"When every member was measured, its rounds agreeing within 0.5 ms, each member's trim becomes the negation of " +
			"its offset and the configs are pushed again; only the trims and ultrasonic result tokens are stored. The owner's alone: refused while the group " +
			"plays (409 playing), while its leader's volume does not hold yet (gated), with no member online (too_few) and " +
			"in a voice turn. Takes up to about 30 s. boost plays the named members' chirps louder (up to 14 dB, still 6 dB " +
			"under their own volume, which never changes). With mode listener, every member connected to the leader plays " +
			"its chirps at its slots, the answer (code scheduled) comes at once with the schedule and a calibration id, " +
			"and the owner's browser, recording where they listen, posts the arrivals it measured to " +
			"apply-group-calibration.",
	}, func(ctx context.Context, in *calibrateIn) (*calibrationOut, error) {
		mode := ""
		var boost []Boost
		if in.Body != nil {
			mode = in.Body.Mode
			for _, b := range in.Body.Boost {
				boost = append(boost, Boost(b))
			}
		}
		c, err := s.Calibrate(ctx, in.ID, mode, boost)
		if err != nil {
			return nil, s.failure(err)
		}
		return &calibrationOut{Body: calibrationView(c)}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "apply-group-calibration", Method: http.MethodPost, Path: "/groups/{id}/calibrations/{calibrationId}",
		Tags: []string{tag},
		Description: "The arrivals the owner's browser measured of a listener calibration's chirps (numbers only: no audio " +
			"reaches the server). Each slot member's offset is its arrival less its round-0 slot; its trim becomes the " +
			"negation of its offset less the slots' mean, stored and pushed as calibrate-group's. A slot member missing, " +
			"not ok or spread past 0.5 ms gives code incomplete, a trim past 500 ms out_of_range; neither applies anything. " +
			"The speakers' own measure of the same run, when their leader has one, is shown beside each member (micsCode, " +
			"micsOffsetUs, pathUs), never applied. The id is used once, within 120 s of its scheduling: an unknown, used " +
			"or expired one is refused 404 expired.",
	}, func(ctx context.Context, in *applyIn) (*calibrationOut, error) {
		heard := make([]Heard, 0, len(in.Body.Members))
		for _, m := range in.Body.Members {
			heard = append(heard, Heard(m))
		}
		c, err := s.ApplyListener(ctx, in.ID, in.CalibrationID, heard)
		if err != nil {
			return nil, s.failure(err)
		}
		return &calibrationOut{Body: calibrationView(c)}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "join-device-group", Method: http.MethodPost, Path: "/devices/{id}/group", Tags: []string{tag},
	}, func(ctx context.Context, in *joinIn) (*groupOut, error) {
		g, err := s.Join(ctx, in.ID, in.Body.Members)
		if err != nil {
			return nil, s.failure(err)
		}
		return &groupOut{Body: s.view(ctx, g, false)}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "leave-device-group", Method: http.MethodDelete, Path: "/devices/{id}/group", Tags: []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *idIn) (*struct{}, error) {
		if err := s.Leave(ctx, in.ID); err != nil {
			return nil, s.failure(err)
		}
		return &struct{}{}, nil
	})
}
