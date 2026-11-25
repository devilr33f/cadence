// Package session runs the device side of a voice session: it streams the
// microphone to the server and plays the reply streams on the speaker. It also
// plays the server's announcements, which belong to no session.
package session

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/policy"
	"git.shino.boo/devilreef/cadence/device/internal/tracing"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

const (
	noSpeechWait = 8 * time.Second
	// the server's own bound on a follow-up window
	maxFollowUpWindow = 15 * time.Second
	inboxSize         = 256
)

// Controller runs one session at a time on a single goroutine, Run. One
// controller serves one link: the server binds a session to the link it began on.
type Controller struct {
	io    audio.IO
	send  func(*cadencev2.Envelope) error
	newID func() string
	log   *slog.Logger
	after func(time.Duration) <-chan time.Time

	// tails of sessions that closed normally, cut when the next session begins
	tail []audio.StreamWriter
	// the announcement playing, nil for none; sessions never touch it
	announce *announcement
	// where each session's trigger is kept, nil for nowhere
	recent *Recent
	// hears each change of phase, nil for none
	phases func(Phase)
	phase  Phase
	// whether an alarm or a timer rings, nil for never: no announcement plays over a ring
	ringing func() bool
	// whether the microphone switch is on, nil for never: no follow-up opens while it is
	muted func() bool
	// runs before each reply stream begins, nil for nothing
	beforeReply func()
	// where a command word reads what the sessions do, nil for nowhere
	activity *Activity
	// counts each session begun, nil for nothing
	counts Counts
	// tells the owner a session failed, nil for never
	errors *ErrorSounds

	in      chan *cadencev2.Envelope
	stopped chan struct{}
}

type Option func(*Controller)

// WithRecent records each session the controller begins in r.
func WithRecent(r *Recent) Option { return func(c *Controller) { c.recent = r } }

// WithRinging refuses each activation and each announcement that begins while ringing says an alarm or a timer rings;
// ringing must not block.
func WithRinging(ringing func() bool) Option { return func(c *Controller) { c.ringing = ringing } }

// WithMuted refuses a follow-up while muted says the microphone switch is on; muted must not block.
func WithMuted(muted func() bool) Option { return func(c *Controller) { c.muted = muted } }

// WithBeforeReply runs f on the controller's goroutine before each reply stream begins: core puts the level in force
// back on the hardware there (M6a-D8).
func WithBeforeReply(f func()) Option { return func(c *Controller) { c.beforeReply = f } }

// WithErrorSounds tells each failed session's owner through e (FailurePrompt), never during a ring or for test audio.
func WithErrorSounds(e *ErrorSounds) Option { return func(c *Controller) { c.errors = e } }

// WithActivity keeps what the sessions do in a, where a command word reads it.
func WithActivity(a *Activity) Option { return func(c *Controller) { c.activity = a } }

// Counts hears each session begun, by what began it, for the metrics; it must never block.
type Counts interface {
	SessionBegan(trigger cadencev2.Trigger)
}

// WithCounts counts each session begun in c.
func WithCounts(c Counts) Option { return func(ctl *Controller) { ctl.counts = c } }

