package tracing

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"git.shino.boo/devilreef/cadence/pkg/tracectx"
)

func TestStartEndReachTheStore(t *testing.T) {
	p := setup(t)
	ctx, root := Start(context.Background(), "agent", "agent.turn", attribute.String(AttrDeviceID, "dev-1"))
	_, child := Start(ctx, "agent", "llm.request", attribute.Int("cadence.step", 1))
	End(child, nil)
	End(root, nil)
	page := p.Store().List(ListQuery{})
	if len(page.Traces) != 1 || page.Traces[0].Name != "agent.turn" || page.Traces[0].Spans != 2 || page.Traces[0].DeviceID != "dev-1" {
		t.Fatalf("list %+v", page)
	}
	tr, _ := p.Store().Trace(page.Traces[0].TraceID)
	if tr.Spans[1].ParentID != tr.Spans[0].SpanID || tr.Spans[0].Scope != "agent" || tr.Spans[0].Service != serviceServer {
		t.Fatalf("tree %+v", tr.Spans)
	}
}

// an error's text can carry a url or a key: the span keeps its code alone
func TestEndRecordsACodeNeverTheText(t *testing.T) {
	p := setup(t)
	_, s := Start(context.Background(), "http", "POST collector")
	End(s, errSecretText)
	_, s2 := Start(context.Background(), "http", "GET slow")
	End(s2, context.DeadlineExceeded)
	for _, sum := range p.Store().List(ListQuery{}).Traces {
		tr, _ := p.Store().Trace(sum.TraceID)
		v := tr.Spans[0]
		want := map[string]string{"POST collector": "error", "GET slow": "timeout"}[v.Name]
		if v.Status != StatusError || v.StatusCode != want || v.Attrs[AttrErrorType] != want || len(v.Events) != 1 ||
			v.Events[0].Name != "exception" || v.Events[0].Attrs[AttrErrorType] != want {
			t.Fatalf("%s: %+v", v.Name, v)
		}
		if b, _ := json.Marshal(v); strings.Contains(string(b), "hunter2") || strings.Contains(string(b), "collector.example") {
			t.Fatalf("%s carries the error's text: %s", v.Name, b)
		}
	}
}

func TestOffAndSampling(t *testing.T) {
	p := setup(t)
	p.Apply(Settings{Enabled: false, SampleRatio: 1, RingSize: 100})
	ctx, s := Start(context.Background(), "agent", "x")
	if s.IsRecording() || ctx != context.Background() {
		t.Fatal("recording while off")
	}
	End(s, nil)
	p.Apply(Settings{Enabled: true, SampleRatio: 0, RingSize: 100})
	_, s = Start(context.Background(), "agent", "unsampled")
	End(s, nil)
	if n := len(p.Store().List(ListQuery{}).Traces); n != 0 {
		t.Fatalf("ratio 0 kept %d", n)
	}
	// a device's sampled turn is followed whatever the ratio: parent based
	parent := tracectx.Carrier{Traceparent: "00-0102030405060708090a0b0c0d0e0f10-1112131415161718-01"}
	_, s = Start(tracectx.Extract(context.Background(), parent), "voice", "voice.session")
	End(s, nil)
	tr, ok := p.Store().Trace("0102030405060708090a0b0c0d0e0f10")
	if !ok || len(tr.Spans) != 1 || !tr.Spans[0].RemoteParent || tr.Spans[0].ParentID != "1112131415161718" {
		t.Fatalf("remote child %+v", tr)
	}
	if st := p.Stats(); st.SampleRatio != 0 || !st.Enabled {
		t.Fatalf("stats %+v", st)
	}
}

func TestStartAtBackdatesAndForeignScopesAreDropped(t *testing.T) {
	p := setup(t)
	wake := time.Now().Add(-1500 * time.Millisecond)
	_, s := StartAt(context.Background(), "voice", "voice.turn", wake)
	s.AddEvent("vqe_ready", trace.WithTimestamp(wake.Add(100*time.Millisecond)))
	End(s, nil)
	_, foreign := otel.Tracer("github.com/docker/client").Start(context.Background(), "docker.ping")
	foreign.End()
	page := p.Store().List(ListQuery{})
	if len(page.Traces) != 1 || !page.Traces[0].Start.Equal(wake) || page.Traces[0].DurationMs < 1500 {
		t.Fatalf("backdated %+v", page)
	}
	if p.Stats().Foreign != 1 {
		t.Fatalf("foreign %d", p.Stats().Foreign)
	}
}

func TestBackgroundLinksToItsCause(t *testing.T) {
	p := setup(t)
	ctx, cancelReq := context.WithCancel(context.Background())
	ctx, req := Start(ctx, "api", "POST /x")
	bg, job := Background(ctx, "tasks", "task.run")
	cancelReq()
	if bg.Err() != nil {
		t.Fatal("background context cancelled with its request")
	}
	End(job, nil)
	End(req, nil)
	if n := len(p.Store().List(ListQuery{}).Traces); n != 2 {
		t.Fatalf("traces %d", n)
	}
	tr, _ := p.Store().Trace(job.SpanContext().TraceID().String())
	if len(tr.Spans[0].Links) != 1 || tr.Spans[0].Links[0].SpanID != req.SpanContext().SpanID().String() {
		t.Fatalf("links %+v", tr.Spans[0].Links)
	}
}

