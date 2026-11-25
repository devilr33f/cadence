package logs_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp/otlptest"
)

const token = "cdd_device-token-0123456789"

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newPipeline ships to srv, with the token registered.
func newPipeline(t *testing.T, srv *otlptest.Server) (*logs.Pipeline, *fakeClock, *syncBuf) {
	t.Helper()
	return newPipelineTrusting(t, srv, srv.Pool())
}

func newPipelineTrusting(t *testing.T, srv *otlptest.Server, roots *x509.CertPool) (*logs.Pipeline, *fakeClock, *syncBuf) {
	t.Helper()
	c, err := otlp.NewClient(srv.URL, token, roots)
	if err != nil {
		t.Fatal(err)
	}
	s := &logs.Scrubber{}
	s.Set("device_token", token)
	buf := &syncBuf{}
	p := logs.New(c, s, slog.New(slog.NewJSONHandler(buf, nil)))
	clk := newFakeClock(t0)
	logs.SetClock(p, clk)
	// the middle of the jitter: a backoff waits as it is
	logs.SetRand(p, func() float64 { return 0.5 })
	return p, clk, buf
}

func run(t *testing.T, p *logs.Pipeline) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return")
		}
	})
}

func info(body string) logs.Record {
	return logs.Record{Severity: logs.Info, Source: "cadenced", Body: body}
}

func bodies(t *testing.T, r otlptest.Request) []string {
	t.Helper()
	var out []string
	for _, rl := range otlptest.Logs(t, r).GetResourceLogs() {
		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				out = append(out, lr.GetBody().GetStringValue())
			}
		}
	}
	return out
}

func TestBatchEveryTwoSeconds(t *testing.T) {
	srv := otlptest.New(t)
	p, clk, _ := newPipeline(t, srv)
	run(t, p)
	if d := clk.next(t); d != 2*time.Second {
		t.Fatalf("batch wait = %v", d)
	}
	at := t0.Add(-time.Second)
	p.Emit(logs.Record{Time: at, Severity: logs.Error, Source: "audiod", Body: "capture_failed code=7"})
	srv.None(t, 50*time.Millisecond)
	clk.Advance(2 * time.Second)

	r := srv.Next(t)
	if r.Path != "/v2/otlp/v1/logs" || r.Header.Get("Authorization") != "Bearer "+token || r.BadGzip {
		t.Fatalf("request = %s, gzip ok %v", r.Path, !r.BadGzip)
	}
	req := otlptest.Logs(t, r)
	if len(req.GetResourceLogs()) != 1 {
		t.Fatalf("resource logs = %d", len(req.GetResourceLogs()))
	}
	rl := req.GetResourceLogs()[0]
	if a := rl.GetResource().GetAttributes(); len(a) != 1 || a[0].GetKey() != "service.name" || a[0].GetValue().GetStringValue() != "cadenced" {
		t.Fatalf("resource = %v", rl.GetResource())
	}
	sl := rl.GetScopeLogs()
	if len(sl) != 1 || sl[0].GetScope().GetName() != "cadence.device" || len(sl[0].GetLogRecords()) != 1 {
		t.Fatalf("scope logs = %v", sl)
	}
	lr := sl[0].GetLogRecords()[0]
	if lr.GetTimeUnixNano() != uint64(at.UnixNano()) || lr.GetSeverityNumber() != 17 || lr.GetSeverityText() != "ERROR" ||
		source(lr) != "audiod" || lr.GetBody().GetStringValue() != "capture_failed code=7" {
		t.Fatalf("record = %v", lr)
	}
	// acked: the ring is empty once the exporter waits again
	if d := clk.next(t); d != 2*time.Second {
		t.Fatalf("batch wait = %v", d)
	}
	if st := p.Stats(); st.RingBytes != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

// nothing goes out below 512 KiB before the 2 s; the record that reaches it starts a batch at once
func TestBatchAt512KiB(t *testing.T) {
	srv := otlptest.New(t)
	p, clk, _ := newPipeline(t, srv)
	run(t, p)
	clk.next(t)
	body := strings.Repeat("x", 4000)
	p.Emit(info(body))
	size := p.Stats().RingBytes
	n := 1
	for (uint64(n)+1)*size < 512<<10 {
		p.Emit(info(body))
		n++
	}
	srv.None(t, 100*time.Millisecond)
	p.Emit(info(body))
	if got := bodies(t, srv.Next(t)); len(got) != n+1 {
		t.Fatalf("batch = %d records, want %d", len(got), n+1)
	}
}

// random text compresses to about three quarters: 2 MiB of it is no single request
func TestSplitOverCompressedCap(t *testing.T) {
	srv := otlptest.New(t)
	p, _, _ := newPipeline(t, srv)
	const n = 512
	want := make([]string, n)
	raw := make([]byte, 3060)
	for i := range n {
		_, _ = rand.Read(raw)
		want[i] = fmt.Sprintf("%04d ", i) + base64.StdEncoding.EncodeToString(raw)
		p.Emit(info(want[i]))
	}
	run(t, p)
	var got []string
	requests := 0
	for len(got) < n {
		r := srv.Next(t)
		requests++
		if r.Compressed > otlp.MaxRequestBytes {
			t.Fatalf("request of %d bytes", r.Compressed)
		}
		got = append(got, bodies(t, r)...)
	}
	if requests < 2 {
		t.Fatalf("%d request", requests)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("record %d out of order", i)
		}
	}
}

