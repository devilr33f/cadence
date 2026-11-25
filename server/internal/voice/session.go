package voice

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/trace"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/audio"
	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

const (
	sttSampleRate  = 16000
	ttsSampleRate  = 24000
	opusComplexity = 10
	frameDuration  = 20 * time.Millisecond
)

// close codes besides the providers' own
const (
	codeNotConfigured = "not_configured"
	codeTimeout       = "timeout"
	codeInternal      = "internal"
)

// why a session ended without a SessionClose; they only reach the turn log
const (
	reasonSuperseded   = "superseded"
	reasonCancelled    = "cancelled"
	reasonDisconnected = "disconnected"
)

var (
	errTurnTimeout = errors.New("voice: turn deadline")
	errEnded       = errors.New("voice: session ended")
	errNotSent     = errors.New("voice: frame not sent")
	errPanic       = errors.New("voice: helper panic")
)

type session struct {
	c       *Coordinator
	id, dev string
	trigger cadencev2.Trigger
	// the ring the wake that began it stopped, nil for none
	afterRing *agent.AfterRing
	conn      gateway.Conn
	// span is voice.session; ctx and base carry it, so stt, the agent and tts join it
	span trace.Span
	down *meter // the reply's opus to the device, on span
	// cancelled with the cause that ended the session; the turn deadline is its own cause
	ctx     context.Context
	base    context.Context // ctx without the deadline
	cancel  context.CancelCauseFunc
	release context.CancelFunc
	mic     chan []byte
	lastMic atomic.Pointer[time.Time]
	// what the device's last turn left for this one, taken at its start (M6a-D5)
	chain       int    // follow-ups in a row before this session
	unsolicited bool   // a follow-up no close offered
	echoOf      string // the guarded text the previous reply gave tts, dropped at this session's end

	// owned by the run goroutine
	started, heard, final, firstAudio time.Time
	opened, utterUntil                time.Time // by the wall clock
	budget                            time.Duration
	budgetKnown                       bool
	spoke                             bool
	channels                          int
	streamID                          string
	seq                               uint32
	followUp                          settings.FollowUp
	spoken                            string // the guarded text this reply gave tts
	// values and mode were resolved at prepare; failure is the agent's detail
	// unless voice replaces it with a later tts failure
	values  settings.Values
	mode    string
	failure *conversation.TurnFailure
	// asked: a final transcript reached the agent; stopped and echoed: one
	// that asked to stop, or was the previous reply's echo, ended the turn;
	// commanded: a direct command answered it, which ends the whole session
	asked, stopped, echoed, commanded bool

	mu     sync.Mutex
	ended  bool
	reason string
	// cause is the failing stage's detail as a token (closed_1011, asr_error_3), the first one noted
	cause string
}

func newSession(parent context.Context, c *Coordinator, conn gateway.Conn, id string, trigger cadencev2.Trigger) *session {
	parent, span := startSessionSpan(parent, conn.DeviceID(), id, trigger.String())
	base, cancel := context.WithCancelCause(parent)
	opened := time.Now()
	ctx, release := context.WithDeadlineCause(base, opened.Add(c.lim.turn), errTurnTimeout)
	return &session{
		c: c, id: id, dev: conn.DeviceID(), trigger: trigger, conn: conn, span: span,
		ctx: ctx, base: base, cancel: cancel, release: release,
		mic: make(chan []byte, c.lim.micQueue), started: c.d.Now(), opened: opened, utterUntil: opened.Add(c.lim.utterance),
	}
}

// extend moves the turn deadline out to start plus the agent mode's time,
// the room a holding tool call may take beyond it (agent.MaxHold: a play
// waiting for its outcome) and slack, never in (M3-R8). The context it replaces stays open
// until the session ends, so a stream begun under it ends as it would have.
// It runs on the run goroutine while no helper reads s.ctx.
func (s *session) extend(start time.Time) {
	dl, _ := s.ctx.Deadline()
	next := start.Add(s.budget + agent.MaxHold + s.c.lim.slack)
	if !s.budgetKnown || !next.After(dl) || s.ctx.Err() != nil {
		return
	}
	ctx, release := context.WithDeadlineCause(s.base, next, errTurnTimeout)
	old := s.release
	s.ctx, s.release = ctx, func() { release(); old() }
}