func New(io audio.IO, send func(*cadencev2.Envelope) error, newID func() string, log *slog.Logger, opts ...Option) *Controller {
	c := &Controller{
		io: io, send: send, newID: newID, log: log, after: time.After,
		in: make(chan *cadencev2.Envelope, inboxSize), stopped: make(chan struct{}),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// announcement is an announce stream; ended once its StreamEnd came, while
// audiod may still play its tail.
type announcement struct {
	id      string
	w       audio.StreamWriter
	ended   bool
	packets int
}

type active struct {
	id       string
	stopMic  context.CancelFunc
	mic      <-chan []byte
	noSpeech <-chan time.Time
	// speech_started came: the no-speech deadline never arms again
	heard     bool
	seq       uint32
	packetsIn int
	packetOut int
	streams   map[string]audio.StreamWriter
	// writers whose StreamEnd came: audiod may still play their tail
	ended []audio.StreamWriter
	// test audio speaks in the session, as its trigger says
	injected bool
	// a follow-up, which silence for its window ends
	followUp bool
	// the reply frames the player took, and whether a reply stream, frame or end was lost: the reply was not heard whole
	replyFrames int
	replyLost   bool
	// the session's voice.turn span; nil-safe
	turn *tracing.Turn
}

// silence is why the session ends when nobody spoke in time
func (s *active) silence() string {
	if s.followUp {
		return "follow_up_silence"
	}
	return "no_speech"
}

// Handle takes a session envelope off the link's read path and reports whether
// it is one. It waits only for room in the inbox, never for audio or disk.
func (c *Controller) Handle(env *cadencev2.Envelope) bool {
	switch env.GetBody().(type) {
	case *cadencev2.Envelope_SessionEvent, *cadencev2.Envelope_StreamStart, *cadencev2.Envelope_AudioDown,
		*cadencev2.Envelope_StreamEnd, *cadencev2.Envelope_SessionClose:
	default:
		return false
	}
	select {
	case c.in <- env:
	case <-c.stopped:
	}
	return true
}

// Run serves triggers and server frames until ctx is done.
func (c *Controller) Run(ctx context.Context) {
	defer close(c.stopped)
	var s *active
	defer func() { c.end(s, "shutdown"); c.abortTail(); c.abortAnnounce("shutdown") }()
	for {
		var mic <-chan []byte
		var noSpeech <-chan time.Time
		if s != nil {
			mic, noSpeech = s.mic, s.noSpeech
		}
		select {
		case <-ctx.Done():
			return
		case t := <-c.io.Triggers:
			s = c.trigger(ctx, s, t)
		case env := <-c.in:
			s = c.handle(ctx, s, env)
		case p, ok := <-mic:
			if !ok {
				s.mic = nil
				if c.micFailed(mic) {
					s = c.cancel(s, "mic_failed")
					continue
				}
				// a silent session still ends on its deadline
				c.armNoSpeech(s)
				continue
			}
			c.armNoSpeech(s)
			s = c.sendMic(s, p)
		case <-noSpeech:
			s = c.cancel(s, s.silence())
		}
	}
}

// trigger acts on t as its mode says about the session that runs, s, which may be nil. While an alarm or a timer rings
// nothing begins: core refuses activations then, and one that was queued before the ring or raced it is refused here.
func (c *Controller) trigger(ctx context.Context, s *active, t audio.Trigger) *active {
	if t.Mode == audio.CommandEnd {
		if s != nil && s.id == t.Session {
			return c.cancel(s, "command_volume")
		}
		return s
	}
	if !t.Ends() && c.ringing != nil && c.ringing() {
		c.log.Info("activation_refused", "reason", "ringing", "trigger", t.Kind.String())
		return s
	}
	switch t.Mode {
	case audio.Toggle:
		if s != nil {
			return c.cancel(s, "local_cancel")
		}
	case audio.IfIdle:
		if s != nil {
			c.log.Info("wake_ignored", "session_id", s.id, "reason", "active")
			return s
		}
	case audio.Mute:
		if s != nil {
			return c.cancel(s, "muted")
		}
		c.abortTail()
		return nil
	case audio.Ring:
		c.abortAnnounce("ring")
		if s != nil {
			return c.cancel(s, "ring")
		}
		c.abortTail()
		return nil
	}
	c.end(s, "superseded")
	return c.begin(ctx, t, 0)
}

// begin opens a session for t. A follow-up, one with a window, keeps what still plays (the reply's tail, M4a-R7) and
// the music's duck, and ends once nobody spoke for its window.
func (c *Controller) begin(ctx context.Context, t audio.Trigger, window time.Duration) *active {
	follow := window > 0
	if !follow {
		c.abortTail()
	}
	id := t.Session
	if id == "" {
		id = c.newID()
	}
	s := &active{id: id, streams: map[string]audio.StreamWriter{}, injected: t.Test, followUp: follow}
	// before the server can name the session in a command
	if c.recent != nil {
		c.recent.Record(s.id, t.Kind)
	}
	// the turn's root span starts where the trigger did (the wake audiod heard), and the server's turn joins it
	s.turn = tracing.StartTurn(ctx, t.At, s.id, t.Kind, t.Wake)
	start := &cadencev2.SessionStart{SessionId: s.id, Trigger: t.Kind, Wake: t.Wake, Trace: s.turn.Proto(), AfterRing: t.AfterRing}
	if err := c.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_SessionStart{SessionStart: start}}); err != nil {
		s.turn.End("send_failed")
		c.log.Warn("session_end", "session_id", s.id, "code", "send_failed", "packets_in", 0, "packets_out", 0)
		if follow {
			c.duck(false)
			c.setPhase(PhaseIdle)
		} else {
			// the owner asked and the link is gone; a follow-up nobody asked for says nothing
			c.tellFailure(t.Test, PromptOffline)
		}
		return nil
	}
	if c.counts != nil {
		c.counts.SessionBegan(t.Kind)
	}
	c.activity.began(s.id, s.injected)
	c.log.Info("session_start", "session_id", s.id, "trigger", t.Kind.String())
	c.setPhase(PhaseListening)
	c.wakeEarcon(t)
	if !follow {
		c.duck(true)
	}
	s.turn.Event("session_start_sent")
	// the capture is the turn's child
	mctx, stop := context.WithCancel(s.turn.Context(ctx))
	mic, err := c.io.Mic.Open(mctx, t)
	if err != nil {
		stop()
		return c.cancel(s, "mic_failed")
	}
	s.mic, s.stopMic = mic, stop
	if follow {
		s.noSpeech = c.after(window)
	}
	return s
}

