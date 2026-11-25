package tracing

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// TurnName is the root span of a voice turn on the device.
const TurnName = "voice.turn"

// Turn is one voice session's root span, voice.turn: opened at the wake (or
// press), it parents the capture, the wait for the server, and the reply's
// playback, and the server's turn spans through the SessionStart's context.
// It ends once its session ended and the last reply stream it holds has
// played (or was cut). A nil Turn does nothing.
type Turn struct {
	span trace.Span
	ctx  context.Context

	mu    sync.Mutex
	holds int
	ended bool
	code  string
	// the open wait for the server's reply, nil for none
	wait trace.Span
}

// StartTurn opens a session's voice.turn, backdated to at (the wake audiod
// heard; zero is now), as a new trace's root. A wake mark audiod sent just
// before lands on it.
func StartTurn(ctx context.Context, at time.Time, sessionID string, trigger cadencev2.Trigger, wake *cadencev2.Wake) *Turn {
	if at.IsZero() || at.After(now()) {
		at = now()
	}
	attrs := []attribute.KeyValue{attribute.String(AttrSessionID, sessionID), attribute.String("cadence.trigger", triggerName(trigger))}
	if wake != nil {
		attrs = append(attrs, attribute.String("cadence.wake.engine", wake.GetEngine()),
			attribute.Float64("cadence.wake.score", float64(wake.GetScore())))
	}
	ctx, span := tracer().Start(ctx, TurnName, trace.WithNewRoot(), trace.WithTimestamp(at), trace.WithAttributes(attrs...))
	if trigger == cadencev2.Trigger_TRIGGER_WAKE {
		TakeWake(span)
	}
	return &Turn{span: span, ctx: ctx}
}

// Context carries the turn's span: the capture and the frames it sends are
// its children.
func (t *Turn) Context(parent context.Context) context.Context {
	if t == nil {
		return parent
	}
	return trace.ContextWithSpan(parent, t.span)
}

// Proto is the turn's context for its SessionStart.
func (t *Turn) Proto() *cadencev2.TraceContext {
	if t == nil {
		return nil
	}
	return Proto(t.ctx)
}

// Event records a stage of the turn now (speech_started, thinking…).
func (t *Turn) Event(name string, attrs ...attribute.KeyValue) {
	if t != nil {
		t.span.AddEvent(name, trace.WithAttributes(attrs...))
	}
}

// Waiting opens the wait for the server's reply (the server said it thinks);
// Replied closes it at the first reply stream. A second Waiting keeps the first.
func (t *Turn) Waiting() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.wait == nil && !t.ended {
		_, t.wait = tracer().Start(t.ctx, "voice.wait")
	}
}

// Replied ends the wait, if one is open.
func (t *Turn) Replied() { t.endWait("") }

func (t *Turn) endWait(code string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	w := t.wait
	t.wait = nil
	t.mu.Unlock()
	if w != nil {
		End(w, code)
	}
}

// Playback opens a reply stream's span: a child of the server's span its
// StreamStart names (the turn's tts), else of the turn. The turn stays open
// until the returned release runs (Bind's release, at the stream's end).
func (t *Turn) Playback(st *cadencev2.StreamStart) (context.Context, trace.Span, func()) {
	parent := Extract(context.Background(), st.GetTrace())
	if !trace.SpanContextFromContext(parent).IsValid() {
		parent = t.Context(context.Background())
	}
	ctx, span := tracer().Start(parent, "voice.playback", trace.WithAttributes(attribute.String(AttrStreamID, st.GetStreamId())))
	if t == nil {
		return ctx, span, func() {}
	}
	t.mu.Lock()
	t.holds++
	t.mu.Unlock()
	var once sync.Once
	return ctx, span, func() { once.Do(t.release) }
}

func (t *Turn) release() {
	t.mu.Lock()
	t.holds--
	done := t.ended && t.holds == 0
	code := t.code
	t.mu.Unlock()
	if done {
		t.endSpan(code)
	}
}

// End closes the turn with its session's code (ok, no_speech, send_failed…);
// the span ends now, or once the reply streams it holds are done.
func (t *Turn) End(code string, attrs ...attribute.KeyValue) {
	if t == nil {
		return
	}
	t.endWait("")
	t.mu.Lock()
	if t.ended {
		t.mu.Unlock()
		return
	}
	t.ended, t.code = true, code
	done := t.holds == 0
	t.mu.Unlock()
	t.span.SetAttributes(attrs...)
	t.span.SetAttributes(attribute.String("cadence.code", code))
	if done {
		t.endSpan(code)
	}
}

// codes that end a session as the owner or the device meant: none fails the turn
var benign = map[string]bool{"ok": true, "": true, "no_speech": true, "follow_up_silence": true, "follow_up_echo": true,
	"superseded": true, "local_cancel": true, "muted": true, "ring": true, "command_volume": true, "shutdown": true,
	"stopped": true}

func (t *Turn) endSpan(code string) {
	if !benign[code] {
		Code(t.span, code)
	}
	t.span.End()
}

func triggerName(t cadencev2.Trigger) string {
	switch t {
	case cadencev2.Trigger_TRIGGER_WAKE:
		return "wake"
	case cadencev2.Trigger_TRIGGER_BUTTON:
		return "button"
	case cadencev2.Trigger_TRIGGER_FOLLOW_UP:
		return "follow_up"
	case cadencev2.Trigger_TRIGGER_EVENT:
		return "event"
	}
	return "unspecified"
}
