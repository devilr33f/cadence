// Package screentest is a fake screen plane (cadence-screen) in Go: it connects to cadenced's screen socket, says
// Hello, answers what cadenced asks the way a screen would and records every frame, for the screen package's tests,
// the sim-tv board and the e2e. It plays nothing: its player only reports states (rule 7: a play waits paused for the
// gate).
package screentest

import (
	"context"
	"errors"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/screen"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// Options say how the fake screen behaves.
type Options struct {
	// what it says first; nil is a 1080p linux screen with h264 and hevc
	Hello *screenv1.Hello
	// the route its player reports, DIRECT by default (no ao_cadence ring yet)
	Route cadencev2.AudioRoute
	// the code it answers every request with; empty is ok
	Code string
}

// Screen is one fake screen's connection.
type Screen struct {
	o    Options
	conn screen.FrameConn

	mu      sync.Mutex
	frames  []*screenv1.Envelope
	changed chan struct{}
	gate    bool
	// the session its player has, and its state
	session string
	state   cadencev2.PlaybackState
	done    chan struct{}
}

// DefaultHello is a 1080p linux screen with h264 and hevc and no microphone.
func DefaultHello() *screenv1.Hello {
	return &screenv1.Hello{
		Protocol: 1, Version: "fake", Platform: screenv1.Platform_PLATFORM_LINUX,
		Display:     &screenv1.Display{Width: 1920, Height: 1080, RefreshMhz: 60000},
		VideoCodecs: []string{"h264", "hevc"}, MaxVideoHeight: 1080, CardVersions: []uint32{1},
		CardFeatures: []string{"card", "text", "toggle"},
	}
}

// Connect dials cadenced's screen socket at path, retrying until ctx ends, and says Hello.
func Connect(ctx context.Context, path string, o Options) (*Screen, error) {
	if o.Hello == nil {
		o.Hello = DefaultHello()
	}
	if o.Route == cadencev2.AudioRoute_AUDIO_ROUTE_UNSPECIFIED {
		o.Route = cadencev2.AudioRoute_AUDIO_ROUTE_DIRECT
	}
	for {
		c, err := screen.Dial(ctx, path)
		if err == nil {
			s := &Screen{o: o, conn: c, changed: make(chan struct{}), done: make(chan struct{})}
			if err := s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_Hello{Hello: o.Hello}}); err != nil {
				_ = c.Close()
				return nil, err
			}
			go s.read()
			return s, nil
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Close hangs up.
func (s *Screen) Close() { _ = s.conn.Close() }

// Done closes once cadenced hung up.
func (s *Screen) Done() <-chan struct{} { return s.done }

// Send sends one frame to cadenced.
func (s *Screen) Send(env *screenv1.Envelope) error {
	b, err := proto.Marshal(env)
	if err != nil {
		return err
	}
	return s.conn.Write(b)
}

// Key presses a key on the remote.
func (s *Screen) Key(k screenv1.Key) error {
	return s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_InputEvent{InputEvent: &screenv1.InputEvent{
		Key: k, Action: screenv1.KeyAction_KEY_ACTION_PRESS, Source: screenv1.InputSource_INPUT_SOURCE_REMOTE,
		AtMs: time.Now().UnixMilli(),
	}}})
}

// Frames are all cadenced sent so far.
func (s *Screen) Frames() []*screenv1.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*screenv1.Envelope(nil), s.frames...)
}

var errTimeout = errors.New("screentest: no such frame")

// Wait returns the first frame since the start that match takes, waiting up to d.
func (s *Screen) Wait(d time.Duration, match func(*screenv1.Envelope) bool) (*screenv1.Envelope, error) {
	deadline := time.After(d)
	seen := 0
	for {
		s.mu.Lock()
		frames, changed := s.frames, s.changed
		s.mu.Unlock()
		for ; seen < len(frames); seen++ {
			if match(frames[seen]) {
				return frames[seen], nil
			}
		}
		select {
		case <-changed:
		case <-deadline:
			return nil, errTimeout
		}
	}
}

// State is its player's session and state.
func (s *Screen) State() (string, cadencev2.PlaybackState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.session, s.state
}

func (s *Screen) read() {
	defer close(s.done)
	for {
		b, err := s.conn.Read()
		if err != nil {
			return
		}
		env := &screenv1.Envelope{}
		if proto.Unmarshal(b, env) != nil {
			return
		}
		// acted on before it is published, so a test that waited for the frame sees the state it set
		s.act(env)
		s.mu.Lock()
		s.frames = append(s.frames, env)
		close(s.changed)
		s.changed = make(chan struct{})
		s.mu.Unlock()
	}
}

