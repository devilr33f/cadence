package voice

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
	"git.shino.boo/devilreef/cadence/server/internal/voice/cmdguard"
)

// Announcement is text spoken on a device outside any voice session.
type Announcement struct {
	Text string
	// Language "" speaks the device's setting
	Language string
}

// what Announce returns when no audio of the announcement went out
var (
	ErrOffline         = errors.New("voice: device offline")
	ErrNoAudio         = errors.New("voice: device reported no audio output")
	ErrBusy            = errors.New("voice: a voice session is active")
	ErrTTSUnconfigured = errors.New("voice: no tts provider set")
	ErrTTSFailed       = errors.New("voice: tts failed")
	ErrTTSTimeout      = errors.New("voice: no tts audio in time")
	ErrSuperseded      = errors.New("voice: a newer announcement replaced it")
	errAnnounce        = errors.New("voice: announcement failed")
	errAnnounceLimit   = errors.New("voice: announcement too long")
)

// announcement codes besides the providers' and the session's own; they reach
// the announce log only
const (
	codeOffline         = "device_offline"
	codeNoAudio         = "no_audio"
	codeBusy            = "busy"
	codeTTSUnconfigured = "tts_unconfigured"
	codeTTSTimeout      = "tts_timeout"
	codeSuperseded      = "superseded"
	codeCanceled        = "canceled"
)

// Linked makes conn the device's link for announcements; call it from the
// gateway's OnHello with its ctx. A new link is a new start: what the old
// one's last turn left for a follow-up goes.
func (c *Coordinator) Linked(ctx context.Context, conn gateway.Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.links[conn.DeviceID()] = link{conn: conn, ctx: ctx}
	c.forgetTurns(conn.DeviceID())
}

// Announce speaks a.Text on the device over its music: a stream with purpose
// announce, duck and no session (S0 §5.7), encoded as replies are. It returns
// the stream id once the first frame went out; the stream then runs on its own
// until its audio ends, a newer announcement replaces it, it runs past
// announceMax or the link closes. A running voice session refuses it.
func (c *Coordinator) Announce(ctx context.Context, deviceID string, a Announcement) (string, error) {
	chars := utf8.RuneCountInString(a.Text)
	// the caller's trace (a task's event, an api call) holds the announcement
	ctx, span := tracing.Start(ctx, traceScope, "voice.announce",
		attribute.String(tracing.AttrDeviceID, deviceID), attribute.Int("cadence.chars", chars))
	l, p, code := c.announcePlan(ctx, deviceID, a.Language)
	if code != "" {
		c.d.Log.Info("announce", "device_id", deviceID, "code", code, "chars", chars)
		span.SetAttributes(attribute.String(attrEndCode, code))
		tracing.Fail(span, code)
		span.End()
		return "", announceError(code)
	}
	an := newAnnouncement(c, l, deviceID, p.channels, chars, span)
	c.supersede(an)
	wait := time.NewTimer(c.lim.announceWait)
	defer wait.Stop()
	an.run(p, a.Text)
	select {
	case <-an.first:
	case <-an.done:
	case <-wait.C:
		an.abortSilent(codeTTSTimeout)
	case <-ctx.Done():
		an.abortSilent(codeCanceled)
	}
	if code, silent := an.outcome(); silent {
		return "", announceError(code)
	}
	return an.id, nil
}

// supersede makes an the device's announcement and stops the older one under
// the same lock (the stop takes only the older one's own mutex and sends
// without blocking): an older stream's end always goes out before any newer
// one can begin, however many announcements overlap.
func (c *Coordinator) supersede(an *announcement) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old := c.announces[an.dev]; old != nil {
		old.stop(codeSuperseded)
	}
	c.announces[an.dev] = an
}

// announcePlan checks that the device can take an announcement now, in the
// order the API reports refusals, and resolves its tts and encoder.
func (c *Coordinator) announcePlan(ctx context.Context, dev, language string) (link, plan, string) {
	c.mu.Lock()
	l, linked := c.links[dev]
	// a session that starts after this check runs beside the announcement: the
	// device plays both, and audiod cuts an announcement still arriving when
	// the reply begins (accepted: the user woke the device)
	busy := c.sessions[dev] != nil
	c.mu.Unlock()
	if !linked || l.ctx.Err() != nil {
		return link{}, plan{}, codeOffline
	}
	channels, bitrate, reported := c.d.Output(ctx, dev)
	if !reported {
		return link{}, plan{}, codeNoAudio
	}
	if busy {
		return link{}, plan{}, codeBusy
	}
	v, err := c.d.Settings(ctx, dev)
	if err != nil {
		if ctx.Err() != nil {
			return link{}, plan{}, codeCanceled
		}
		c.d.Log.Error("voice_settings_failed", "device_id", dev)
		return link{}, plan{}, codeInternal
	}
	// a turn needs no voice either: an unset one is the tts module's own default (aura's model, uniproxy's voice)
	if v.TTS == "" {
		return link{}, plan{}, codeTTSUnconfigured
	}
	p := plan{voice: v.Voice, language: v.Language, channels: channels, values: v}
	if language != "" {
		p.language = language
	}
	if p.tts, err = c.d.Providers.TTS(ctx, v.TTS); err != nil {
		if resolveCode(err, providers.CodeTTSFailed) == codeNotConfigured {
			return link{}, plan{}, codeTTSUnconfigured
		}
		return link{}, plan{}, providers.CodeTTSFailed
	}
	if p.enc, err = c.encoder(channels, bitrate); err != nil {
		c.d.Log.Error("voice_encoder_failed", "device_id", dev)
		return link{}, plan{}, providers.CodeTTSFailed
	}
	return l, p, ""
}