func TestRetryAfterHonoured(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		header string
		// a date this far ahead of the answer, in place of header
		dateIn   time.Duration
		min, max time.Duration
	}{
		{"429 seconds", 429, "7", 0, 7 * time.Second, 7 * time.Second},
		{"503 absent", 503, "", 0, 5 * time.Second, 5 * time.Second},
		{"429 date", 429, "", 30 * time.Second, 28 * time.Second, 30 * time.Second},
		{"503 date capped", 503, "", time.Hour, time.Minute, time.Minute},
		{"429 seconds capped", 429, "600", 0, time.Minute, time.Minute},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := otlptest.New(t)
			var answered atomic.Bool
			srv.Respond(func(otlptest.Request) otlptest.Response {
				if answered.Swap(true) {
					return otlptest.Response{Status: http.StatusOK}
				}
				h := http.Header{}
				switch {
				case c.dateIn > 0:
					// dated as the fake answers, so no time passes between the two
					h.Set("Retry-After", time.Now().Add(c.dateIn).UTC().Format(http.TimeFormat))
				case c.header != "":
					h.Set("Retry-After", c.header)
				}
				return otlptest.Response{Status: c.status, Header: h}
			})
			p, clk, _ := newPipeline(t, srv)
			// no stretch: the wait as the server asked
			logs.SetRand(p, func() float64 { return 0 })
			run(t, p)
			waiting(t, clk)
			p.Emit(info("one"))
			clk.Advance(2 * time.Second)
			if r := srv.Next(t); r.Answer.Status != c.status {
				t.Fatalf("answer = %d", r.Answer.Status)
			}
			d := clk.next(t)
			if d < c.min || d > c.max {
				t.Fatalf("waits %v, want %v..%v", d, c.min, c.max)
			}
			srv.None(t, 50*time.Millisecond)
			clk.Advance(d)
			if got := bodies(t, srv.Next(t)); len(got) != 1 || got[0] != "one" {
				t.Fatalf("retried = %v", got)
			}
		})
	}
}

// a refused batch is dropped and counted; the log says so once per episode,
// with no body and no token
func TestRejectedBatchDropped(t *testing.T) {
	srv := otlptest.New(t)
	srv.Script(otlptest.Response{Status: 400}, otlptest.Response{Status: 413}, otlptest.Response{Status: 200}, otlptest.Response{Status: 400})
	p, clk, buf := newPipeline(t, srv)
	run(t, p)
	waiting(t, clk)
	for i, want := range []uint64{1, 3, 3, 4} {
		// the second batch has two records, the token in one of them
		p.Emit(info(fmt.Sprintf("secret body %d", i)))
		if i == 1 {
			p.Emit(info("token " + token))
		}
		clk.Advance(2 * time.Second)
		srv.Next(t)
		// back to the next batch's wait: the batch is done with
		waiting(t, clk)
		if st := p.Stats(); st.Rejected != want || st.RingBytes != 0 {
			t.Fatalf("after batch %d: %+v", i, st)
		}
	}
	out := buf.String()
	if n := strings.Count(out, `"msg":"logs_rejected"`); n != 2 {
		t.Fatalf("logs_rejected %d times: %s", n, out)
	}
	if !strings.Contains(out, `"msg":"logs_rejected","status":400,"count":1`) {
		t.Fatalf("log: %s", out)
	}
	if strings.Contains(out, "secret body") || strings.Contains(out, token) {
		t.Fatalf("log carries a body or the token: %s", out)
	}
}

