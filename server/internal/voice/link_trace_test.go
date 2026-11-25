package voice

import (
	"context"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/tracectx"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
	"git.shino.boo/devilreef/cadence/server/internal/tracing/tracingtest"
)

// the link boundary end to end: a session start's traceparent, extracted as the gateway hands frames down, is the
// parent of voice.session, and the reply's stream start carries the session's span back to the device in that trace
func TestSessionStartTraceparentRoundTrips(t *testing.T) {
	p := tracingtest.Setup(t)
	h := newHarness(t)
	env := &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionStart{SessionStart: &cadencev2.SessionStart{
		SessionId: "s1", Trigger: cadencev2.Trigger_TRIGGER_WAKE,
		Trace: &cadencev2.TraceContext{Traceparent: "00-" + tracingtest.DeviceTrace + "-" + tracingtest.DeviceSpan + "-01"},
	}}}
	h.ctx = gateway.FrameContext(context.Background(), env)
	h.handle(env)
	h.mic("s1", 3, 1)
	h.waitClose("s1")
	spans := tracingtest.Wait(t, p, tracingtest.DeviceTrace, "voice.session")
	sess := tracingtest.Find(t, spans, "voice.session")
	if sess.ParentID != tracingtest.DeviceSpan || !sess.RemoteParent {
		t.Fatalf("session %+v", sess)
	}
	var st *cadencev2.StreamStart
	for _, e := range h.conn.of("s1") {
		if e.GetStreamStart() != nil {
			st = e.GetStreamStart()
		}
	}
	sc := tracectx.SpanContext(gateway.Carrier(st.GetTrace()))
	if st == nil || sc.TraceID().String() != tracingtest.DeviceTrace || sc.SpanID().String() != sess.SpanID {
		t.Fatalf("stream start %v carries %v, want the session %s", st, sc, sess.SpanID)
	}
}