// followUp says whether the close of s opens a follow-up, and its window: the server allowed one, and the device's
// own facts hold (M6a-D5). A refusal of the server's leave is logged.
func (c *Controller) followUp(s *active, sc *cadencev2.SessionClose, code string) (time.Duration, bool) {
	f := policy.FollowUpFacts{
		Allowed: sc.GetFollowUpAllowed(), Window: min(time.Duration(sc.GetFollowUpWindowMs())*time.Millisecond, maxFollowUpWindow),
		Normal: code == "ok", Muted: c.muted != nil && c.muted(), Ringing: c.ringing != nil && c.ringing(),
		HeardWhole: s.replyFrames > 0 && !s.replyLost && len(s.streams) == 0,
	}
	open, reason := policy.FollowUp(f)
	if !open && reason != policy.NotAllowed {
		c.log.Info("follow_up_refused", "session_id", s.id, "reason", reason)
	}
	return f.Window, open
}

// armNoSpeech starts the no-speech deadline once the microphone hears: audiod may hold a session's first packet while
// it probes and starts the vendor engines, past the no-speech wait, and those seconds are no silence of the user's
func (c *Controller) armNoSpeech(s *active) {
	if s.noSpeech == nil && !s.heard {
		s.noSpeech = c.after(noSpeechWait)
	}
}

func (c *Controller) sendMic(s *active, p []byte) *active {
	m := &cadencev2.MicAudio{SessionId: s.id, Seq: s.seq, Opus: p}
	if err := c.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_MicAudio{MicAudio: m}}); err != nil {
		c.end(s, "send_failed")
		return nil
	}
	if s.seq == 0 {
		s.turn.Event("first_audio_sent")
	}
	s.seq++
	s.packetOut++
	return s
}

// cancel tells the server; it answers a cancel with nothing, so the session ends here.
func (c *Controller) cancel(s *active, reason string) *active {
	m := &cadencev2.SessionCancel{SessionId: s.id, Reason: reason}
	_ = c.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_SessionCancel{SessionCancel: m}})
	c.end(s, reason)
	return nil
}

