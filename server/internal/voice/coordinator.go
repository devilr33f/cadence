// Package voice runs one voice turn per device session: STT, the agent's
// turn, TTS and paced Opus back to the device. It also speaks announcements
// outside any session the same way. Transcripts, replies, announced text and
// provider names never reach a log.
package voice

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/audio"
	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

const (
	defaultChannels = 1
	defaultBitrate  = 64000
)

// Deps are the coordinator's collaborators. Frames go out on the Conn the
// session started on, so a reconnect never gets frames of an old session.
type Deps struct {
	Settings  func(ctx context.Context, deviceID string) (settings.Values, error)
	Providers interface {
		STT(ctx context.Context, name string) (providers.STT, error)
		LLM(ctx context.Context, name string) (providers.LLM, error)
		TTS(ctx context.Context, name string) (providers.TTS, error)
	}
	// Agent answers each final transcript; its speech is spoken as it comes.
	Agent interface {
		Run(ctx context.Context, t agent.Turn, out chan<- agent.Out) agent.Result
	}
	// RecordFailure stores a failed voice turn, including stt/setup failures
	// before the agent and a tts failure after its commit. Nil in isolated tests.
	RecordFailure func(ctx context.Context, deviceID, turnID string, meta conversation.TurnMeta, failure conversation.TurnFailure) error
	// Mode is the agent mode the device's next turn runs, false when it is not
	// known. Its time bounds the turn; it goes to the agent with the settings,
	// so a turn reads neither twice.
	Mode func(ctx context.Context, deviceID string, v settings.Values) (agent.Mode, bool)
	// EndSession tells the agent that a session of the device ended, however it ended.
	EndSession func(deviceID, sessionID string)
	// SessionEnded, when set, hears that a session of the device ended, with
	// the trigger that began it and whether a user turn ran in it (a final
	// transcript reached the agent), once it no longer counts as active.
	SessionEnded func(deviceID string, trigger cadencev2.Trigger, heard bool)
	// Transcript, when set, hears each new interim hypothesis of a session's
	// utterance as STT gives it, then its final: the screens showing the turn
	// draw it live. It must stay quick; the text is shown, never logged (rule 8).
	Transcript func(deviceID, sessionID, text string, final bool)
	// ResetConversation stores a voice reset turn and moves the device's context
	// boundary. Its sessionID becomes the reset turn id; it returns at once after
	// scheduling the closed segment's compaction.
	ResetConversation func(ctx context.Context, deviceID, sessionID, text, reply string) error
	// AnnounceEnded, when set, hears that an announcement of the device ended.
	AnnounceEnded func(deviceID string)
	NewEncoder    func(audio.EncoderOptions) (audio.Encoder, error)
	// Output gives the encoder's channels and bitrate for the device, and
	// whether it reported audio.output at all.
	Output func(ctx context.Context, deviceID string) (channels, bitrate int, reported bool)
	// MediaPlaying, when set, says whether the device's media session plays;
	// PauseMedia pauses it. A stop request pauses playing media first.
	MediaPlaying func(deviceID string) bool
	PauseMedia   func(ctx context.Context, deviceID string) error
	// Command runs a capability command on the device for a direct command
	// (audio.output's change_level, media.player's controls); nil leaves every
	// direct command to the agent.
	Command func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error)
	// HomeControl turns one home device on or off for a direct command. done is
	// false when the call did not land — no device matched the name, several
	// did, the household's policy refused, or Home Assistant failed — and the
	// agent answers those as before; nil leaves home commands to the agent.
	HomeControl func(ctx context.Context, call HomeCall) (entity string, done bool)
	// StoreCommand stores a direct command turn: its transcript, the fixed
	// reply it spoke ("" when it answered silently) and its trigger.
	StoreCommand func(ctx context.Context, deviceID, turnID, text, reply, trigger string) error
	Now          func() time.Time
	Log          *slog.Logger
}

type limits struct {
	micQueue  int
	sttOpen   time.Duration
	utterance time.Duration
	// turn bounds the whole session unless the agent's mode needs more:
	// its time plus slack to listen and speak (M3-R8)
	turn, slack time.Duration
	// an announcement answers once its first frame is out, or gives up after
	// announceWait; announceMax bounds its whole stream
	announceWait, announceMax time.Duration
	// pause bounds the media pause a stop request sends
	pause time.Duration
}

// spec §4.2: 1 s of mic audio queued while stt opens
var defaultLimits = limits{
	micQueue:     50,
	sttOpen:      5 * time.Second,
	utterance:    30 * time.Second,
	turn:         60 * time.Second,
	slack:        15 * time.Second,
	announceWait: 10 * time.Second,
	announceMax:  2 * time.Minute,
	pause:        2 * time.Second,
}

