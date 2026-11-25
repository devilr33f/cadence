package voice

import (
	"context"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

// the voice spans: ids, codes, sizes and timings, never a transcript or a reply (rule 8)
const (
	traceScope = "voice"

	attrTrigger    = "cadence.trigger"
	attrEndCode    = "cadence.end_code"
	attrMode       = "cadence.mode"
	attrSegment    = "cadence.segment"
	attrFirstMs    = "cadence.first_ms"
	attrPartials   = "cadence.partials"
	attrPackets    = "cadence.packets"
	attrEndpointMs = "cadence.endpoint_ms"
	attrFirstAudio = "cadence.first_audio_ms"
	attrDetail     = "cadence.detail"

	// the root's summary of a failure: the stage, the span to open and the cause
	attrFailedStage  = "cadence.failed_stage"
	attrFailedSpan   = "cadence.failed_span"
	attrFailedDetail = "cadence.failed_detail"
	attrFailedStep   = "cadence.failed_step"
)

// startSessionSpan opens voice.session under parent: the link's context, or
// the device's voice.turn once the link layer extracted it from SessionStart.
func startSessionSpan(parent context.Context, dev, id, trigger string) (context.Context, trace.Span) {
	return tracing.StartKind(parent, traceScope, "voice.session", trace.SpanKindServer,
		attribute.String(tracing.AttrDeviceID, dev), attribute.String(tracing.AttrSessionID, id),
		attribute.String(attrTrigger, trigger))
}

// endSessionSpan closes the session's span with how it ended; only a code
// failureCode counts as a failure marks it failed.
func (s *session) endSessionSpan(code string) {
	if code == "" {
		code = "ok"
	}
	sp := s.span
	sp.SetAttributes(attribute.String(attrEndCode, code))
	if s.mode != "" {
		sp.SetAttributes(attribute.String(attrMode, s.mode))
	}
	if !s.final.IsZero() && !s.heard.IsZero() {
		sp.SetAttributes(attribute.Int64(attrEndpointMs, s.final.Sub(s.heard).Milliseconds()))
	}
	if !s.final.IsZero() && !s.firstAudio.IsZero() {
		sp.SetAttributes(attribute.Int64(attrFirstAudio, s.firstAudio.Sub(s.final).Milliseconds()))
	}
	if failureCode(code) {
		sp.SetAttributes(s.failedAttrs(code)...)
		tracing.Fail(sp, code)
	}
	sp.End()
}

// failedAttrs say on the root which stage failed and why, so the trace's top
// line answers without opening its children
func (s *session) failedAttrs(code string) []attribute.KeyValue {
	f := s.failureFor(code)
	if f == nil {
		return nil
	}
	attrs := []attribute.KeyValue{attribute.String(attrFailedStage, f.Stage)}
	if name := failedSpan(f.Stage); name != "" {
		attrs = append(attrs, attribute.String(attrFailedSpan, name))
	}
	detail := s.causeNoted()
	if detail == "" && f.HTTPStatus > 0 {
		detail = "status_" + strconv.Itoa(f.HTTPStatus)
	}
	if detail != "" {
		attrs = append(attrs, attribute.String(attrFailedDetail, detail))
	}
	if f.Step > 0 {
		attrs = append(attrs, attribute.Int(attrFailedStep, f.Step))
	}
	return attrs
}

// failedSpan is the span a failed stage's detail is on
func failedSpan(stage string) string {
	switch stage {
	case conversation.StageSTT:
		return "stt.stream"
	case conversation.StageLLM, conversation.StageTool:
		return "agent.turn"
	case conversation.StageTTS:
		return "tts.stream"
	}
	return ""
}

// noteCause keeps the first failing stage's detail; "" is no detail
func (s *session) noteCause(detail string) {
	if detail = providers.SafeDetail(detail); detail == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cause == "" {
		s.cause = detail
	}
}

func (s *session) causeNoted() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cause
}

// orDetail is err's provider detail, else fallback
func orDetail(err error, fallback string) string {
	if d := providers.Detail(err); d != "" {
		return d
	}
	return fallback
}

// meter reports a stream's bytes on its span about once a second; the mic's
// 50 packets a second would otherwise take the store's lock each.
type meter struct {
	ctx     context.Context
	in, out int64
	last    time.Time
}

