package media

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"time"

	"google.golang.org/protobuf/proto"

	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// a player gets this long to carry out what arbitration asked of it
const externalWait = 5 * time.Second

// ExternalPlayer is the player of the sessions audiod does not render (a screen's video, airplay mirroring): it
// carries out a transport action on one of them (pause, resume, stop, seek, next, previous; reason is a token,
// displaced or gated, or empty for a command) and the controls of a DIRECT route. Neither may block for long: the
// action gets a context with a deadline, and the control must never block.
type ExternalPlayer interface {
	ExternalCommand(ctx context.Context, id, action string, positionMs uint64, reason string) error
	RouteControl(rc *audiodv1.RouteControl)
}

// External is a session another plane renders, as its player reports it. State is the session as the server takes
// it: its id is the session's, its source VIDEO, SCREENCAST or EXTERNAL, its route and controls the player's own.
// Metadata in it is memory-only (D11).
type External struct {
	State *cadencev2.MediaSessionState
}

var externalKinds = map[cadencev2.MediaSource]audiodv1.MediaSourceKind{
	cadencev2.MediaSource_MEDIA_SOURCE_VIDEO:      audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_VIDEO,
	cadencev2.MediaSource_MEDIA_SOURCE_SCREENCAST: audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_SCREENCAST,
	cadencev2.MediaSource_MEDIA_SOURCE_EXTERNAL:   audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_EXTERNAL,
}

// UseExternalPlayer has p carry out what audiod asks of the external sessions, and the transport of one that holds
// the speaker; set it before audiod's first Ready.
func (m *Media) UseExternalPlayer(p ExternalPlayer) {
	m.extMu.Lock()
	defer m.extMu.Unlock()
	m.extPlayer = p
}

// UpdateExternal registers an external session with audiod, or reports its new state: audiod arbitrates it with its
// own sources (the last to play takes the speaker; the rule 7 gate pauses it), and the server learns it whenever it
// holds the speaker. An idle state ends it. A follower's video (multiroom P4) is neither: its sound is the group's
// session, which it would displace were it registered, so audiod and the server never hear of it.
func (m *Media) UpdateExternal(e External) {
	s := e.State
	if s.GetSessionId() == "" || externalKinds[s.GetSource()] == audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_UNSPECIFIED ||
		s.GetFollow() {
		return
	}
	s = proto.CloneOf(s)
	m.extMu.Lock()
	idle := s.GetState() == cadencev2.PlaybackState_PLAYBACK_STATE_IDLE
	if idle {
		delete(m.externals, s.GetSessionId())
	} else {
		m.externals[s.GetSessionId()] = s
	}
	m.mu.Lock()
	send := m.send
	m.mu.Unlock()
	var current bool
	if send != nil {
		_ = send(externalEnvelope(s, idle))
		current = m.extCurrent == s.GetSessionId()
	} else {
		// no audiod: the newest that plays is the session, and one that waits is while no other is
		if s.GetState() == cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING || (m.extCurrent == "" && !idle) {
			m.extCurrent = s.GetSessionId()
		}
		// the session's own end is published before it stops being the session
		current = m.extCurrent == s.GetSessionId()
		if idle && current {
			m.extCurrent = ""
		}
	}
	m.extMu.Unlock()
	if current {
		if idle {
			m.publishSession(&cadencev2.MediaSessionState{State: cadencev2.PlaybackState_PLAYBACK_STATE_IDLE}, "", "")
			return
		}
		m.publishSession(s, "", "")
	}
}

// RemoveExternal ends an external session, as its player went (the screen disconnected).
func (m *Media) RemoveExternal(id string) {
	m.extMu.Lock()
	s, ok := m.externals[id]
	m.extMu.Unlock()
	if !ok {
		return
	}
	s = proto.CloneOf(s)
	s.State = cadencev2.PlaybackState_PLAYBACK_STATE_IDLE
	m.UpdateExternal(External{State: s})
}

// externalEnvelope is the session's ExternalSource for audiod
func externalEnvelope(s *cadencev2.MediaSessionState, unregister bool) *audiodv1.Envelope {
	es := &audiodv1.ExternalSource{Id: s.GetSessionId(), Op: audiodv1.ExternalOp_EXTERNAL_OP_UPDATE}
	if unregister {
		es.Op = audiodv1.ExternalOp_EXTERNAL_OP_UNREGISTER
	} else {
		es.Kind = externalKinds[s.GetSource()]
		es.State = audiodv1.PlaybackState(s.GetState())
		es.AudioRoute = audiodv1.AudioRoute(s.GetAudioRoute())
		// a group leader's epoch of the player's ring starts at its own position (multiroom P4)
		es.PositionMs, es.PositionAtMs = s.GetPositionMs(), s.GetPositionAtMs()
		for _, c := range s.GetAudioControls() {
			es.AudioControls = append(es.AudioControls, audiodv1.AudioControl(c))
		}
	}
	return &audiodv1.Envelope{Body: &audiodv1.Envelope_ExternalSource{ExternalSource: es}}
}

