// Package media exposes audiod's media plane as the capabilities media.player,
// media.url, media.eq and media.viz, on top of the connection the audiod
// supervisor hands over after each Ready.
package media

import (
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// audiod answers a UrlPlay within 8 s; this is inside the 10 s a command gets
const commandWait = 9 * time.Second

// Media keeps the settings audiod forgets whenever its client goes, and
// correlates the commands sent to it with their results.
type Media struct {
	log            *slog.Logger
	commandTimeout time.Duration
	nextID         atomic.Uint64
	// the session's latest state is playing
	playing atomic.Bool
	// the applied viz style, read where envelopes arrive, which must never wait on setMu
	vizStyle atomic.Pointer[string]

	// held while the settings change and go out, so audiod gets them in order
	setMu    sync.Mutex
	duckMode string
	eq       eqConfig
	viz      vizConfig
	// audio.output's delay trim, once its config set one
	delay    int32
	delaySet bool

	mu      sync.Mutex
	send    func(*audiodv1.Envelope) error // the live connection's, nil while audiod is down
	pending map[string]chan string         // request id -> its code

	sessions chan *cadencev2.MediaSessionState
	frames   chan VizFrame

	// the latest session state, and its cover's audiod id, for an upload that finishes later
	stateMu      sync.Mutex
	latest       *cadencev2.MediaSessionState
	latestArt    string
	latestSender string
	art          *artwork
	srv          *serverCovers
	local        func(Local)
	watchers     []func(*cadencev2.MediaSessionState)

	// the sessions another plane renders, by id, the one among them that holds the speaker, and their player
	extMu      sync.Mutex
	externals  map[string]*cadencev2.MediaSessionState
	extCurrent string
	extPlayer  ExternalPlayer
	mirror     MirrorListener
	onGate     func(open bool)
	// the server's latest duck of the whole mix, sent again to a new audiod while active
	duck *cadencev2.Duck
	// the screen's tap, sent again to a new audiod
	tap *audiodv1.TapConfig
	// audiod holds the persisted level (rule 7)
	gate atomic.Bool

	player *player
	url    *urlPlayer
	eqCap  *equalizer
	vizCap *visualizer
}

func New(log *slog.Logger) *Media {
	m := &Media{
		log: log, commandTimeout: commandWait, duckMode: duckDefault, eq: eqConfig{Bands: []eqBand{}}, viz: vizDefault,
		pending: map[string]chan string{}, externals: map[string]*cadencev2.MediaSessionState{},
		sessions: make(chan *cadencev2.MediaSessionState, 1),
		frames:   make(chan VizFrame, 1),
	}
	style := vizDefault.Style
	m.vizStyle.Store(&style)
	m.player = &player{m: m, state: make(chan json.RawMessage, 1)}
	m.url = &urlPlayer{m: m}
	m.eqCap = &equalizer{m: m, state: make(chan json.RawMessage, 1)}
	m.vizCap = &visualizer{m: m, state: make(chan json.RawMessage, 1)}
	m.player.publish()
	m.eqCap.publish()
	m.vizCap.publish()
	return m
}

func (m *Media) Capabilities() []capability.Provider {
	return []capability.Provider{m.player, m.url, m.eqCap, m.vizCap}
}

// Sessions delivers the latest media session state, dropping older unread ones.
func (m *Media) Sessions() <-chan *cadencev2.MediaSessionState { return m.sessions }

// Playing is whether the media session's latest state is playing; it never waits.
func (m *Media) Playing() bool { return m.playing.Load() }

// Viz delivers the latest visualizer bands, 0-255 each, with the style to show them in, dropping older unread ones.
func (m *Media) Viz() <-chan VizFrame { return m.frames }

// MediaReady sends a new audiod connection every setting, right after its
// VolumeSet and DuckSet, and only then takes commands over it: a command
// meanwhile is answered unavailable rather than reach audiod before them.
func (m *Media) MediaReady(send func(*audiodv1.Envelope) error) {
	m.setMu.Lock()
	defer m.setMu.Unlock()
	m.sendSettingsTo(send)
	m.sendDelayTo(send)
	m.sendExternalsTo(send)
	m.sendTapTo(send)
	m.mu.Lock()
	m.send = send
	m.mu.Unlock()
}

// MediaEnvelope takes a MediaState, CommandResult, VizFrame, VolumeApplied, ExternalAction, RouteControl, MirrorState,
// AirplayPin or HlsCast from audiod; it never blocks.
func (m *Media) MediaEnvelope(env *audiodv1.Envelope) {
	switch b := env.GetBody().(type) {
	case *audiodv1.Envelope_CommandResult:
		m.answer(b.CommandResult)
	case *audiodv1.Envelope_MediaState:
		s := sessionState(b.MediaState)
		if ext := m.external(b.MediaState); ext != nil {
			// the player's own details, with audiod's word on whether it holds the speaker
			ext.State = s.GetState()
			s = ext
		} else {
			m.extMu.Lock()
			m.extCurrent = ""
			m.extMu.Unlock()
		}
		m.log.Info("media_state", "state", stateName(s.GetState()), "source", sourceName(s.GetSource()))
		m.publishSession(s, b.MediaState.GetMetadata().GetArtworkId(), b.MediaState.GetMetadata().GetSender())
	case *audiodv1.Envelope_ExternalAction:
		m.externalAction(b.ExternalAction)
	case *audiodv1.Envelope_RouteControl:
		m.routeControl(b.RouteControl)
	case *audiodv1.Envelope_VolumeApplied:
		if !b.VolumeApplied.GetTransient() {
			m.setGate(b.VolumeApplied.GetOk())
		}
	case *audiodv1.Envelope_MirrorState, *audiodv1.Envelope_AirplayPin, *audiodv1.Envelope_HlsCast:
		m.mirrorEnvelope(env)
	case *audiodv1.Envelope_VizFrame:
		var f VizFrame
		if len(b.VizFrame.GetBands()) == len(f.Bands) {
			copy(f.Bands[:], b.VizFrame.GetBands())
			f.Style = *m.vizStyle.Load()
			offer(m.frames, f)
		}
	}
}

// MediaGone follows the end of the connection MediaReady took: every pending
// command is answered unavailable, and there is no media session.
func (m *Media) MediaGone() {
	m.mu.Lock()
	m.send = nil
	pending := m.pending
	m.pending = map[string]chan string{}
	m.mu.Unlock()
	for _, ch := range pending {
		ch <- codeUnavailable
	}
	m.setGate(false)
	m.extMu.Lock()
	m.extCurrent = ""
	m.extMu.Unlock()
	m.playing.Store(false)
	m.stateMu.Lock()
	m.latest, m.latestArt, m.latestSender = nil, "", ""
	m.wantServerCover(nil, "")
	idle := &cadencev2.MediaSessionState{State: cadencev2.PlaybackState_PLAYBACK_STATE_IDLE}
	offer(m.sessions, idle)
	m.notifyLocal()
	m.notifyWatchers(idle)
	m.stateMu.Unlock()
}

// publishSession makes s the latest session state, with the cover audiod named for it, if any, and its sender's name
// (the local view's alone)
func (m *Media) publishSession(s *cadencev2.MediaSessionState, artworkID, sender string) {
	m.playing.Store(s.GetState() == cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING)
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	m.latest, m.latestArt, m.latestSender = s, artworkID, sender
	m.withArtwork(s, artworkID)
	m.wantServerCover(s, artworkID)
	offer(m.sessions, s)
	m.notifyLocal()
	m.notifyWatchers(s)
}

// WatchSession has f hear the session after every change (an idle one when there is none), the state the server
// gets; set it before audiod's first Ready. f must never block or keep s: it runs with the session's state held.
func (m *Media) WatchSession(f func(*cadencev2.MediaSessionState)) {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	m.watchers = append(m.watchers, f)
}

// callers hold stateMu
func (m *Media) notifyWatchers(s *cadencev2.MediaSessionState) {
	for _, f := range m.watchers {
		f(s)
	}
}

// update changes the settings and sends them all while audiod is up; a failed
// send means a connection on its way out, and the next Ready sends them again.
func (m *Media) update(change func()) {
	m.setMu.Lock()
	defer m.setMu.Unlock()
	change()
	m.mu.Lock()
	send := m.send
	m.mu.Unlock()
	if send != nil {
		m.sendSettingsTo(send)
	}
}

// callers hold setMu
func (m *Media) sendSettingsTo(send func(*audiodv1.Envelope) error) {
	s := &audiodv1.MediaSettings{DuckMode: duckModes[m.duckMode], Eq: m.eq.proto(), Viz: m.viz.proto()}
	_ = send(&audiodv1.Envelope{Body: &audiodv1.Envelope_MediaSettings{MediaSettings: s}})
}

// offer puts v in a channel of one that keeps the latest value; it never blocks.
func offer[T any](ch chan T, v T) {
	for {
		select {
		case ch <- v:
			return
		default:
		}
		select {
		case <-ch:
		default:
		}
	}
}