// any error backs off the same way, whether or not the request reached the server
func TestNetworkErrorBackoffAndReset(t *testing.T) {
	srv := otlptest.New(t)
	var down atomic.Bool
	down.Store(true)
	srv.Respond(func(otlptest.Request) otlptest.Response {
		if down.Load() {
			return otlptest.Response{Drop: true}
		}
		return otlptest.Response{Status: http.StatusOK}
	})
	p, clk, buf := newPipeline(t, srv)
	run(t, p)
	waiting(t, clk)
	p.Emit(info("first line"))
	clk.Advance(2 * time.Second)
	for _, want := range []time.Duration{1, 2, 4, 8, 16, 32, 60, 60} {
		clk.expect(t, want*time.Second)
	}
	down.Store(false)
	if got := delivered(t, srv, clk); len(got) != 1 || got[0] != "first line" {
		t.Fatalf("delivered = %v", got)
	}
	// a success starts the backoff over
	waiting(t, clk)
	down.Store(true)
	p.Emit(info("second line"))
	clk.Advance(2 * time.Second)
	if d := clk.next(t); d != time.Second {
		t.Fatalf("waits %v after a success", d)
	}
	down.Store(false)
	clk.Advance(time.Second)
	if got := delivered(t, srv, clk); len(got) != 1 || got[0] != "second line" {
		t.Fatalf("delivered = %v", got)
	}
	if out := buf.String(); strings.Contains(out, token) || strings.Contains(out, "line") {
		t.Fatalf("log: %s", out)
	}
}

// devices that failed together spread their retries: the backoff over ±20 %,
// though it doubles from its own value, and a wait the server asked for over
// 0 to +20 %, never shorter
func TestRetriesJittered(t *testing.T) {
	srv := otlptest.New(t)
	srv.Script(otlptest.Response{Drop: true}, otlptest.Response{Drop: true}, otlptest.Response{Drop: true},
		otlptest.Response{Status: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"7"}}},
		otlptest.Response{Status: http.StatusOK})
	p, clk, _ := newPipeline(t, srv)
	var mu sync.Mutex
	draws := []float64{0, 0.25, 0.625, 0.5}
	logs.SetRand(p, func() float64 {
		mu.Lock()
		defer mu.Unlock()
		r := draws[0]
		if len(draws) > 1 {
			draws = draws[1:]
		}
		return r
	})
	run(t, p)
	waiting(t, clk)
	p.Emit(info("one"))
	clk.Advance(2 * time.Second)
	for _, want := range []time.Duration{800 * time.Millisecond, 1800 * time.Millisecond, 4200 * time.Millisecond, 7700 * time.Millisecond} {
		srv.Next(t)
		d := clk.next(t)
		if (d - want).Abs() > time.Microsecond {
			t.Fatalf("waits %v, want %v", d, want)
		}
		clk.Advance(d)
	}
	if got := bodies(t, srv.Next(t)); len(got) != 1 || got[0] != "one" {
		t.Fatalf("delivered = %v", got)
	}
}

// delivered lets the exporter's retries fire until a request is answered,
// and returns its records.
func delivered(t *testing.T, srv *otlptest.Server, clk *fakeClock) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r, ok := srv.TryNext(20 * time.Millisecond); ok && !r.Answer.Drop {
			return bodies(t, r)
		}
		select {
		case d := <-clk.afters:
			clk.Advance(d)
		default:
		}
	}
	t.Fatalf("nothing delivered (%s)", srv.Connections())
	return nil
}

// waiting checks that the exporter waits for its next 2 s batch; the test fires it with Advance.
func waiting(t *testing.T, clk *fakeClock) {
	t.Helper()
	if d := clk.next(t); d != 2*time.Second {
		t.Fatalf("waits %v, want the 2 s batch", d)
	}
}