func announceError(code string) error {
	switch code {
	case codeOffline, reasonDisconnected:
		return ErrOffline
	case codeNoAudio:
		return ErrNoAudio
	case codeBusy:
		return ErrBusy
	case codeTTSUnconfigured:
		return ErrTTSUnconfigured
	case providers.CodeTTSFailed:
		return ErrTTSFailed
	// the stream's own limit before any audio is no audio in time
	case codeTTSTimeout, codeTimeout:
		return ErrTTSTimeout
	case codeSuperseded:
		return ErrSuperseded
	case codeCanceled:
		return context.Canceled
	}
	return errAnnounce
}

func (c *Coordinator) forgetAnnouncement(a *announcement) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.announces[a.dev] == a {
		delete(c.announces, a.dev)
	}
}

// announcement is one announce stream; its frames go out on the link it began
// on, and stop with it.
type announcement struct {
	c        *Coordinator
	dev, id  string
	conn     gateway.Conn
	channels uint32
	chars    int
	ctx      context.Context
	cancel   context.CancelCauseFunc
	release  context.CancelFunc
	span     trace.Span // voice.announce, carried by ctx
	started  time.Time
	first    chan struct{} // closed once the first frame went out
	done     chan struct{} // closed once it ended

	mu      sync.Mutex
	ended   bool
	code    string // set once ended; "" is ok
	began   bool
	seq     uint32
	firstAt time.Time
}

func newAnnouncement(c *Coordinator, l link, dev string, channels, chars int, span trace.Span) *announcement {
	// the link's lifetime, the caller's span
	base, cancel := context.WithCancelCause(trace.ContextWithSpan(l.ctx, span))
	ctx, release := context.WithTimeoutCause(base, c.lim.announceMax, errAnnounceLimit)
	ch := uint32(1)
	if channels == 2 {
		ch = 2
	}
	return &announcement{
		c: c, dev: dev, id: rand.Text(), conn: l.conn, channels: ch, chars: chars,
		ctx: ctx, cancel: cancel, release: release, span: span, started: c.d.Now(),
		first: make(chan struct{}), done: make(chan struct{}),
	}
}

// run speaks text on its own goroutine and ends the stream, after a panic too.
func (a *announcement) run(p plan, text string) {
	code := codeInternal
	a.goSafe("announce", func() { code = a.speak(p, text) }, func() { a.finish(code) })
}

// speak sends the text to tts and paces its audio to the device; it returns
// the code the stream ends with. Audio that never came is a tts failure.
func (a *announcement) speak(p plan, text string) string {
	tts, err := p.tts.Open(a.ctx, p.ttsOptions())
	if err != nil || tts == nil {
		return a.orCtx(providers.CodeTTSFailed)
	}
	// aura's close waits for the upstream close handshake
	defer a.goSafe("tts_close", func() { _ = tts.Close() })
	if tts.Speak(cmdguard.Rewrite(text, p.language)) != nil || tts.Flush() != nil {
		return a.orCtx(providers.CodeTTSFailed)
	}
	// room for the longest stream: the encoder never waits on the pacer, so tts is always read
	packets := make(chan packet, int(a.c.lim.announceMax/frameDuration))
	encoded := make(chan error, 1)
	a.goSafe("encode", func() {
		_, tt := startTTSSpan(a.ctx, p.values.TTS, 0)
		err := encode(a.ctx, tts, p.enc, p.channels, 0, packets, tt)
		tt.end(err)
		encoded <- err
	}, func() { close(packets) })
	tick, stopTick := a.c.tick()
	pace(a.ctx, packets, func(packet) bool { return true }, a.send, tick)
	stopTick()
	if a.ctx.Err() != nil {
		return a.ctxCode()
	}
	if err := <-encoded; err != nil || a.silent() {
		return providers.CodeTTSFailed
	}
	return ""
}

