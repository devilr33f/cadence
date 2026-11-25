package tracing_test

import (
	"context"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"git.shino.boo/devilreef/cadence/device/internal/tracing"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

func record(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	t.Cleanup(tracing.Use(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))))
	return rec
}

// a mark is an event of the span its id names at its own instant; one that lasted is a child span; values keep only
// code keys; a mark of nothing traced is dropped and counted
func TestMarksLandOnTheirSpans(t *testing.T) {
	rec := record(t)
	_, capture := tracing.Start(context.Background(), "voice.capture")
	tracing.Bind("mic-1", capture, nil)
	at := time.Now().Add(-50 * time.Millisecond)
	tracing.Mark(&audiodv1.TraceMark{Stage: audiodv1.TraceStage_TRACE_STAGE_FIRST_OPUS_FRAME, SessionId: "mic-1", AtUnixNs: at.UnixNano(),
		Values: []*audiodv1.TraceValue{{Key: "frames", Value: 3}, {Key: "Not A Code", Value: 1}}})
	tracing.Mark(&audiodv1.TraceMark{Stage: audiodv1.TraceStage_TRACE_STAGE_VQE_STATS, SessionId: "mic-1", AtUnixNs: at.UnixNano(),
		DurationNs: int64(20 * time.Millisecond)})
	tracing.Mark(&audiodv1.TraceMark{Stage: 99, SessionId: "mic-1"})
	before := tracing.DroppedMarks()
	tracing.Mark(&audiodv1.TraceMark{Stage: audiodv1.TraceStage_TRACE_STAGE_UNDERRUN, StreamId: "nobody"})
	if tracing.DroppedMarks() != before+1 {
		t.Fatal("a mark of nothing traced was not counted")
	}
	tracing.Release("mic-1", "ok")
	var got sdktrace.ReadOnlySpan
	var child sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		switch s.Name() {
		case "voice.capture":
			got = s
		case "audiod.vqe_stats":
			child = s
		}
	}
	ev := got.Events()
	if len(ev) != 2 || ev[0].Name != "audiod.first_opus_frame" || !ev[0].Time.Equal(time.Unix(0, at.UnixNano())) ||
		len(ev[0].Attributes) != 1 || string(ev[0].Attributes[0].Key) != "audiod.frames" || ev[1].Name != "audiod.stage_99" {
		t.Fatalf("events %+v", ev)
	}
	if child == nil || child.Parent().SpanID() != got.SpanContext().SpanID() || child.EndTime().Sub(child.StartTime()) != 20*time.Millisecond {
		t.Fatalf("interval %v", child)
	}
}

// a span bound past the cap ends the oldest as evicted, and a release runs once
func TestBindEvictsTheOldest(t *testing.T) {
	rec := record(t)
	released := 0
	for i := range 65 {
		_, s := tracing.Start(context.Background(), "s")
		tracing.Bind(string(rune('A'+i)), s, func() { released++ })
	}
	if len(rec.Ended()) != 1 || released != 1 || tracing.Bound("A") != nil || tracing.Bound("B") == nil {
		t.Fatalf("ended %d released %d", len(rec.Ended()), released)
	}
	tracing.ReleasePrefix("", "test")
	if released != 65 {
		t.Fatalf("released %d", released)
	}
}