func TestRedirectRefused(t *testing.T) {
	other := otlptest.New(t)
	srv := otlptest.New(t)
	srv.Respond(func(otlptest.Request) otlptest.Response {
		return otlptest.Response{Status: http.StatusTemporaryRedirect, Header: http.Header{"Location": {other.URL + "/v2/otlp/v1/logs"}}}
	})
	p, clk, _ := newPipeline(t, srv)
	run(t, p)
	waiting(t, clk)
	p.Emit(info("one"))
	clk.Advance(2 * time.Second)
	srv.Next(t)
	// a refused redirect is an error: the batch stays and the exporter backs off
	if d := clk.next(t); d != time.Second {
		t.Fatalf("waits %v", d)
	}
	other.None(t, 50*time.Millisecond)
	if st := p.Stats(); st.RingBytes == 0 {
		t.Fatalf("batch dropped: %+v", st)
	}
}

func TestBadCertificateRefused(t *testing.T) {
	srv := otlptest.New(t)
	p, clk, _ := newPipelineTrusting(t, srv, otlptest.OtherCA(t))
	run(t, p)
	waiting(t, clk)
	p.Emit(info("one"))
	clk.Advance(2 * time.Second)
	if d := clk.next(t); d != time.Second {
		t.Fatalf("waits %v", d)
	}
	srv.None(t, 50*time.Millisecond)
	if st := p.Stats(); st.RingBytes == 0 {
		t.Fatalf("batch dropped: %+v", st)
	}
}

// the server down while more than the ring is logged: the oldest drop, and
// once up the backlog drains oldest first; every record is delivered or
// counted dropped, once
func TestOutageDropsOldestThenDrains(t *testing.T) {
	srv := otlptest.New(t)
	var down atomic.Bool
	down.Store(true)
	srv.Respond(func(otlptest.Request) otlptest.Response {
		if down.Load() {
			return otlptest.Response{Drop: true}
		}
		return otlptest.Response{Status: http.StatusOK}
	})
	p, clk, _ := newPipeline(t, srv)
	run(t, p)
	const n = 1200
	pad := strings.Repeat("y", 3990)
	for i := range n {
		p.Emit(info(fmt.Sprintf("%06d %s", i, pad)))
	}
	// the batch the first 512 KiB started has failed once the exporter backs off: none of it is in flight
	clk.skipTo(t, time.Second)
	st := p.Stats()
	recs := held(t, p)
	size := st.RingBytes / uint64(len(recs))
	keep := uint64(4<<20) / size
	if st.RingBytes > 4<<20 || uint64(len(recs)) != keep {
		t.Fatalf("ring: %+v with %d records of %d bytes, want %d kept", st, len(recs), size, keep)
	}
	down.Store(false)
	got := drain(t, srv, clk, int(keep))
	for i, b := range got {
		if want := fmt.Sprintf("%06d ", uint64(i)+n-keep); !strings.HasPrefix(b, want) {
			t.Fatalf("record %d starts %q, want %q", i, b[:7], want)
		}
	}
	// the failed batch's records were dropped from the ring and never sent: they count at the retry
	if st := settled(t, p); st.Dropped != n-keep {
		t.Fatalf("after drain: %+v, want %d dropped", st, n-keep)
	}
}

