package gateway_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/trace"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
)

const deviceTurn = "00-0102030405060708090a0b0c0d0e0f10-1112131415161718-01"

// a session start's trace context reaches the handler as the remote parent of what it starts
func TestSessionStartParentsTheHandler(t *testing.T) {
	got := make(chan trace.SpanContext, 4)
	h := start(t, func(hs *gateway.Handlers) {
		hs.OnMessage = func(ctx context.Context, _ gateway.Conn, env *cadencev2.Envelope) {
			if env.GetSessionStart() != nil {
				got <- trace.SpanContextFromContext(ctx)
			}
		}
	})
	c := h.dial(t, tokenOne)
	greet(t, c)
	send(t, c, &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionStart{SessionStart: &cadencev2.SessionStart{
		SessionId: "s-1", Trigger: cadencev2.Trigger_TRIGGER_WAKE, Trace: &cadencev2.TraceContext{Traceparent: deviceTurn},
	}}})
	send(t, c, &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionStart{SessionStart: &cadencev2.SessionStart{
		SessionId: "s-2", Trigger: cadencev2.Trigger_TRIGGER_WAKE,
	}}})
	sc := <-got
	if !sc.IsRemote() || sc.TraceID().String() != "0102030405060708090a0b0c0d0e0f10" || sc.SpanID().String() != "1112131415161718" {
		t.Fatalf("handler context %v", sc)
	}
	// the link is no span: the next frame carries its own context, here none
	if sc := <-got; sc.IsValid() {
		t.Fatalf("an untraced frame inherited %v", sc)
	}
}

func TestFrameContextIgnoresOtherFramesAndMalformedOnes(t *testing.T) {
	ctx := context.Background()
	for _, env := range []*cadencev2.Envelope{
		{Body: &cadencev2.Envelope_MicAudio{MicAudio: &cadencev2.MicAudio{}}},
		{Body: &cadencev2.Envelope_RpcRequest{RpcRequest: &cadencev2.RpcRequest{Trace: &cadencev2.TraceContext{Traceparent: "garbage"}}}},
	} {
		if trace.SpanContextFromContext(gateway.FrameContext(ctx, env)).IsValid() {
			t.Fatalf("%v gave a context", env)
		}
	}
	rq := &cadencev2.Envelope{Body: &cadencev2.Envelope_RpcRequest{RpcRequest: &cadencev2.RpcRequest{Trace: &cadencev2.TraceContext{Traceparent: deviceTurn}}}}
	if !trace.SpanContextFromContext(gateway.FrameContext(ctx, rq)).IsValid() {
		t.Fatal("rpc request context not extracted")
	}
	if gateway.TraceOf(ctx) != nil {
		t.Fatal("trace of an empty context")
	}
}
