//go:build !cadence_notrace

package tracing

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	coltracev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/trace/v1"
	tracev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/trace/v1"
)

// fakeServer answers the trace ingest with status, and keeps what it took
type fakeServer struct {
	mu     sync.Mutex
	status int
	auth   []string
	spans  []*tracev1.Span
	res    [][]string
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != otlp.TracesPath {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	zr, err := gzip.NewReader(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	b, _ := io.ReadAll(zr)
	var req coltracev1.ExportTraceServiceRequest
	if proto.Unmarshal(b, &req) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if f.status != http.StatusOK {
		w.WriteHeader(f.status)
		return
	}
	for _, rs := range req.GetResourceSpans() {
		var attrs []string
		for _, a := range rs.GetResource().GetAttributes() {
			attrs = append(attrs, a.GetKey()+"="+a.GetValue().GetStringValue())
		}
		f.res = append(f.res, attrs)
		for _, ss := range rs.GetScopeSpans() {
			if ss.GetScope().GetName() == otlp.TraceScope {
				f.spans = append(f.spans, ss.GetSpans()...)
			}
		}
	}
}

func (f *fakeServer) took() []*tracev1.Span {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*tracev1.Span(nil), f.spans...)
}

func setupShipping(t *testing.T, status int) (*Provider, *fakeServer) {
	t.Helper()
	f := &fakeServer{status: status}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := otlp.NewClient(srv.URL, "cdd_token", nil)
	if err != nil {
		t.Fatal(err)
	}
	p := Setup(c, "1.2.3")
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	return p, f
}

// a turn's spans reach the server as otlp protobuf: the turn a root backdated to the wake, the session start's
// context naming it, the playback a child of the server's remote span
func TestTurnShipsToTheServer(t *testing.T) {
	p, f := setupShipping(t, http.StatusOK)
	heard := time.Now().Add(-time.Second)
	turn := StartTurn(context.Background(), heard, "s1", cadencev2.Trigger_TRIGGER_WAKE, &cadencev2.Wake{Engine: "stock", Score: 0.5})
	carried := turn.Proto().GetTraceparent()
	server := "00-" + carried[3:35] + "-0909090909090909-01"
	_, span, release := turn.Playback(&cadencev2.StreamStart{StreamId: "st1", Trace: &cadencev2.TraceContext{Traceparent: server}})
	Bind("st1", span, release)
	turn.End("ok")
	if !Release("st1", "ok") {
		t.Fatal("not bound")
	}
	if !p.ship.flush(t.Context()) {
		t.Fatal("flush ended")
	}
	spans := f.took()
	if len(spans) != 2 {
		t.Fatalf("server took %d spans", len(spans))
	}
	byName := map[string]*tracev1.Span{}
	for _, s := range spans {
		byName[s.GetName()] = s
	}
	root, pb := byName[TurnName], byName["voice.playback"]
	if root == nil || pb == nil || len(root.GetParentSpanId()) != 0 || root.GetStartTimeUnixNano() != uint64(heard.UnixNano()) { //nolint:gosec // a time
		t.Fatalf("turn %v", root)
	}
	if carried[36:52] != hexOf(root.GetSpanId()) || hexOf(pb.GetParentSpanId()) != "0909090909090909" ||
		pb.GetFlags()&flagIsRemote == 0 || root.GetFlags()&flagIsRemote != 0 {
		t.Fatalf("carried %s, turn %x, playback parent %x flags %x", carried, root.GetSpanId(), pb.GetParentSpanId(), pb.GetFlags())
	}
	if f.auth[0] != "Bearer cdd_token" || len(f.res[0]) != 2 || f.res[0][1] != "service.version=1.2.3" {
		t.Fatalf("auth %v resource %v", f.auth, f.res)
	}
	if st := p.Stats(); st.Sent != 2 || st.Dropped != 0 {
		t.Fatalf("stats %+v", st)
	}
}

// a server that refuses the batch drops it, counted; one that fails keeps it for the retry
func TestShipperKeepsWhatFailedAndDropsWhatWasRefused(t *testing.T) {
	p, f := setupShipping(t, http.StatusBadGateway)
	_, span := Start(context.Background(), "one")
	span.End()
	waits := make(chan time.Duration, 1)
	p.ship.after = func(d time.Duration) <-chan time.Time {
		waits <- d
		f.mu.Lock()
		f.status = http.StatusBadRequest
		f.mu.Unlock()
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	p.ship.flush(t.Context())
	if d := <-waits; d < 800*time.Millisecond || d > 1200*time.Millisecond {
		t.Fatalf("first backoff %v", d)
	}
	if st := p.Stats(); st.Rejected != 1 || st.Sent != 0 || p.ship.ring.Len() != 0 {
		t.Fatalf("stats %+v ring %d", st, p.ship.ring.Len())
	}
}

// off at run time, nothing records and no frame carries a context; on again, it does
func TestRuntimeSwitch(t *testing.T) {
	setupShipping(t, http.StatusOK)
	t.Cleanup(func() { SetEnabled(true) })
	SetEnabled(false)
	if turn := StartTurn(context.Background(), time.Time{}, "s", cadencev2.Trigger_TRIGGER_BUTTON, nil); turn.Proto() != nil {
		t.Fatalf("off, the turn carried %v", turn.Proto())
	}
	SetEnabled(true)
	if turn := StartTurn(context.Background(), time.Time{}, "s", cadencev2.Trigger_TRIGGER_BUTTON, nil); turn.Proto() == nil {
		t.Fatal("on, the turn carried nothing")
	}
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}
