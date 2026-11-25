package uniproxy

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

// the socket span's keys: codes, protocol names and numbers alone (rule 8)
const (
	attrDirective   = "cadence.directive"
	attrCloseReason = "websocket.close_reason"
	attrSocketError = "cadence.socket_error"
	attrDetail      = "cadence.detail"
	attrSentFrames  = "cadence.sent_frames"
	attrSentBytes   = "cadence.sent_bytes"
	attrReceived    = "cadence.received"
	attrResults     = "cadence.results"
	attrASRCode     = "uniproxy.asr_code"
	attrStreamClose = "uniproxy.stream_close_reason"
)

// a close reason is kept only when it is a token; anything else may be text
var closeReasonRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,48}$`)

// ending is why a socket ended: codes and numbers alone
type ending struct {
	outcome outcome
	// event is the terminal directive's name (EventException…)
	event string
	// closeCode is the websocket close status, -1 for none
	closeCode int
	reason    string
	// cause is a read error's class when no close status came (eof, conn_reset…)
	cause string
}

// closed records how the read failed: the close status and a token reason, else the error's class
func (e *ending) closed(err error) {
	if s := websocket.CloseStatus(err); s != -1 {
		e.closeCode = int(s)
		var ce websocket.CloseError
		if errors.As(err, &ce) && closeReasonRe.MatchString(ce.Reason) {
			e.reason = ce.Reason
		}
		return
	}
	e.cause = tracing.ErrorCode(err)
}

// detail is the ending as one token: closed_1011, closed_eof, invalid_auth…
func (e ending) detail() string {
	switch {
	case e.outcome == outcomeClosed && e.closeCode >= 0:
		return "closed_" + strconv.Itoa(e.closeCode)
	case e.outcome == outcomeClosed && e.cause != "":
		return "closed_" + e.cause
	}
	return string(e.outcome)
}

// socketSpan is one socket's life on a span of its own, uniproxy.stt or uniproxy.tts, a child of the stream's caller:
// the handshake's http span under it, the protocol's milestones as events, and how it ended
type socketSpan struct {
	span   trace.Span
	kind   string
	module string
	start  time.Time

	frames, bytes, received, results atomic.Int64
	audioOut, audioIn                atomic.Bool
	// the read loop's alone
	acked, resulted bool

	mu     sync.Mutex
	marked outcome
	detail string
	kv     []attribute.KeyValue
}

func startSocketSpan(ctx context.Context, c *config) (context.Context, *socketSpan) {
	kind := c.kind()
	ctx, span := tracing.StartKind(ctx, "uniproxy", "uniproxy."+kind, trace.SpanKindClient,
		attribute.String(tracing.AttrModule, c.module))
	return ctx, &socketSpan{span: span, kind: kind, module: c.module, start: time.Now()}
}

// kind is stt or tts, by the module
func (c *config) kind() string {
	if strings.HasSuffix(c.module, "-tts") {
		return "tts"
	}
	return "stt"
}

func (s *socketSpan) dialFailed(log *slog.Logger, err error, e dialError) {
	s.span.SetAttributes(attribute.String(tracing.AttrOutcome, "dial_failed"))
	if e.status != 0 {
		s.span.SetAttributes(attribute.Int(tracing.AttrHTTPStatus, e.status))
		tracing.Fail(s.span, e.detail)
	} else {
		tracing.FailErr(s.span, err)
	}
	s.span.End()
	logger(log).Warn(s.kind+"_stream_failed", "module", s.module, "outcome", "dial_failed", "detail", e.cause(),
		"ms", time.Since(s.start).Milliseconds())
}

func (s *socketSpan) opened() { s.span.AddEvent("ws_open") }

func (s *socketSpan) sent(n int) {
	s.frames.Add(1)
	s.bytes.Add(int64(n))
}

func (s *socketSpan) audioSent() {
	if !s.audioOut.Swap(true) {
		s.span.AddEvent("first_audio_sent")
	}
}

func (s *socketSpan) audioReceived() {
	if !s.audioIn.Swap(true) {
		s.span.AddEvent("first_audio_received")
	}
}

func (s *socketSpan) gotFrame() { s.received.Add(1) }

// milestone marks a directive the stream's progress hangs on, the first of each
func (s *socketSpan) milestone(name string) {
	switch {
	case name == evInputStartAck && !s.acked:
		s.acked = true
		s.span.AddEvent("stream_opened")
	case name == evAsrResult:
		s.results.Add(1)
		if !s.resulted {
			s.resulted = true
			s.span.AddEvent("first_result")
		}
	}
}

// mark is the stream's own failure; the first one stands
func (s *socketSpan) mark(o outcome, detail string, kv ...attribute.KeyValue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.marked != "" {
		return
	}
	s.marked, s.detail, s.kv = o, detail, kv
}

// end closes the span: ok after the caller's own close with nothing marked, else failed by what ended it, with one
// log line of codes and numbers
func (s *socketSpan) end(log *slog.Logger, e ending) {
	counts := []attribute.KeyValue{
		attribute.Int64(attrSentFrames, s.frames.Load()), attribute.Int64(attrSentBytes, s.bytes.Load()),
		attribute.Int64(attrReceived, s.received.Load()), attribute.Int64(attrResults, s.results.Load()),
	}
	s.span.SetAttributes(counts...)
	s.mu.Lock()
	o, detail, kv := s.marked, s.detail, s.kv
	s.mu.Unlock()
	if o == "" {
		o, detail = e.outcome, e.detail()
	} else {
		// the stream failed first: the socket's close is its doing
		e = ending{outcome: o, closeCode: -1}
	}
	if o == "" {
		s.span.AddEvent("ws_closed")
		s.span.End()
		return
	}
	attrs := append([]attribute.KeyValue{attribute.String(tracing.AttrOutcome, string(o)), attribute.String(attrDetail, detail)}, kv...)
	if e.event != "" {
		attrs = append(attrs, attribute.String(attrDirective, e.event))
	}
	if e.closeCode >= 0 {
		attrs = append(attrs, attribute.Int(tracing.AttrWSCloseCode, e.closeCode))
	}
	if e.reason != "" {
		attrs = append(attrs, attribute.String(attrCloseReason, e.reason))
	}
	if e.cause != "" {
		attrs = append(attrs, attribute.String(attrSocketError, e.cause))
	}
	s.span.SetAttributes(attrs...)
	s.span.AddEvent("socket_end", trace.WithAttributes(attrs...))
	tracing.Fail(s.span, detail)
	s.span.End()
	logger(log).Warn(s.kind+"_stream_failed", "module", s.module, "outcome", string(o), "detail", detail,
		"event", e.event, "close_code", e.closeCode, "close_reason", e.reason, "sent_frames", s.frames.Load(),
		"sent_bytes", s.bytes.Load(), "received", s.received.Load(), "results", s.results.Load(),
		"ms", time.Since(s.start).Milliseconds())
}

func logger(l *slog.Logger) *slog.Logger {
	if l == nil {
		return slog.Default()
	}
	return l
}