// offer queues a mic packet; past the queue (stt still opening) it is dropped.
func (s *session) offer(opus []byte) {
	now := s.c.d.Now()
	s.lastMic.Store(&now)
	select {
	case s.mic <- opus:
	default:
	}
}

// stop ends the session from outside; once it returns, no frame of it goes out.
func (s *session) stop(reason string) {
	s.mu.Lock()
	if !s.ended {
		s.ended, s.reason = true, reason
	}
	s.mu.Unlock()
	s.cancel(errEnded)
}

// stopByServer ends the session as stop does, but the device hears it: SessionClose with codeStopped, which cuts what
// it plays. false when it had ended already.
func (s *session) stopByServer() bool {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return false
	}
	s.ended, s.reason = true, codeStopped
	_ = s.conn.Send(&cadencev2.Envelope{Body: &cadencev2.Envelope_SessionClose{SessionClose: &cadencev2.SessionClose{
		SessionId: s.id, Code: codeStopped,
	}}})
	s.mu.Unlock()
	s.cancel(errEnded)
	return true
}

// send fails for an ended session; a failed send ends the session with its cause.
func (s *session) send(env *cadencev2.Envelope) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return false
	}
	if err := s.conn.Send(env); err != nil {
		s.cancel(err)
		return false
	}
	return true
}

func (s *session) event(k cadencev2.SessionEventKind) {
	s.span.AddEvent(eventName(k))
	s.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_SessionEvent{SessionEvent: &cadencev2.SessionEvent{
		SessionId: s.id, Kind: k,
	}}})
}

func (s *session) speechStarted() {
	if !s.spoke {
		s.spoke = true
		s.event(cadencev2.SessionEventKind_SESSION_EVENT_KIND_SPEECH_STARTED)
	}
}

// orCtx prefers the session's own end over a provider error it caused.
func (s *session) orCtx(code string) string {
	if s.ctx.Err() != nil {
		return s.ctxCode()
	}
	return code
}

func (s *session) ctxCode() string {
	cause := context.Cause(s.ctx)
	if errors.Is(cause, errTurnTimeout) || errors.Is(cause, gateway.ErrBackpressure) {
		return codeTimeout
	}
	if errors.Is(cause, errPanic) {
		return codeInternal
	}
	return reasonDisconnected
}

// goSafe runs a turn helper on its own goroutine. A panic ends the turn as internal; the value can
// carry anything, so only the ids and the part are logged. then runs last, after the turn is
// cancelled, so a waiter it releases already sees the cause.
func (s *session) goSafe(name string, fn func(), then ...func()) {
	goRecover(func() {
		s.c.d.Log.Error("voice_panic", "device_id", s.dev, "session_id", s.id, "part", name)
		s.cancel(errPanic)
	}, fn, then...)
}

// goRecover runs fn on its own goroutine; a panic runs onPanic instead of
// ending the server, and then runs last, after either.
func goRecover(onPanic, fn func(), then ...func()) {
	go func() {
		for _, f := range then {
			defer f()
		}
		defer func() {
			if recover() != nil {
				onPanic()
			}
		}()
		fn()
	}()
}

func (s *session) run() {
	defer func() {
		// the value can carry anything, so only the ids are logged
		if recover() != nil {
			s.c.d.Log.Error("voice_panic", "device_id", s.dev, "session_id", s.id)
			s.finish(codeInternal)
		}
	}()
	s.finish(s.turn())
}

func (s *session) turn() string {
	p, code := s.prepare()
	if code != "" {
		return code
	}
	text, code := s.listen(p)
	if code != "" {
		return code
	}
	// echo first: the speaker's own words never pause the music
	if s.trigger == cadencev2.Trigger_TRIGGER_FOLLOW_UP && s.echoOf != "" && isEcho(words(text), words(s.echoOf)) {
		s.echoed = true
		return codeFollowUpEcho
	}
	if isStopRequest(text, p.language) {
		return s.stopRequested()
	}
	if isResetRequest(text, p.language) {
		return s.resetRequested(p, text)
	}
	if c := matchDirect(text, p.language); c != nil {
		if code, handled := s.directCommand(p, c, text); handled {
			return code
		}
	}
	s.asked = true
	s.speechStarted()
	s.event(cadencev2.SessionEventKind_SESSION_EVENT_KIND_THINKING)
	return s.reply(p, text)
}