// sendExternalsTo registers every external session with a new audiod, which forgot them; callers hold setMu
func (m *Media) sendExternalsTo(send func(*audiodv1.Envelope) error) {
	m.extMu.Lock()
	defer m.extMu.Unlock()
	m.extCurrent = ""
	for _, id := range slices.Sorted(maps.Keys(m.externals)) {
		_ = send(externalEnvelope(m.externals[id], false))
	}
	if d := m.duck; d != nil && d.GetActive() {
		_ = send(duckEnvelope("", d))
	}
}

// external is the session audiod's state names, when it is an external one, with the player's details
func (m *Media) external(ms *audiodv1.MediaState) *cadencev2.MediaSessionState {
	if _, ok := externalKinds[sources[ms.GetSource()]]; !ok {
		return nil
	}
	// an audiod with external ids names the session by them; one before it used cadenced's id as its session id
	id := ms.GetExternalId()
	if id == "" {
		id = ms.GetSessionId()
	}
	m.extMu.Lock()
	defer m.extMu.Unlock()
	s, ok := m.externals[id]
	if !ok {
		return nil
	}
	m.extCurrent = id
	return proto.CloneOf(s)
}

// externalAction carries out what audiod's arbitration asks of an external session, off audiod's goroutine
func (m *Media) externalAction(a *audiodv1.ExternalAction) {
	name, ok := actionNames[a.GetAction()]
	m.extMu.Lock()
	p := m.extPlayer
	m.extMu.Unlock()
	if !ok || p == nil {
		return
	}
	m.log.Info("external_action", "session_id", a.GetId(), "action", name, "reason", reasonToken(a.GetReason()))
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), externalWait)
		defer cancel()
		if err := p.ExternalCommand(ctx, a.GetId(), name, a.GetPositionMs(), a.GetReason()); err != nil {
			m.log.Warn("external_action_failed", "session_id", a.GetId(), "action", name)
		}
	}()
}

func (m *Media) routeControl(rc *audiodv1.RouteControl) {
	m.extMu.Lock()
	p := m.extPlayer
	m.extMu.Unlock()
	if p != nil {
		p.RouteControl(rc)
	}
}

// currentExternal is the external session that holds the speaker, if any, and its player
func (m *Media) currentExternal() (string, ExternalPlayer) {
	m.extMu.Lock()
	defer m.extMu.Unlock()
	if m.extCurrent == "" || m.extPlayer == nil {
		return "", nil
	}
	return m.extCurrent, m.extPlayer
}

// ServerDuck lowers the device's own mix while a turn in its room listens or speaks (cadence.v2.Duck): audiod
// applies it in the mixer, or hands it to a DIRECT player. A duck never raises a level (rule 7): audiod clamps its
// level, and a level above 1 is taken as 1 here too. Without audiod a DIRECT external session gets it directly.
func (m *Media) ServerDuck(d *cadencev2.Duck) {
	d = proto.CloneOf(d)
	if d.GetLevel() > 1 || d.GetLevel() < 0 {
		d.Level = min(max(d.GetLevel(), 0), 1)
	}
	m.setMu.Lock()
	defer m.setMu.Unlock()
	m.extMu.Lock()
	if d.GetSessionId() == "" {
		m.duck = d
	}
	current := m.extCurrent
	var direct bool
	if s, ok := m.externals[current]; ok {
		direct = s.GetAudioRoute() == cadencev2.AudioRoute_AUDIO_ROUTE_DIRECT
	}
	p := m.extPlayer
	m.extMu.Unlock()
	m.mu.Lock()
	send := m.send
	m.mu.Unlock()
	m.log.Info("server_duck", "active", d.GetActive(), "reason", duckReason(d.GetReason()))
	if send != nil {
		_ = send(duckEnvelope("d-"+strconv.FormatUint(m.nextID.Add(1), 10), d))
		return
	}
	if direct && p != nil && (d.GetSessionId() == "" || d.GetSessionId() == current) {
		p.RouteControl(&audiodv1.RouteControl{SessionId: current, Control: &audiodv1.RouteControl_Duck{Duck: wireDuck(d)}})
	}
}

func wireDuck(d *cadencev2.Duck) *audiodv1.Duck {
	return &audiodv1.Duck{
		Active: d.GetActive(), Level: d.GetLevel(), RampMs: d.GetRampMs(), Reason: audiodv1.DuckReason(d.GetReason()),
	}
}