func (c *Controller) handle(ctx context.Context, s *active, env *cadencev2.Envelope) *active {
	if c.announceFrame(env) {
		return s
	}
	if s == nil {
		if st := env.GetStreamStart(); st != nil {
			c.refuse(st, replyReason(st))
		}
		return nil
	}
	switch b := env.GetBody().(type) {
	case *cadencev2.Envelope_SessionEvent:
		if b.SessionEvent.GetSessionId() != s.id {
			return s
		}
		switch b.SessionEvent.GetKind() {
		case cadencev2.SessionEventKind_SESSION_EVENT_KIND_SPEECH_STARTED:
			s.turn.Event("speech_started")
			s.noSpeech, s.heard = nil, true
			c.setPhase(PhaseListening)
		case cadencev2.SessionEventKind_SESSION_EVENT_KIND_LISTENING:
			c.setPhase(PhaseListening)
		case cadencev2.SessionEventKind_SESSION_EVENT_KIND_THINKING:
			s.turn.Waiting()
			c.setPhase(PhaseThinking)
		}
	case *cadencev2.Envelope_SessionClose:
		sc := b.SessionClose
		if sc.GetSessionId() != s.id {
			return s
		}
		code := sc.GetCode()
		if code == "" {
			code = "ok"
		}
		if window, ok := c.followUp(s, sc, code); ok {
			c.finish(s, code)
			return c.begin(ctx, audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_FOLLOW_UP, Test: s.injected}, window)
		}
		c.end(s, code)
		return nil
	case *cadencev2.Envelope_StreamStart:
		st := b.StreamStart
		if st.GetSessionId() != s.id || st.GetPurpose() != cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY {
			c.refuse(st, replyReason(st))
			return s
		}
		if c.beforeReply != nil {
			c.beforeReply()
		}
		w, err := c.beginReply(s, st)
		if err != nil {
			s.replyLost = true
			c.refuse(st, "speaker")
			return s
		}
		s.streams[st.GetStreamId()] = w
		c.setPhase(PhaseSpeaking)
	case *cadencev2.Envelope_AudioDown:
		if w, ok := s.streams[b.AudioDown.GetStreamId()]; ok {
			s.packetsIn++
			if err := w.Write(b.AudioDown.GetSeq(), b.AudioDown.GetOpus()); err != nil {
				s.replyLost = true
				c.log.Warn("stream_write_failed", "session_id", s.id, "stream_id", b.AudioDown.GetStreamId())
				return s
			}
			s.replyFrames++
			c.activity.replyFrame(s.injected)
		}
	case *cadencev2.Envelope_StreamEnd:
		id := b.StreamEnd.GetStreamId()
		if w, ok := s.streams[id]; ok {
			delete(s.streams, id)
			s.ended = append(s.ended, w)
			c.endStream(s, id, w)
		}
	}
	return s
}

// announceFrame plays announce streams and reports whether env was one's. A
// newer announcement cuts the older, even one that only plays its tail.
// accepted: a wake right after a reply can cut an announcement queued behind
// the reply's tail, as audiod's StreamCancel cuts the streams queued after it.
func (c *Controller) announceFrame(env *cadencev2.Envelope) bool {
	a := c.announce
	switch b := env.GetBody().(type) {
	case *cadencev2.Envelope_StreamStart:
		st := b.StreamStart
		if st.GetPurpose() != cadencev2.StreamPurpose_STREAM_PURPOSE_ANNOUNCE {
			return false
		}
		if st.GetSessionId() != "" {
			c.refuse(st, "session")
			return true
		}
		if c.ringing != nil && c.ringing() {
			c.refuse(st, "ringing")
			return true
		}
		c.abortAnnounce("superseded")
		w, err := c.io.Speaker.Begin(st)
		if err != nil {
			c.refuse(st, "speaker")
			return true
		}
		c.announce = &announcement{id: st.GetStreamId(), w: w}
		c.log.Info("announce_start", "stream_id", st.GetStreamId())
	case *cadencev2.Envelope_AudioDown:
		if a == nil || a.ended || a.id != b.AudioDown.GetStreamId() {
			return false
		}
		a.packets++
		if err := a.w.Write(b.AudioDown.GetSeq(), b.AudioDown.GetOpus()); err != nil {
			c.log.Warn("stream_write_failed", "stream_id", a.id)
		}
	case *cadencev2.Envelope_StreamEnd:
		if a == nil || a.ended || a.id != b.StreamEnd.GetStreamId() {
			return false
		}
		a.ended = true
		if err := a.w.End(); err != nil {
			c.log.Warn("stream_end_failed", "stream_id", a.id)
		}
		c.log.Info("announce_end", "stream_id", a.id, "code", "ok", "packets_in", a.packets)
	default:
		return false
	}
	return true
}