// Coordinator holds at most one session and one announcement per device.
type Coordinator struct {
	d    Deps
	lim  limits
	tick func() (<-chan time.Time, func())

	mu        sync.Mutex
	sessions  map[string]*session
	links     map[string]link
	announces map[string]*announcement
	// per device, in memory only: the follow-up the last close offered, as
	// the chain count of the session that offered it, and the guarded text the
	// last reply gave tts; both go with the next session start or the link
	offers map[string]int
	echoes map[string]string
}

// link is a device's current connection; ctx is done once it closes or is replaced.
type link struct {
	conn gateway.Conn
	ctx  context.Context
}

func New(d Deps) *Coordinator {
	return &Coordinator{
		d: d, lim: defaultLimits, tick: frameTicker,
		sessions: map[string]*session{}, links: map[string]link{}, announces: map[string]*announcement{},
		offers: map[string]int{}, echoes: map[string]string{},
	}
}

func frameTicker() (<-chan time.Time, func()) {
	t := time.NewTicker(frameDuration)
	return t.C, t.Stop
}

// Handle consumes session_start, mic_audio and session_cancel and reports
// whether env was one. It never blocks: a session runs on its own goroutine,
// and ctx (the link's) ends it when the link closes or is replaced.
func (c *Coordinator) Handle(ctx context.Context, conn gateway.Conn, env *cadencev2.Envelope) bool {
	switch b := env.GetBody().(type) {
	case *cadencev2.Envelope_SessionStart:
		c.start(ctx, conn, b.SessionStart)
	case *cadencev2.Envelope_MicAudio:
		c.mic(conn.DeviceID(), b.MicAudio)
	case *cadencev2.Envelope_SessionCancel:
		c.cancel(conn.DeviceID(), b.SessionCancel.GetSessionId())
	case *cadencev2.Envelope_WordHeard:
		c.wordHeard(ctx, conn.DeviceID(), b.WordHeard.GetWord())
	default:
		return false
	}
	return true
}

// wordHeard runs a home phrase the command spotter heard on an idle speaker:
// no session ever carried it, so the server is the only one that can act. It
// settles exactly what the direct table settles — the speaker's room's lights,
// silently, the action its own answer — and everything else it drops without a
// word: no model runs on a phrase nobody asked it.
func (c *Coordinator) wordHeard(ctx context.Context, deviceID, word string) {
	if c.d.HomeControl == nil {
		return
	}
	v, err := c.d.Settings(ctx, deviceID)
	if err != nil {
		c.d.Log.Error("word_heard_failed", "device_id", deviceID, "reason", "settings")
		return
	}
	cmd := matchDirect(word, v.Language)
	if cmd == nil || (cmd.kind != homeOn && cmd.kind != homeOff) {
		return
	}
	id := "word-" + rand.Text()
	_, done := c.d.HomeControl(ctx, HomeCall{
		DeviceID: deviceID, TurnID: id, Target: cmd.target,
		Trigger: cadencev2.Trigger_TRIGGER_WORD, On: cmd.kind == homeOn,
	})
	if !done {
		c.d.Log.Info("word_heard_refused", "device_id", deviceID, "kind", cmd.kind.String())
		return
	}
	if c.d.StoreCommand != nil {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err := c.d.StoreCommand(sctx, deviceID, id, word, "", "word")
		cancel()
		if err != nil {
			c.d.Log.Error("word_heard_store_failed", "device_id", deviceID)
		}
	}
	c.d.Log.Info("direct_command", "device_id", deviceID, "kind", cmd.kind.String(), "spoken", false, "heard", true)
}

// Disconnected ends the device's session and announcement without sending
// anything more, and forgets its link.
func (c *Coordinator) Disconnected(deviceID string) {
	c.mu.Lock()
	s := c.sessions[deviceID]
	an := c.announces[deviceID]
	delete(c.sessions, deviceID)
	delete(c.announces, deviceID)
	delete(c.links, deviceID)
	c.forgetTurns(deviceID)
	c.mu.Unlock()
	if s != nil {
		s.stop(reasonDisconnected)
	}
	if an != nil {
		an.stop(reasonDisconnected)
	}
}

