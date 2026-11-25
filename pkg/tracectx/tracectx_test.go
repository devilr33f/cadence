package tracectx_test

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"git.shino.boo/devilreef/cadence/pkg/tracectx"
)

func spanCtx(t *testing.T, state string) context.Context {
	t.Helper()
	tid, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	sid, _ := trace.SpanIDFromHex("1112131415161718")
	ts, err := trace.ParseTraceState(state)
	if err != nil {
		t.Fatal(err)
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled, TraceState: ts})
	return trace.ContextWithSpanContext(context.Background(), sc)
}

func TestRoundTrip(t *testing.T) {
	c := tracectx.Inject(spanCtx(t, "cad=1"))
	if c.Traceparent != "00-0102030405060708090a0b0c0d0e0f10-1112131415161718-01" || c.Tracestate != "cad=1" {
		t.Fatalf("carrier %+v", c)
	}
	sc := trace.SpanContextFromContext(tracectx.Extract(context.Background(), c))
	if !sc.IsValid() || !sc.IsRemote() || sc.TraceID().String() != "0102030405060708090a0b0c0d0e0f10" ||
		sc.SpanID().String() != "1112131415161718" || !sc.IsSampled() || sc.TraceState().String() != "cad=1" {
		t.Fatalf("extracted %+v", sc)
	}
}

func TestMapForm(t *testing.T) {
	m := map[string]string{}
	tracectx.InjectMap(spanCtx(t, ""), m)
	if len(m) != 1 || m[tracectx.KeyTraceparent] == "" {
		t.Fatalf("map %v", m)
	}
	if !trace.SpanContextFromContext(tracectx.ExtractMap(context.Background(), m)).IsValid() {
		t.Fatal("map context not extracted")
	}
}

func TestNothingToCarry(t *testing.T) {
	if c := tracectx.Inject(context.Background()); !c.Empty() {
		t.Fatalf("carrier from an empty context: %+v", c)
	}
	m := map[string]string{}
	tracectx.InjectMap(context.Background(), m)
	if len(m) != 0 {
		t.Fatalf("map %v", m)
	}
	ctx := context.Background()
	for _, bad := range []tracectx.Carrier{{}, {Traceparent: "garbage"}, {Traceparent: "00-00000000000000000000000000000000-1112131415161718-01"}} {
		if tracectx.SpanContext(bad).IsValid() {
			t.Fatalf("%+v extracted as valid", bad)
		}
		if tracectx.Extract(ctx, bad) != ctx && trace.SpanContextFromContext(tracectx.Extract(ctx, bad)).IsValid() {
			t.Fatalf("%+v changed the context", bad)
		}
	}
}

func TestLongTracestateDropped(t *testing.T) {
	long := "a=" + strings.Repeat("b", 600)
	c := tracectx.Carrier{Traceparent: "00-0102030405060708090a0b0c0d0e0f10-1112131415161718-01", Tracestate: long}
	sc := tracectx.SpanContext(c)
	if !sc.IsValid() || sc.TraceState().Len() != 0 {
		t.Fatalf("long tracestate kept: %+v", sc)
	}
}