// act answers as a screen would: a request gets its result, a play becomes the player's session (paused while the
// gate is shut), a command moves its state
func (s *Screen) act(env *screenv1.Envelope) {
	switch b := env.GetBody().(type) {
	case *screenv1.Envelope_PlaybackGate:
		s.mu.Lock()
		s.gate = b.PlaybackGate.GetOpen()
		s.mu.Unlock()
	case *screenv1.Envelope_MediaPlay:
		s.result(b.MediaPlay.GetRequestId())
		if s.o.Code == "" {
			s.mu.Lock()
			state := cadencev2.PlaybackState_PLAYBACK_STATE_PAUSED
			if s.gate {
				state = cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING
			}
			s.mu.Unlock()
			s.report(b.MediaPlay.GetSessionId(), state)
		}
	case *screenv1.Envelope_MediaCommand:
		s.result(b.MediaCommand.GetRequestId())
		s.mu.Lock()
		session := s.session
		s.mu.Unlock()
		if s.o.Code != "" || session == "" {
			return
		}
		switch b.MediaCommand.GetAction() {
		case screenv1.MediaAction_MEDIA_ACTION_PAUSE:
			s.report(session, cadencev2.PlaybackState_PLAYBACK_STATE_PAUSED)
		case screenv1.MediaAction_MEDIA_ACTION_RESUME:
			s.report(session, cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING)
		case screenv1.MediaAction_MEDIA_ACTION_STOP:
			s.report(session, cadencev2.PlaybackState_PLAYBACK_STATE_IDLE)
		default:
		}
	case *screenv1.Envelope_DisplayPower:
		s.result(b.DisplayPower.GetRequestId())
	case *screenv1.Envelope_SetLevel:
		s.result(b.SetLevel.GetRequestId())
	case *screenv1.Envelope_Route:
		s.result(b.Route.GetRequestId())
	}
}

func (s *Screen) result(id string) {
	_ = s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_CommandResult{CommandResult: &screenv1.CommandResult{
		RequestId: id, Code: s.o.Code,
	}}})
}

// Report has its player report session in state, as mpv's would.
func (s *Screen) Report(session string, state cadencev2.PlaybackState) { s.report(session, state) }

// End has its player report session's last state, ended in state (mpv reports IDLE; anything else tests that the
// marker alone ends it), then no session.
func (s *Screen) End(session string, state cadencev2.PlaybackState) {
	s.mu.Lock()
	s.session, s.state = "", cadencev2.PlaybackState_PLAYBACK_STATE_IDLE
	s.mu.Unlock()
	_ = s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_MediaState{MediaState: &screenv1.MediaState{
		SessionId: session, State: state, PositionAtMs: time.Now().UnixMilli(), AudioRoute: s.o.Route,
		Purpose: screenv1.MediaPurpose_MEDIA_PURPOSE_CONTENT, Ended: true,
	}}})
	_ = s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_MediaState{MediaState: &screenv1.MediaState{
		State: cadencev2.PlaybackState_PLAYBACK_STATE_IDLE,
	}}})
}

func (s *Screen) report(session string, state cadencev2.PlaybackState) {
	s.mu.Lock()
	s.session, s.state = session, state
	if state == cadencev2.PlaybackState_PLAYBACK_STATE_IDLE {
		s.session = ""
	}
	gated := !s.gate && state == cadencev2.PlaybackState_PLAYBACK_STATE_PAUSED
	s.mu.Unlock()
	rate := float32(0)
	if state == cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING {
		rate = 1
	}
	_ = s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_MediaState{MediaState: &screenv1.MediaState{
		SessionId: session, State: state, PositionAtMs: time.Now().UnixMilli(), Rate: rate, DurationMs: 60000,
		AudioRoute: s.o.Route, Purpose: screenv1.MediaPurpose_MEDIA_PURPOSE_CONTENT, Gated: gated,
		Ended: state == cadencev2.PlaybackState_PLAYBACK_STATE_IDLE,
		AudioControls: []cadencev2.AudioControl{cadencev2.AudioControl_AUDIO_CONTROL_VOLUME,
			cadencev2.AudioControl_AUDIO_CONTROL_DUCK, cadencev2.AudioControl_AUDIO_CONTROL_MUTE},
		Tracks: []*cadencev2.MediaTrack{{Id: "a1", Kind: cadencev2.MediaTrackKind_MEDIA_TRACK_KIND_AUDIO, Selected: true}},
	}}})
}