// start supersedes the device's session, which sends nothing more once this returns.
func (c *Coordinator) start(ctx context.Context, conn gateway.Conn, st *cadencev2.SessionStart) {
	id, trigger := st.GetSessionId(), st.GetTrigger()
	if !deviceTrigger(trigger) {
		_ = conn.Send(&cadencev2.Envelope{Body: &cadencev2.Envelope_SessionClose{SessionClose: &cadencev2.SessionClose{
			SessionId: id, Code: "invalid_trigger",
		}}})
		return
	}
	s := newSession(ctx, c, conn, id, trigger)
	s.afterRing = afterRing(st)
	c.mu.Lock()
	old := c.sessions[s.dev]
	c.sessions[s.dev] = s
	s.takeTurns(c.offers, c.echoes)
	c.mu.Unlock()
	if old != nil {
		old.stop(reasonSuperseded)
	}
	go s.run()
}

// afterRing is the ring a wake stopped before the session began: only a wake stops one, and only an alarm or a timer
// is told to the agent
func afterRing(st *cadencev2.SessionStart) *agent.AfterRing {
	ar := st.GetAfterRing()
	if ar == nil || st.GetTrigger() != cadencev2.Trigger_TRIGGER_WAKE {
		return nil
	}
	return &agent.AfterRing{Kind: ar.GetKind(), ID: ar.GetId(), Stopped: time.Duration(ar.GetStoppedMsAgo()) * time.Millisecond}
}

// mic drops audio of a superseded or unknown session, so it never reaches the current stt.
func (c *Coordinator) mic(deviceID string, m *cadencev2.MicAudio) {
	c.mu.Lock()
	s := c.sessions[deviceID]
	c.mu.Unlock()
	if s != nil && s.id == m.GetSessionId() {
		s.offer(m.GetOpus())
	}
}

// cancel ends the session it names; a cancel for another one is stale.
func (c *Coordinator) cancel(deviceID, id string) {
	c.mu.Lock()
	s := c.sessions[deviceID]
	if s == nil || s.id != id {
		c.mu.Unlock()
		return
	}
	delete(c.sessions, deviceID)
	c.mu.Unlock()
	s.stop(reasonCancelled)
}

// Stop ends the device's session the server's caller names (sessionID; "" is whichever runs) as the device's own
// stop would: the device hears SessionClose{stopped}, with no follow-up, and cuts the reply it plays. false: no such
// session runs (it ended, or a newer one replaced it).
func (c *Coordinator) Stop(deviceID, sessionID string) bool {
	c.mu.Lock()
	s := c.sessions[deviceID]
	if s == nil || sessionID != "" && s.id != sessionID {
		c.mu.Unlock()
		return false
	}
	delete(c.sessions, deviceID)
	// the follow-up the session would offer is not offered
	delete(c.offers, deviceID)
	c.mu.Unlock()
	return s.stopByServer()
}

// SessionActive reports whether a voice session of the device runs.
func (c *Coordinator) SessionActive(deviceID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions[deviceID] != nil
}

// Announcing reports whether an announcement of the device is under way.
func (c *Coordinator) Announcing(deviceID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.announces[deviceID] != nil
}

// forgetTurns drops what the device's last turn left for the next; under c.mu.
func (c *Coordinator) forgetTurns(deviceID string) {
	delete(c.offers, deviceID)
	delete(c.echoes, deviceID)
}

func (c *Coordinator) forget(s *session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessions[s.dev] == s {
		delete(c.sessions, s.dev)
	}
}

// OutputFrom reads the encoder's channels and bitrate from the device's
// audio.output state, else 1 channel at 64 kbps; reported is whether the
// device has that state.
func OutputFrom(state func(ctx context.Context, deviceID, capability string) (json.RawMessage, bool)) func(ctx context.Context, deviceID string) (int, int, bool) {
	return func(ctx context.Context, deviceID string) (int, int, bool) {
		channels, bitrate := defaultChannels, defaultBitrate
		raw, ok := state(ctx, deviceID, "audio.output")
		var st struct {
			Channels   int `json:"channels"`
			MaxBitrate int `json:"maxBitrate"`
		}
		if !ok || json.Unmarshal(raw, &st) != nil {
			return channels, bitrate, ok
		}
		if st.Channels == 1 || st.Channels == 2 {
			channels = st.Channels
		}
		if st.MaxBitrate >= audio.MinBitrate && st.MaxBitrate <= audio.MaxBitrate {
			bitrate = st.MaxBitrate
		}
		return channels, bitrate, true
	}
}

// deviceTrigger is a way a device may begin a session; EVENT turns start on the server.
func deviceTrigger(t cadencev2.Trigger) bool {
	switch t {
	case cadencev2.Trigger_TRIGGER_WAKE, cadencev2.Trigger_TRIGGER_BUTTON, cadencev2.Trigger_TRIGGER_FOLLOW_UP:
		return true
	}
	return false
}