func duckEnvelope(id string, d *cadencev2.Duck) *audiodv1.Envelope {
	sc := &audiodv1.SessionControl{
		RequestId: id, SessionId: d.GetSessionId(), Control: &audiodv1.SessionControl_Duck{Duck: wireDuck(d)},
	}
	return &audiodv1.Envelope{Body: &audiodv1.Envelope_SessionControl{SessionControl: sc}}
}

func duckReason(r cadencev2.DuckReason) string {
	switch r {
	case cadencev2.DuckReason_DUCK_REASON_ASSISTANT:
		return "assistant"
	case cadencev2.DuckReason_DUCK_REASON_ANNOUNCEMENT:
		return "announcement"
	case cadencev2.DuckReason_DUCK_REASON_ALARM:
		return "alarm"
	default:
		return "unspecified"
	}
}

// reasonToken passes audiod's known reasons on, and nothing else it might send
func reasonToken(r string) string {
	switch r {
	case "displaced", "gated", "alarm":
		return r
	case "":
		return "command"
	default:
		return "unknown"
	}
}

// GateOpen is whether audiod holds the persisted level (rule 7): a VolumeApplied ok since its last Ready, and no
// failed one after it. Without audiod it is shut.
func (m *Media) GateOpen() bool { return m.gate.Load() }

// OnGate has f hear each change of GateOpen; set it before audiod's first Ready. f must never block.
func (m *Media) OnGate(f func(open bool)) {
	m.extMu.Lock()
	defer m.extMu.Unlock()
	m.onGate = f
}

func (m *Media) setGate(open bool) {
	if m.gate.Swap(open) == open {
		return
	}
	m.extMu.Lock()
	f := m.onGate
	m.extMu.Unlock()
	if f != nil {
		f(open)
	}
}

// MirrorListener hears airplay mirroring as audiod reports it (MirrorState, AirplayPin): the screen shows it. Both
// carry the sender's name, memory-only and never logged (D11). They must never block.
type MirrorListener interface {
	MirrorState(*audiodv1.MirrorState)
	AirplayPin(*audiodv1.AirplayPin)
}

// UseMirrorListener has l hear mirroring; set it before audiod's first Ready.
func (m *Media) UseMirrorListener(l MirrorListener) {
	m.extMu.Lock()
	defer m.extMu.Unlock()
	m.mirror = l
}

func (m *Media) mirrorEnvelope(env *audiodv1.Envelope) {
	m.extMu.Lock()
	l := m.mirror
	m.extMu.Unlock()
	if l == nil {
		return
	}
	switch b := env.GetBody().(type) {
	case *audiodv1.Envelope_MirrorState:
		l.MirrorState(b.MirrorState)
	case *audiodv1.Envelope_AirplayPin:
		l.AirplayPin(b.AirplayPin)
	case *audiodv1.Envelope_HlsCast:
		if h, ok := l.(HlsListener); ok {
			h.HlsCast(b.HlsCast)
		}
	}
}

// HlsListener is a MirrorListener that also plays airplay video casts (HlsCast: a url to play, scrub, rate, stop),
// as the screen's player does; it must never block, and the url is never logged.
type HlsListener interface {
	HlsCast(*audiodv1.HlsCast)
}

// HlsInfo tells audiod how the screen plays a cast, which it answers the sender's playback-info with; dropped while
// audiod is down (the next state sends it again).
func (m *Media) HlsInfo(info *audiodv1.HlsPlaybackInfo) {
	m.mu.Lock()
	send := m.send
	m.mu.Unlock()
	if send != nil {
		_ = send(&audiodv1.Envelope{Body: &audiodv1.Envelope_HlsPlaybackInfo{HlsPlaybackInfo: info}})
	}
}

// UseTap has audiod serve the screen's tap (pcm snapshots, bands, mirror frames) as cfg says, now and after every
// Ready; nil or an empty path serves none.
func (m *Media) UseTap(cfg *audiodv1.TapConfig) {
	m.setMu.Lock()
	defer m.setMu.Unlock()
	m.extMu.Lock()
	m.tap = proto.CloneOf(cfg)
	m.extMu.Unlock()
	m.mu.Lock()
	send := m.send
	m.mu.Unlock()
	if send != nil {
		m.sendTapTo(send)
	}
}

// callers hold setMu
func (m *Media) sendTapTo(send func(*audiodv1.Envelope) error) {
	m.extMu.Lock()
	tap := m.tap
	m.extMu.Unlock()
	if tap == nil {
		return
	}
	_ = send(&audiodv1.Envelope{Body: &audiodv1.Envelope_TapConfig{TapConfig: tap}})
}
