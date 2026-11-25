package calls_test

import (
	"context"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/pkg/tracectx"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
	"git.shino.boo/devilreef/cadence/server/internal/tracing/tracingtest"
)

// a call whose request carries the device's client span runs as its child, and its answer and events carry the
// call's span back; an untraced refusal carries nothing
func TestCallJoinsTheCallersTraceAndAnswersWithIt(t *testing.T) {
	p := tracingtest.Setup(t)
	r, s := newRouter(t)
	r.Register("echo", func(_ context.Context, c calls.Call) calls.Reply {
		_ = c.Event(map[string]int{"n": 1})
		return calls.OK(nil)
	})
	q := req("1", "echo", true)
	q.GetRpcRequest().Trace = &cadencev2.TraceContext{Traceparent: "00-" + tracingtest.DeviceTrace + "-" + tracingtest.DeviceSpan + "-01"}
	r.Handle("d1", q)
	frames := s.wait(t, 3)
	call := tracingtest.Find(t, tracingtest.Wait(t, p, tracingtest.DeviceTrace, "echo"), "echo")
	if call.ParentID != tracingtest.DeviceSpan || !call.RemoteParent {
		t.Fatalf("call %+v", call)
	}
	kinds := 0
	for _, e := range frames {
		var tc *cadencev2.TraceContext
		switch {
		case e.GetRpcResponse() != nil:
			tc = e.GetRpcResponse().GetTrace()
		case e.GetRpcEvent() != nil:
			tc = e.GetRpcEvent().GetTrace()
		}
		sc := tracectx.SpanContext(gateway.Carrier(tc))
		if sc.TraceID().String() != tracingtest.DeviceTrace || sc.SpanID().String() != call.SpanID {
			t.Fatalf("frame %v carries %v, want the call %s", e, sc, call.SpanID)
		}
		kinds++
	}
	if kinds != 3 {
		t.Fatalf("frames %v", frames)
	}
	r.Handle("d1", req("2", "nope", false))
	for _, e := range s.wait(t, 4) {
		if e.GetRpcResponse().GetId() == "2" && (e.GetRpcResponse().GetStatus() != rpc.StatusUnknownMethod || e.GetRpcResponse().GetTrace() != nil) {
			t.Fatalf("refusal %v", e.GetRpcResponse())
		}
	}
}