func newMeter(ctx context.Context) *meter { return &meter{ctx: ctx, last: time.Now()} }

func (m *meter) add(in, out int) {
	m.in += int64(in)
	m.out += int64(out)
	if time.Since(m.last) >= time.Second {
		m.flush()
	}
}

func (m *meter) flush() {
	m.last = time.Now()
	if m.in == 0 && m.out == 0 {
		return
	}
	tracing.Progress(m.ctx, m.in, m.out)
	m.in, m.out = 0, 0
}

// sttTrace is the listen phase's span: stt.stream, from its open to the
// final, with the mic's bytes in and the time to the first hypothesis.
type sttTrace struct {
	span     trace.Span
	m        *meter
	start    time.Time
	partials int
	first    bool
}

func (s *session) startSTTSpan(provider string) (context.Context, *sttTrace) {
	ctx, sp := tracing.StartKind(s.ctx, traceScope, "stt.stream", trace.SpanKindClient,
		attribute.String(tracing.AttrProvider, provider))
	return ctx, &sttTrace{span: sp, m: newMeter(ctx), start: time.Now()}
}

func (t *sttTrace) event(name string) { t.span.AddEvent(name) }

// hypothesis marks the first partial or the final, the latency the owner reads
func (t *sttTrace) hypothesis(final bool) {
	if !final {
		t.partials++
	}
	if !t.first {
		t.first = true
		t.span.SetAttributes(attribute.Int64(attrFirstMs, time.Since(t.start).Milliseconds()))
		t.span.AddEvent("first_hypothesis")
	}
	if final {
		t.span.AddEvent("final")
	}
}

// end: a session ended from outside (superseded, cancelled) is no stt failure;
// a failure's detail is the provider's token for its cause
func (t *sttTrace) end(code, detail string) {
	t.m.flush()
	t.span.SetAttributes(attribute.Int(attrPartials, t.partials))
	if code != "" {
		t.span.SetAttributes(attribute.String(attrEndCode, code))
	}
	if failureCode(code) {
		if detail != "" {
			t.span.SetAttributes(attribute.String(attrDetail, detail))
		}
		tracing.Fail(t.span, code)
	}
	t.span.End()
}

// eventName is a session event's span event: speech_started, thinking…
func eventName(k cadencev2.SessionEventKind) string {
	return strings.ToLower(strings.TrimPrefix(k.String(), "SESSION_EVENT_KIND_"))
}

// ttsTrace is one reply segment's tts.stream: its open, the time to its
// first audio, PCM bytes in from the provider and Opus bytes out.
type ttsTrace struct {
	span    trace.Span
	m       *meter
	start   time.Time
	first   bool
	packets int
}

func startTTSSpan(ctx context.Context, provider string, gen uint64) (context.Context, *ttsTrace) {
	ctx, sp := tracing.StartKind(ctx, traceScope, "tts.stream", trace.SpanKindClient,
		attribute.String(tracing.AttrProvider, provider), attribute.Int64(attrSegment, int64(gen))) //nolint:gosec // a reply's segment count, small
	return ctx, &ttsTrace{span: sp, m: newMeter(ctx), start: time.Now()}
}

func (t *ttsTrace) pcm(samples int) {
	if t == nil {
		return
	}
	if !t.first {
		t.first = true
		t.span.SetAttributes(attribute.Int64(attrFirstMs, time.Since(t.start).Milliseconds()))
		t.span.AddEvent("first_audio")
	}
	t.m.add(samples*2, 0)
}

func (t *ttsTrace) packet(n int) {
	if t == nil {
		return
	}
	t.packets++
	t.m.add(0, n)
}

func (t *ttsTrace) end(err error) {
	if t == nil {
		return
	}
	t.m.flush()
	t.span.SetAttributes(attribute.Int(attrPackets, t.packets))
	if d := providers.Detail(err); d != "" {
		t.span.SetAttributes(attribute.String(attrDetail, d))
	}
	tracing.End(t.span, err)
}

// codeError is a failure known by its code alone.
type codeError string

func (e codeError) Error() string { return string(e) }
func (e codeError) Code() string  { return string(e) }

// openErr is why a tts stream did not open, for its span.
func openErr(st *stream) error {
	if st.err != nil {
		return st.err
	}
	return codeError("tts_failed")
}
