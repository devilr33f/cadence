package screen

import (
	"context"
	"time"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/media"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

const (
	codeInvalid     = "invalid"
	codeUnavailable = "unavailable"
	codeTimeout     = "screen_timeout"
)

// the codes a screen may answer with (CommandResult.code), and nothing else it might send
var screenCodes = map[string]bool{"unsupported": true, "invalid": true, "gated": true, "busy": true, "failed": true}

func commandCode(code string) string {
	if screenCodes[code] {
		return code
	}
	return "failed"
}

var errInvalid = &capability.CommandError{Code: codeInvalid}

// the screen's transport actions, as media.player and audiod name them
var screenActions = map[string]screenv1.MediaAction{
	"pause": screenv1.MediaAction_MEDIA_ACTION_PAUSE, "resume": screenv1.MediaAction_MEDIA_ACTION_RESUME,
	"stop": screenv1.MediaAction_MEDIA_ACTION_STOP, "seek": screenv1.MediaAction_MEDIA_ACTION_SEEK,
	"next": screenv1.MediaAction_MEDIA_ACTION_NEXT, "previous": screenv1.MediaAction_MEDIA_ACTION_PREVIOUS,
}

// ExternalCommand carries out a transport action on the screen's player: media.player's on a video session that holds
// the speaker, or what audiod's arbitration asks (a pause when another source took the speaker, or the gate shut).
// The player is one, and its command names no session: an action for a session a new play displaced (audiod asks it
// to pause as the new one takes the speaker) acts on nothing, or it would land on the player's next session.
func (s *Server) ExternalCommand(ctx context.Context, id, action string, positionMs uint64, reason string) error {
	a, ok := screenActions[action]
	if !ok {
		return capability.ErrUnsupported
	}
	_ = reason
	if !s.video.playing(id) {
		s.log.Info("screen_command_stale", "session_id", id, "action", action)
		return nil
	}
	return s.request(ctx, func(rid string) *screenv1.Envelope {
		return &screenv1.Envelope{Body: &screenv1.Envelope_MediaCommand{MediaCommand: &screenv1.MediaCommand{
			RequestId: rid, Action: a, PositionMs: positionMs,
		}}}
	})
}

// RouteControl hands a DIRECT route's control to the player, which applies it itself. A level is the persisted one
// (rule 7); a MIXER session never gets one, so mpv's own cubic volume never takes a cadence level.
func (s *Server) RouteControl(rc *audiodv1.RouteControl) {
	var env *screenv1.Envelope
	switch c := rc.GetControl().(type) {
	case *audiodv1.RouteControl_SetLevel:
		env = &screenv1.Envelope{Body: &screenv1.Envelope_SetLevel{SetLevel: &screenv1.SetLevel{
			Level: min(c.SetLevel.GetLevel(), 100), RampMs: c.SetLevel.GetRampMs(),
		}}}
	case *audiodv1.RouteControl_Mute:
		env = &screenv1.Envelope{Body: &screenv1.Envelope_Mute{Mute: &screenv1.Mute{Muted: c.Mute.GetMuted()}}}
	case *audiodv1.RouteControl_Duck:
		d := c.Duck
		env = &screenv1.Envelope{Body: &screenv1.Envelope_Duck{Duck: &cadencev2.Duck{
			Active: d.GetActive(), Level: min(max(d.GetLevel(), 0), 1), RampMs: d.GetRampMs(),
			Reason: cadencev2.DuckReason(d.GetReason()), SessionId: rc.GetSessionId(),
		}}}
	case *audiodv1.RouteControl_SetEq:
		eq := &screenv1.SetEq{Preset: c.SetEq.GetPreset(), PreampDb: c.SetEq.GetEq().GetPreampDb()}
		for _, b := range c.SetEq.GetEq().GetBands() {
			eq.Bands = append(eq.Bands, &screenv1.EqBand{
				Type: screenv1.EqType(b.GetType()), FreqHz: b.GetFreqHz(), GainDb: b.GetGainDb(), Q: b.GetQ(),
			})
		}
		env = &screenv1.Envelope{Body: &screenv1.Envelope_SetEq{SetEq: eq}}
	default:
		return
	}
	s.send(env)
}

// mediaState is the screen's player state: its session is one of audiod's external ones, and its tracks are
// media.video's state. ambient clips are never a session. ended is the session's last state whatever its state says,
// so it ends here too; gated needs nothing here, as audiod's arbitration knows the gate itself
func (s *Server) mediaState(ms *screenv1.MediaState) {
	// read before tracks forgets an ended session's play
	follow := s.video.following(ms.GetSessionId()) != ""
	s.video.tracks(ms)
	s.castState(ms)
	if ms.GetPurpose() == screenv1.MediaPurpose_MEDIA_PURPOSE_AMBIENT || ms.GetSessionId() == "" {
		return
	}
	if ms.GetEnded() && ms.GetState() != cadencev2.PlaybackState_PLAYBACK_STATE_IDLE {
		ms = proto.CloneOf(ms)
		ms.State = cadencev2.PlaybackState_PLAYBACK_STATE_IDLE
	}
	idle := ms.GetState() == cadencev2.PlaybackState_PLAYBACK_STATE_IDLE
	s.mu.Lock()
	if idle {
		delete(s.sessions, ms.GetSessionId())
		delete(s.served, ms.GetSessionId())
	} else {
		s.sessions[ms.GetSessionId()] = true
	}
	s.mu.Unlock()
	if s.o.Media == nil {
		return
	}
	st := &cadencev2.MediaSessionState{
		SessionId: ms.GetSessionId(), Source: cadencev2.MediaSource_MEDIA_SOURCE_VIDEO, State: ms.GetState(),
		PositionMs: ms.GetPositionMs(), PositionAtMs: ms.GetPositionAtMs(), Rate: ms.GetRate(),
		DurationMs: ms.GetDurationMs(), AudioRoute: ms.GetAudioRoute(), AudioControls: ms.GetAudioControls(),
		ControlLatencyMs: ms.GetControlLatencyMs(), Actions: []string{"pause", "resume", "stop", "seek"},
		Playable: cadencev2.PlayableKind_PLAYABLE_KIND_STREAM, BufferedMs: ms.GetBufferedMs(),
		PictureReady: ms.GetPictureReady(), ErrorCode: ms.GetErrorCode(),
		// a follower's picture: the group's session carries its sound and its transport (multiroom P4)
		Follow: follow,
	}
	if ms.BufferingPercent != nil {
		st.BufferingPercent = proto.Uint32(min(ms.GetBufferingPercent(), 100))
	}
	if md, actions := s.video.played(ms.GetSessionId()); md != nil {
		st.Metadata = md
		st.Actions = append(st.Actions, actions...)
	}
	s.o.Media.UpdateExternal(media.External{State: st})
}

// MirrorState shows airplay mirroring on the screen; the sender's name is never logged (D11)
func (s *Server) MirrorState(m *audiodv1.MirrorState) {
	var env *screenv1.Envelope
	s.mu.Lock()
	if m.GetActive() {
		s.mirror = &screenv1.MirrorStart{
			SessionId: m.GetSessionId(), Codec: screenv1.MirrorCodec(m.GetCodec()), Width: m.GetWidth(),
			Height: m.GetHeight(), ClientName: m.GetClientName(),
		}
		env = &screenv1.Envelope{Body: &screenv1.Envelope_MirrorStart{MirrorStart: s.mirror}}
	} else {
		s.mirror = nil
		env = &screenv1.Envelope{Body: &screenv1.Envelope_MirrorStop{MirrorStop: &screenv1.MirrorStop{
			SessionId: m.GetSessionId(), Reason: m.GetReason(),
		}}}
	}
	s.mu.Unlock()
	s.screencast.setMirroring(m.GetActive(), m.GetClientName())
	s.send(env)
}

// ScreencastReceiver is the board's airplay receiver's state (off, starting, ready, failed) and its code, for
// media.screencast's state
func (s *Server) ScreencastReceiver(state, code string) { s.screencast.setReceiver(state, code) }

// AirplayPin shows the pin a sender asks for; never logged
func (s *Server) AirplayPin(p *audiodv1.AirplayPin) {
	var expires int64
	if p.GetActive() {
		expires = time.Now().Add(time.Minute).UnixMilli()
	}
	s.send(&screenv1.Envelope{Body: &screenv1.Envelope_AirplayPin{AirplayPin: &screenv1.AirplayPin{
		Active: p.GetActive(), Pin: p.GetPin(), ClientName: p.GetClientName(), ExpiresAtMs: expires,
	}}})
}

// localMic is whether the screen's own microphone serves it: the owner's MicChoice, else whether one is attached;
// called with s.mu held
func (s *Server) localMic() bool {
	if !s.hello.GetLocalMic() {
		return false
	}
	switch s.mic.GetKind() {
	case cadencev2.MicSourceKind_MIC_SOURCE_KIND_SPEAKER, cadencev2.MicSourceKind_MIC_SOURCE_KIND_NONE:
		return false
	}
	return true
}

var keyKinds = map[screenv1.Key]input.Kind{
	screenv1.Key_KEY_VOLUME_UP: input.VolumeUp, screenv1.Key_KEY_VOLUME_DOWN: input.VolumeDown,
	screenv1.Key_KEY_ASSISTANT: input.Action,
}

// input takes a key the ui did not consume: input.remote's state learns its source, and with ForwardKeys a volume key
// or, on a screen with a microphone of its own, the assistant key acts on the board's input as a press. a screen
// without one asks the server itself (rpc assistant.invoke), which opens the room speaker's microphone
func (s *Server) input(e *screenv1.InputEvent) {
	s.remote.saw(e.GetSource())
	// keys the api injected never move the level (rule 7)
	if !s.o.ForwardKeys || e.GetSource() == screenv1.InputSource_INPUT_SOURCE_API {
		return
	}
	k, ok := keyKinds[e.GetKey()]
	if !ok {
		return
	}
	s.mu.Lock()
	localMic := s.localMic()
	held := s.assistantHeld
	if k == input.Action {
		switch e.GetAction() {
		case screenv1.KeyAction_KEY_ACTION_DOWN:
			s.assistantHeld = true
		case screenv1.KeyAction_KEY_ACTION_UP, screenv1.KeyAction_KEY_ACTION_PRESS:
			s.assistantHeld = false
		}
	}
	s.mu.Unlock()
	if k == input.Action && !localMic {
		return
	}
	var edges []bool
	switch e.GetAction() {
	case screenv1.KeyAction_KEY_ACTION_DOWN:
		edges = []bool{true}
	case screenv1.KeyAction_KEY_ACTION_UP:
		edges = []bool{false}
	case screenv1.KeyAction_KEY_ACTION_PRESS:
		edges = []bool{true, false}
	case screenv1.KeyAction_KEY_ACTION_LONG:
		// a long hold of a key already down acts on nothing more; the assistant's long with no down of its own (a
		// long BACK) is a press of it
		if k != input.Action || held {
			return
		}
		edges = []bool{true, false}
	default:
		return
	}
	for _, pressed := range edges {
		select {
		case s.buttons <- input.ButtonEvent{Kind: k, Pressed: pressed, At: time.Now()}:
		default:
			s.log.Warn("screen_key_dropped", "key", k.String())
		}
	}
}