// finish closes the stream and the session unless something ended it first,
// then logs the turn.
func (s *session) finish(code string) {
	s.failure = s.failureFor(code)
	code = s.close(code)
	s.echoOf, s.spoken = "", ""
	s.cancel(errEnded)
	s.release()
	s.recordFailure(code)
	s.c.forget(s)
	// under after_session this ends a mode the session set by voice
	s.c.d.EndSession(s.dev, s.id)
	if s.c.d.SessionEnded != nil {
		s.c.d.SessionEnded(s.dev, s.trigger, s.asked)
	}
	s.logTurn(code)
	if s.down != nil {
		s.down.flush()
	}
	s.endSessionSpan(code)
}

// close returns the code the session ended with. The unlock is deferred: a
// panic in a send must not leave stop, which the link's reader calls, waiting.
func (s *session) close(code string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return s.reason
	}
	s.ended = true
	if code == reasonDisconnected {
		return code
	}
	allowed, window := s.leave(code)
	if s.streamID != "" {
		_ = s.conn.Send(&cadencev2.Envelope{Body: &cadencev2.Envelope_StreamEnd{StreamEnd: &cadencev2.StreamEnd{
			StreamId: s.streamID,
		}}})
	}
	_ = s.conn.Send(&cadencev2.Envelope{Body: &cadencev2.Envelope_SessionClose{SessionClose: &cadencev2.SessionClose{
		SessionId: s.id, Code: code, FollowUpAllowed: allowed, FollowUpWindowMs: window, Failure: failureProto(s.failure),
	}}})
	return code
}

func (s *session) logTurn(code string) {
	if code == "" {
		code = "ok"
	}
	attrs := []any{"device_id", s.dev, "session_id", s.id, "trigger", s.trigger.String()}
	if !s.final.IsZero() && !s.heard.IsZero() {
		attrs = append(attrs, "endpointMs", s.final.Sub(s.heard).Milliseconds())
	}
	if !s.final.IsZero() && !s.firstAudio.IsZero() {
		attrs = append(attrs, "firstAudioMs", s.firstAudio.Sub(s.final).Milliseconds())
	}
	attrs = append(attrs, "totalMs", s.c.d.Now().Sub(s.started).Milliseconds(), "code", code)
	s.c.d.Log.Info("turn", attrs...)
}

type plan struct {
	stt             providers.STT
	tts             providers.TTS
	enc             audio.Encoder
	channels        int
	voice, language string
	// read once for the turn and handed to the agent
	values settings.Values
	mode   *agent.Mode
}

func (p plan) ttsOptions() providers.TTSOptions {
	return providers.TTSOptions{Voice: p.voice, SampleRate: ttsSampleRate, Language: p.language}
}

// prepare resolves everything the turn needs before the device is listened to.
func (s *session) prepare() (plan, string) {
	d := s.c.d
	v, err := d.Settings(s.ctx, s.dev)
	if err != nil {
		if s.ctx.Err() == nil {
			d.Log.Error("voice_settings_failed", "device_id", s.dev)
		}
		return plan{}, s.orCtx(providers.CodeSTTUnavailable)
	}
	s.followUp = v.FollowUp
	s.values = v
	if v.STT == "" || v.TTS == "" || v.LLM == "" {
		return plan{}, codeNotConfigured
	}
	mode, known := d.Mode(s.ctx, s.dev, v)
	s.budget, s.budgetKnown = mode.MaxDuration, known
	s.extend(s.opened)
	p := plan{voice: v.Voice, language: v.Language, values: v}
	if known {
		p.mode = &mode
		s.mode = mode.Name
	}
	if p.stt, err = d.Providers.STT(s.ctx, v.STT); err != nil {
		return plan{}, s.orCtx(resolveCode(err, providers.CodeSTTUnavailable))
	}
	// the agent resolves its llm itself; a turn it could not answer never listens
	if _, err = d.Providers.LLM(s.ctx, v.LLM); err != nil {
		return plan{}, s.orCtx(resolveCode(err, providers.CodeLLMFailed))
	}
	if p.tts, err = d.Providers.TTS(s.ctx, v.TTS); err != nil {
		return plan{}, s.orCtx(resolveCode(err, providers.CodeTTSFailed))
	}
	channels, bitrate, _ := d.Output(s.ctx, s.dev)
	if p.enc, err = s.c.encoder(channels, bitrate); err != nil {
		d.Log.Error("voice_encoder_failed", "device_id", s.dev)
		return plan{}, providers.CodeTTSFailed
	}
	p.channels = channels
	return p, ""
}