// abortAnnounce cuts the announcement, its tail included.
func (c *Controller) abortAnnounce(code string) {
	a := c.announce
	if a == nil {
		return
	}
	c.announce = nil
	a.w.Abort()
	if !a.ended {
		c.log.Info("announce_end", "stream_id", a.id, "code", code, "packets_in", a.packets)
	}
}

func (c *Controller) refuse(st *cadencev2.StreamStart, reason string) {
	c.log.Warn("stream_refused", "stream_id", st.GetStreamId(), "reason", reason)
}

// replyReason says why a stream that is no announcement does not play.
func replyReason(st *cadencev2.StreamStart) string {
	if st.GetPurpose() != cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY {
		return "purpose"
	}
	return "session"
}

func (c *Controller) endStream(s *active, id string, w audio.StreamWriter) {
	if err := w.End(); err != nil {
		s.replyLost = true
		c.log.Warn("stream_end_failed", "session_id", s.id, "stream_id", id)
	}
}

// end stops the mic, cuts the open streams, lets the music come back and logs; s may be nil.
func (c *Controller) end(s *active, code string) {
	if s == nil {
		return
	}
	c.finish(s, code)
	c.duck(false)
	c.setPhase(PhaseIdle)
	// a follow-up whose microphone failed was never the owner's request
	if !s.followUp || code != "mic_failed" {
		c.tellFailure(s.injected, FailurePrompt(code))
	}
}

// tellFailure plays the failure's prompt after the session's audio was cut, unless test audio spoke or a ring sounds
func (c *Controller) tellFailure(test bool, prompt string) {
	if prompt == "" || test || (c.ringing != nil && c.ringing()) {
		return
	}
	c.errors.Play(prompt)
}

// finish is end without the music and the phase, which a follow-up keeps.
func (c *Controller) finish(s *active, code string) {
	if s.stopMic != nil {
		s.stopMic()
	}
	for _, w := range s.streams {
		w.Abort()
	}
	// a normal close lets the reply tail play, until the next session begins; so does the server's echo close, as a
	// follow-up that heard only the reply's echo cuts nothing of it (M4a-R7); so does a follow-up whose microphone never opened
	if code == "ok" || code == "follow_up_echo" || code == "mic_failed" {
		c.tail = append(c.tail, s.ended...)
	} else {
		for _, w := range s.ended {
			w.Abort()
		}
		c.abortTail()
	}
	c.activity.ended(s.id)
	s.turn.End(code, attribute.Int("cadence.packets_in", s.packetsIn), attribute.Int("cadence.packets_out", s.packetOut))
	c.log.Info("session_end", "session_id", s.id, "code", code, "packets_in", s.packetsIn, "packets_out", s.packetOut)
}

// duck lowers the music, on a speaker that plays any, while a session is open.
func (c *Controller) duck(active bool) {
	if d, ok := c.io.Speaker.(audio.Ducker); ok {
		d.Duck(active)
	}
}

// wakeEarcon tells the owner the speaker listens: at a wake word or a press, never a follow-up (the reply just
// spoke) or test audio
func (c *Controller) wakeEarcon(t audio.Trigger) {
	if t.Test || (t.Kind != cadencev2.Trigger_TRIGGER_WAKE && t.Kind != cadencev2.Trigger_TRIGGER_BUTTON) {
		return
	}
	if e, ok := c.io.Speaker.(audio.WakeEarconer); ok {
		e.WakeEarcon()
	}
}

func (c *Controller) micFailed(packets <-chan []byte) bool {
	r, ok := c.io.Mic.(audio.MicFailureReporter)
	return ok && r.MicFailure(packets)
}

func (c *Controller) abortTail() {
	for _, w := range c.tail {
		w.Abort()
	}
	c.tail = nil
}