// send sends one packet, opening the stream before the first.
func (a *announcement) send(opus []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ended {
		return errNotSent
	}
	if !a.began {
		if err := a.conn.Send(&cadencev2.Envelope{Body: &cadencev2.Envelope_StreamStart{StreamStart: &cadencev2.StreamStart{
			StreamId: a.id, Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_ANNOUNCE,
			Codec: cadencev2.Codec_CODEC_OPUS, Channels: a.channels, Duck: true, Trace: gateway.TraceOf(a.ctx),
		}}}); err != nil {
			a.cancel(err)
			return err
		}
		a.began = true
	}
	if err := a.conn.Send(&cadencev2.Envelope{Body: &cadencev2.Envelope_AudioDown{AudioDown: &cadencev2.AudioDown{
		StreamId: a.id, Seq: a.seq, Opus: opus,
	}}}); err != nil {
		a.cancel(err)
		return err
	}
	if a.seq == 0 {
		a.firstAt = a.c.d.Now()
		close(a.first)
	}
	a.seq++
	return nil
}

// stop ends the stream from outside: from its return no frame of it goes out
// but its end, at once.
func (a *announcement) stop(code string) {
	a.end(code)
	a.cancel(errEnded)
}

// abortSilent stops the stream only while no audio of it went out.
func (a *announcement) abortSilent(code string) {
	a.mu.Lock()
	silent := a.seq == 0
	if silent {
		a.endLocked(code)
	}
	a.mu.Unlock()
	if silent {
		a.cancel(errEnded)
	}
}

// end records the code the stream ended with and sends its end; the first call wins.
func (a *announcement) end(code string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.endLocked(code)
}

func (a *announcement) endLocked(code string) {
	if a.ended {
		return
	}
	a.ended, a.code = true, code
	// a link that is gone gets nothing more
	if a.began && code != reasonDisconnected {
		_ = a.conn.Send(&cadencev2.Envelope{Body: &cadencev2.Envelope_StreamEnd{StreamEnd: &cadencev2.StreamEnd{
			StreamId: a.id,
		}}})
	}
}

func (a *announcement) finish(code string) {
	a.end(code)
	a.cancel(errEnded)
	a.release()
	a.c.forgetAnnouncement(a)
	close(a.done)
	a.log()
	if a.c.d.AnnounceEnded != nil {
		a.c.d.AnnounceEnded(a.dev)
	}
}

// outcome is the code the stream ended with, and whether no audio of it went out.
func (a *announcement) outcome() (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.code, a.seq == 0
}

func (a *announcement) silent() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.seq == 0
}

// log carries ids, codes and counts only: never the text, the voice or the provider.
func (a *announcement) log() {
	a.mu.Lock()
	code, firstAt := a.code, a.firstAt
	a.mu.Unlock()
	if code == "" {
		code = "ok"
	}
	attrs := []any{"device_id", a.dev, "stream_id", a.id, "code", code, "chars", a.chars}
	if !firstAt.IsZero() {
		attrs = append(attrs, "first_audio_ms", firstAt.Sub(a.started).Milliseconds())
	}
	a.c.d.Log.Info("announce", attrs...)
	a.span.SetAttributes(attribute.String(attrEndCode, code))
	if code != "ok" && code != codeSuperseded {
		tracing.Fail(a.span, code)
	}
	a.span.End()
}

func (a *announcement) orCtx(code string) string {
	if a.ctx.Err() != nil {
		return a.ctxCode()
	}
	return code
}

// ctxCode names why the stream's ctx ended; a stop's own code is already set
// and wins in end. A device whose send queue is full cannot take the stream:
// for the caller it is offline.
func (a *announcement) ctxCode() string {
	cause := context.Cause(a.ctx)
	switch {
	case errors.Is(cause, gateway.ErrBackpressure):
		return codeOffline
	case errors.Is(cause, errAnnounceLimit):
		return codeTimeout
	case errors.Is(cause, errPanic):
		return codeInternal
	}
	return reasonDisconnected
}

// goSafe runs a helper of the stream; a panic ends the stream as internal,
// and only the ids and the part are logged.
func (a *announcement) goSafe(name string, fn func(), then ...func()) {
	goRecover(func() {
		a.c.d.Log.Error("voice_panic", "device_id", a.dev, "stream_id", a.id, "part", name)
		a.cancel(errPanic)
	}, fn, then...)
}

// stopAnnouncement ends the device's announcement, when one plays, for a stop request: the owner's stop reaches
// everything that is speaking.
func (c *Coordinator) stopAnnouncement(deviceID string) bool {
	c.mu.Lock()
	an := c.announces[deviceID]
	c.mu.Unlock()
	if an == nil {
		return false
	}
	an.stop(codeCanceled)
	return true
}