// encoder encodes tts audio once for the device (S0 §6): replies and
// announcements alike.
func (c *Coordinator) encoder(channels, bitrate int) (audio.Encoder, error) {
	return c.d.NewEncoder(audio.EncoderOptions{
		SampleRate: ttsSampleRate, Channels: channels, Bitrate: bitrate, Complexity: opusComplexity,
		Application: audio.AppAudio,
	})
}

// resolveCode: a name with no usable instance behind it is a settings problem, not an outage.
func resolveCode(err error, outage string) string {
	if errors.Is(err, providers.ErrNotFound) || errors.Is(err, providers.ErrWrongKind) ||
		errors.Is(err, providers.ErrUnknownModule) {
		return codeNotConfigured
	}
	return outage
}

// listen feeds the mic to stt until its final transcript.
func (s *session) listen(p plan) (_ string, code string) {
	utter := time.NewTimer(time.Until(s.utterUntil))
	defer utter.Stop()
	sctx, tr := s.startSTTSpan(p.values.STT)
	defer func() { tr.end(code, s.causeNoted()) }()
	st, code := s.openSTT(sctx, p)
	if code != "" {
		return "", code
	}
	tr.event("opened")
	// flux's close waits for the upstream close handshake
	defer func() { s.goSafe("stt_close", func() { _ = st.Close() }) }()
	var partial string
	for {
		select {
		case <-s.ctx.Done():
			return "", s.ctxCode()
		case <-utter.C:
			// partials are shown, never committed: only a final asks
			return "", s.orCtx(providers.CodeSTTFailed)
		case opus := <-s.mic:
			tr.m.add(len(opus), 0)
			if err := st.Write(opus); err != nil {
				s.noteCause(orDetail(err, "send_failed"))
				return "", s.orCtx(providers.CodeSTTFailed)
			}
		case e, ok := <-st.Events():
			if !ok {
				s.noteCause("events_closed")
				return "", s.orCtx(providers.CodeSTTFailed)
			}
			switch e.Kind {
			case providers.STTSpeechStarted:
				tr.event("speech_started")
				s.speechStarted()
			case providers.STTPartial:
				// each new hypothesis goes out as it comes, never held for the final
				if text := strings.TrimSpace(e.Text); text != "" && text != partial {
					partial = text
					tr.hypothesis(false)
					s.transcript(text, false)
				}
			case providers.STTFinal:
				if strings.TrimSpace(e.Text) != "" {
					tr.hypothesis(true)
					s.final = s.c.d.Now()
					if t := s.lastMic.Load(); t != nil {
						s.heard = *t
					}
					s.transcript(strings.TrimSpace(e.Text), true)
					return e.Text, ""
				}
			case providers.STTFailed:
				code := providers.CodeSTTFailed
				if e.Code == providers.CodeSTTUnavailable {
					code = e.Code
				}
				s.noteCause(providers.SafeDetail(e.Detail))
				return "", s.orCtx(code)
			}
		}
	}
}

// transcript hands the screens a hypothesis of the session's utterance
func (s *session) transcript(text string, final bool) {
	if s.c.d.Transcript != nil {
		s.c.d.Transcript(s.dev, s.id, text, final)
	}
}

// openSTT gives up after the open deadline; a start that still completes is closed.
func (s *session) openSTT(ctx context.Context, p plan) (providers.STTStream, string) {
	type opened struct {
		st  providers.STTStream
		err error
	}
	done := make(chan opened, 1)
	// closed rather than sent to on a panic, so the waiters never block
	s.goSafe("stt_start", func() {
		st, err := p.stt.Start(ctx, providers.STTOptions{SampleRate: sttSampleRate, Language: p.language})
		done <- opened{st, err}
	}, func() { close(done) })
	t := time.NewTimer(s.c.lim.sttOpen)
	defer t.Stop()
	select {
	case o := <-done:
		if o.err != nil || o.st == nil {
			s.noteCause(orDetail(o.err, "open_failed"))
			return nil, s.orCtx(providers.CodeSTTUnavailable)
		}
		return o.st, ""
	case <-t.C:
		s.noteCause("open_timeout")
	case <-s.ctx.Done():
	}
	s.goSafe("stt_close", func() {
		if o := <-done; o.st != nil {
			_ = o.st.Close()
		}
	})
	return nil, s.orCtx(providers.CodeSTTUnavailable)
}