// a batch in flight while the ring drops its records still reaches the
// server: its records count as delivered, not dropped
func TestInFlightDropsDeliveredNotCounted(t *testing.T) {
	srv := otlptest.New(t)
	var first atomic.Bool
	first.Store(true)
	release, entered := make(chan struct{}), make(chan otlptest.Request, 1)
	srv.Respond(func(r otlptest.Request) otlptest.Response {
		if first.CompareAndSwap(true, false) {
			entered <- r
			<-release
		}
		return otlptest.Response{Status: http.StatusOK}
	})
	p, clk, _ := newPipeline(t, srv)
	run(t, p)
	const n = 1600
	pad := strings.Repeat("y", 3990)
	i := 0
	for ; p.Stats().RingBytes < 512<<10; i++ {
		p.Emit(info(fmt.Sprintf("%06d %s", i, pad)))
	}
	inFlight := len(bodies(t, <-entered))
	for ; i < n; i++ {
		p.Emit(info(fmt.Sprintf("%06d %s", i, pad)))
	}
	recs := held(t, p)
	keep := len(recs)
	if index6(t, recs[0].GetBody().GetStringValue()) < inFlight {
		t.Fatalf("the ring still holds part of the batch in flight")
	}
	close(release)
	// the batch in flight first, whole, then what the ring kept
	got := drain(t, srv, clk, inFlight+keep)
	if index6(t, got[0]) != 0 || index6(t, got[inFlight-1]) != inFlight-1 || index6(t, got[inFlight]) != n-keep {
		t.Fatalf("delivered %d..%d, then from %d, want from %d", index6(t, got[0]), index6(t, got[inFlight-1]), index6(t, got[inFlight]), n-keep)
	}
	if st := settled(t, p); st.Dropped != uint64(n-keep-inFlight) {
		t.Fatalf("dropped %d, want %d: %d sent in flight, %d drained of %d", st.Dropped, n-keep-inFlight, inFlight, keep, n)
	}
}

func index6(t *testing.T, body string) int {
	t.Helper()
	var i int
	if _, err := fmt.Sscanf(body[:6], "%06d", &i); err != nil {
		t.Fatal(err)
	}
	return i
}

// drain lets the exporter's waits fire until want records were answered 200, and returns them.
func drain(t *testing.T, srv *otlptest.Server, clk *fakeClock, want int) []string {
	t.Helper()
	var got []string
	deadline := time.Now().Add(20 * time.Second)
	for len(got) < want && time.Now().Before(deadline) {
		clk.Advance(time.Minute)
		if r, ok := srv.TryNext(20 * time.Millisecond); ok && !r.Answer.Drop {
			got = append(got, bodies(t, r)...)
		}
	}
	if len(got) != want {
		t.Fatalf("drained %d of %d (%s)", len(got), want, srv.Connections())
	}
	return got
}

// settled waits for the ring to empty: the fake has a request before the exporter has its answer and acks.
func settled(t *testing.T, p *logs.Pipeline) logs.Stats {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for p.Stats().RingBytes != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	st := p.Stats()
	if st.RingBytes != 0 {
		t.Fatalf("not drained: %+v", st)
	}
	return st
}

// a server that never answers holds the exporter, never the sources
func TestEmitNeverBlocks(t *testing.T) {
	srv := otlptest.New(t)
	release, entered := make(chan struct{}), make(chan struct{}, 1)
	t.Cleanup(func() { close(release) })
	srv.Respond(func(otlptest.Request) otlptest.Response {
		entered <- struct{}{}
		<-release
		return otlptest.Response{Status: http.StatusOK}
	})
	p, clk, _ := newPipeline(t, srv)
	run(t, p)
	waiting(t, clk)
	p.Emit(info("held"))
	clk.Advance(2 * time.Second)
	<-entered
	done := make(chan struct{})
	go func() {
		body := strings.Repeat("z", 4000)
		for range 3000 {
			p.Emit(info(body))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("emit blocked")
	}
	if st := p.Stats(); st.Dropped == 0 || st.RingBytes > 4<<20 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestConcurrentEmit(t *testing.T) {
	srv := otlptest.New(t)
	p, clk, _ := newPipeline(t, srv)
	run(t, p)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 300 {
				p.Emit(info(fmt.Sprintf("g%d-%d", g, i)))
				if i%50 == 0 {
					p.SetLevels(map[string]logs.Severity{"kmsg": logs.Warn}, logs.Info)
					_ = p.Stats()
				}
			}
		})
	}
	stop := make(chan struct{})
	pumped := make(chan struct{})
	go func() {
		defer close(pumped)
		for {
			select {
			case <-stop:
				return
			case <-clk.afters:
				clk.Advance(2 * time.Second)
			}
		}
	}()
	wg.Wait()
	got := 0
	deadline := time.Now().Add(10 * time.Second)
	for got < 8*300 && time.Now().Before(deadline) {
		if r, ok := srv.TryNext(50 * time.Millisecond); ok {
			got += len(bodies(t, r))
		}
	}
	close(stop)
	<-pumped
	if got != 8*300 {
		t.Fatalf("delivered %d of %d", got, 8*300)
	}
}