func TestGuard(t *testing.T) {
	m := guardAttrs([]attribute.KeyValue{
		attribute.String("url.full", "https://x/y?token=1"), attribute.String("http.url", "x"), attribute.String("url.query", "q=1"),
		attribute.String("gen_ai.prompt", "hi"), attribute.String("cadence.transcript", "hello"), attribute.String("exception.message", "e"),
		attribute.String("auth_token", "t"), attribute.String("media.title", "song"), attribute.String("http.request.header.cookie", "c"),
		attribute.String(AttrHTTPRoute, "/api/v1/devices/:id"), attribute.Int(AttrHTTPStatus, 200),
		attribute.String("cadence.long", strings.Repeat("я", 300)), attribute.StringSlice("cadence.ids", make([]string, 40)),
		attribute.Int64Slice("cadence.big", make([]int64, 33)),
	})
	if len(m) != 4 || m[AttrHTTPRoute] != "/api/v1/devices/:id" || m[AttrHTTPStatus] != int64(200) {
		t.Fatalf("kept %v", m)
	}
	if s := m["cadence.long"].(string); len(s) > maxValue+len("…") || !strings.HasSuffix(s, "…") {
		t.Fatalf("not cut: %d", len(s))
	}
	if ids := m["cadence.ids"].([]string); len(ids) != maxSlice {
		t.Fatalf("slice %d", len(ids))
	}
	for desc, want := range map[string]string{"timeout": "timeout", "status_503": "status_503", "dial tcp 1.2.3.4: refused": "error", "": "error"} {
		if got := statusCode(desc); got != want {
			t.Errorf("%q: %q", desc, got)
		}
	}
}

func TestExportSendsGuardedSpansToTheTarget(t *testing.T) {
	p := setup(t)
	stop := p.Run(context.Background())
	defer stop()
	c := newCollector()
	p.SetTarget(c)
	_, s := Start(context.Background(), "agent", "llm.request", attribute.String("gen_ai.prompt", "secret words"), attribute.Int("cadence.tokens", 12))
	End(s, errSecretText)
	if err := p.tp.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, c.got)
	reqs := c.requests()
	if len(reqs) != 1 || c.sigs[0] != SignalTraces {
		t.Fatalf("requests %d", len(reqs))
	}
	rs := reqs[0].ResourceSpans[0]
	if rs.Resource.Attributes[0].Value.GetStringValue() != serviceServer || rs.ScopeSpans[0].Scope.Name != "cadence/agent" {
		t.Fatalf("resource %+v", rs.Resource)
	}
	sp := rs.ScopeSpans[0].Spans[0]
	if sp.Name != "llm.request" || sp.Status.GetMessage() != "error" || len(sp.Events) != 1 || sp.Events[0].Name != "exception" {
		t.Fatalf("span %+v", sp)
	}
	for _, a := range sp.Attributes {
		if a.Key == "gen_ai.prompt" {
			t.Fatal("prompt exported")
		}
	}
	if st := p.Stats().Export; !st.On || st.Sent != 1 {
		t.Fatalf("export %+v", st)
	}
	// off: what waits is dropped and nothing more is queued
	p.SetTarget(nil)
	_, s = Start(context.Background(), "agent", "after")
	End(s, nil)
	_ = p.tp.ForceFlush(context.Background())
	if st := p.Stats().Export; st.On || st.Queued != 0 || st.Sent != 1 {
		t.Fatalf("after off %+v", st)
	}
}

func TestExportQueueDropsTheOldestAndPermanentRefusals(t *testing.T) {
	e := NewExporter(nil)
	c := newCollector()
	e.SetTarget(c)
	for range exportQueueSpans/100 + 3 {
		e.Enqueue(resourceOf(100))
	}
	if st := e.Stats(); st.Queued > exportQueueSpans || st.Dropped != 300 {
		t.Fatalf("overflow %+v", st)
	}
	c.err = permanentErr{}
	stop := e.Run(context.Background())
	defer stop()
	waitFor(t, c.got)
	eventually(t, func() bool { return e.Stats().Failures == 1 })
	if st := e.Stats(); st.Dropped != 300+500 {
		t.Fatalf("permanent %+v", st)
	}
}

func waitFor(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing posted")
	}
}

func eventually(t *testing.T, ok func() bool) {
	t.Helper()
	for range 500 {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("never")
}

// a root keeps its owner's end, and links back to the span that spawned it
func TestRootKeepsCancellation(t *testing.T) {
	p := setup(t)
	parent, ps := Start(context.Background(), "api", "POST /x")
	owner, cancel := context.WithCancel(parent)
	ctx, s := Root(owner, "tasks", "task.event_turn")
	cancel()
	if ctx.Err() == nil {
		t.Fatal("root outlived its owner")
	}
	End(s, nil)
	End(ps, nil)
	tr, ok := p.Store().Trace(s.SpanContext().TraceID().String())
	if !ok || tr.Spans[0].ParentID != "" || len(tr.Spans[0].Links) != 1 || s.SpanContext().TraceID() == ps.SpanContext().TraceID() {
		t.Fatalf("root %+v", tr)
	}
}
