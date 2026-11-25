package session

import (
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/tracing"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/tracectx"
)

func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(tracing.Use(tp))
	return rec
}

func endedSpan(rec *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	for _, s := range rec.Ended() {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

// a wake opens voice.turn at the instant audiod heard it, with audiod's wake mark on it; the session start carries
// it, the reply plays under the server's span, and the turn ends once that playback is done
func TestTurnTraceFromWakeToPlayback(t *testing.T) {
	rec := recordSpans(t)
	r := newRig(t, 1)
	heard := time.Now().Add(-300 * time.Millisecond)
	tracing.Mark(&audiodv1.TraceMark{Stage: audiodv1.TraceStage_TRACE_STAGE_WAKE_DETECTED, AtUnixNs: heard.UnixNano(),
		Values: []*audiodv1.TraceValue{{Key: "score", Value: 0.9}}})
	r.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Wake: &cadencev2.Wake{Engine: "stock", Score: 0.9}, At: heard}
	start := r.next(t).GetSessionStart()
	turn := tracectx.SpanContext(tracectx.Carrier{Traceparent: start.GetTrace().GetTraceparent()})
	if !turn.IsValid() || !turn.IsSampled() {
		t.Fatalf("session start trace %v", start.GetTrace())
	}
	r.next(t) // the mic packet

	// the server's tts span, a child of the turn
	server := trace.NewSpanContext(trace.SpanContextConfig{TraceID: turn.TraceID(), SpanID: trace.SpanID{9, 9, 9, 9, 9, 9, 9, 9},
		TraceFlags: trace.FlagsSampled})
	stc := tracectx.Inject(trace.ContextWithSpanContext(t.Context(), server))
	r.handle(t, &cadencev2.SessionEvent{SessionId: "s1", Kind: cadencev2.SessionEventKind_SESSION_EVENT_KIND_THINKING})
	r.handle(t, &cadencev2.StreamStart{StreamId: "st1", SessionId: "s1", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY,
		Trace: &cadencev2.TraceContext{Traceparent: stc.Traceparent}})
	st := <-r.speaker.began
	<-r.speaker.writers
	playback := tracectx.SpanContext(tracectx.Carrier{Traceparent: st.GetTrace().GetTraceparent()})
	if playback.TraceID() != turn.TraceID() || playback.SpanID() == server.SpanID() {
		t.Fatalf("the speaker got %v, want the device's playback span", st.GetTrace())
	}
	r.handle(t, &cadencev2.StreamEnd{StreamId: "st1"})
	r.handle(t, &cadencev2.SessionClose{SessionId: "s1"})
	waitFor(t, func() bool { return tracing.Bound("st1") != nil && endedSpan(rec, "voice.wait") != nil })
	if endedSpan(rec, tracing.TurnName) != nil {
		t.Fatal("the turn ended while its reply played")
	}

	// audiod says the stream is done: the playback ends, and with it the turn
	tracing.Mark(&audiodv1.TraceMark{Stage: audiodv1.TraceStage_TRACE_STAGE_FIRST_SAMPLE_PLAYED, StreamId: "st1", AtUnixNs: time.Now().UnixNano()})
	if !tracing.Release("st1", "ok") {
		t.Fatal("no playback bound to the stream")
	}
	waitFor(t, func() bool { return endedSpan(rec, tracing.TurnName) != nil })

	root := endedSpan(rec, tracing.TurnName)
	if root.SpanContext().SpanID() != turn.SpanID() || !root.StartTime().Equal(heard) || root.Parent().IsValid() {
		t.Fatalf("turn %v from %v, want %v from %v", root.SpanContext().SpanID(), root.StartTime(), turn.SpanID(), heard)
	}
	if ev := root.Events(); len(ev) == 0 || ev[0].Name != "audiod.wake_detected" || !ev[0].Time.Equal(time.Unix(0, heard.UnixNano())) {
		t.Fatalf("turn events %v", ev)
	}
	pb := endedSpan(rec, "voice.playback")
	if pb.Parent().SpanID() != server.SpanID() || pb.SpanContext().SpanID() != playback.SpanID() ||
		len(pb.Events()) != 1 || pb.Events()[0].Name != "audiod.first_sample_played" {
		t.Fatalf("playback parent %v events %v", pb.Parent().SpanID(), pb.Events())
	}
	if w := endedSpan(rec, "voice.wait"); w.Parent().SpanID() != turn.SpanID() {
		t.Fatalf("wait parent %v", w.Parent())
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
