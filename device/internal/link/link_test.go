package link

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"io"
	stdlog "log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
)

const token = "cdd_test"

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

func timeout(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func server(t *testing.T, useTLS bool, fn http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(fn)
	// failed tls handshakes are the point of some tests; keep their noise out of the output
	srv.Config.ErrorLog = stdlog.New(io.Discard, "", 0)
	if useTLS {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv
}

func trust(srv *httptest.Server) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return pool
}

func accept(t *testing.T, w http.ResponseWriter, r *http.Request) *websocket.Conn {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		t.Error(err)
		return nil
	}
	return c
}

// drain reads until the peer goes away and returns how the link ended.
func drain(c *websocket.Conn) websocket.StatusCode {
	for {
		if _, _, err := c.Read(context.Background()); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func hello() *cadencev2.Envelope {
	return &cadencev2.Envelope{Body: &cadencev2.Envelope_Hello{Hello: &cadencev2.Hello{Protocol: 2, Board: "sim"}}}
}

func welcome() *cadencev2.Envelope {
	return &cadencev2.Envelope{Body: &cadencev2.Envelope_Welcome{Welcome: &cadencev2.Welcome{ServerVersion: "test"}}}
}

func TestDialSendsBearer(t *testing.T) {
	var auth, path atomic.Value
	got := make(chan *cadencev2.Envelope, 1)
	srv := server(t, true, func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		path.Store(r.URL.Path)
		c := accept(t, w, r)
		if c == nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		_, b, err := c.Read(r.Context())
		if err != nil {
			t.Error(err)
			return
		}
		env, err := wire.Decode(b)
		if err != nil {
			t.Error(err)
			return
		}
		got <- env
		out, _ := wire.Encode(welcome())
		_ = c.Write(context.Background(), websocket.MessageBinary, out)
		drain(c)
	})
	s, err := Dialer{URL: srv.URL, Token: token, RootCAs: trust(srv)}.Dial(timeout(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Send(hello()); err != nil {
		t.Fatal(err)
	}
	if env := <-got; !proto.Equal(env, hello()) {
		t.Fatalf("server got %v", env)
	}
	env, err := s.Recv(timeout(t))
	if err != nil || env.GetWelcome().GetServerVersion() != "test" {
		t.Fatalf("recv = %v, %v", env, err)
	}
	if auth.Load() != "Bearer "+token || path.Load() != "/v2/device" {
		t.Fatalf("auth = %q, path = %q", auth.Load(), path.Load())
	}
}

func TestDialUnauthorized(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	closed := make(chan struct{}, 1)
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateClosed {
			closed <- struct{}{}
		}
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	_, err := Dialer{URL: srv.URL, Token: token, RootCAs: trust(srv)}.Dial(timeout(t))
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
	// each attempt has its own transport: a kept-alive connection would leak once per retry
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("refused dial left its connection open")
	}
}

func TestDialRefusesRedirect(t *testing.T) {
	var followed atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/device", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/elsewhere", func(w http.ResponseWriter, r *http.Request) {
		followed.Store(true)
		if c := accept(t, w, r); c != nil {
			_ = c.CloseNow()
		}
	})
	srv := server(t, true, mux.ServeHTTP)
	if _, err := (Dialer{URL: srv.URL, Token: token, RootCAs: trust(srv)}).Dial(timeout(t)); err == nil {
		t.Fatal("dial followed the redirect")
	}
	if followed.Load() {
		t.Fatal("redirect target was requested")
	}
}

func TestDialRejectsUntrustedTLS(t *testing.T) {
	var reached atomic.Bool
	srv := server(t, true, func(http.ResponseWriter, *http.Request) { reached.Store(true) })
	// no RootCAs: the system pool, which does not hold the test server's own cert
	if _, err := (Dialer{URL: srv.URL, Token: token}).Dial(timeout(t)); err == nil {
		t.Fatal("dial accepted an untrusted certificate")
	}
	if reached.Load() {
		t.Fatal("request reached the server")
	}
}

func TestRecvClosesOnInvalidFrame(t *testing.T) {
	status := make(chan websocket.StatusCode, 1)
	srv := server(t, false, func(w http.ResponseWriter, r *http.Request) {
		c := accept(t, w, r)
		if c == nil {
			return
		}
		_ = c.Write(context.Background(), websocket.MessageBinary, []byte{0xff, 0xff})
		status <- drain(c)
	})
	s, err := Dialer{URL: srv.URL, Token: token}.Dial(timeout(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Recv(timeout(t)); !errors.Is(err, wire.ErrInvalid) {
		t.Fatalf("recv err = %v", err)
	}
	if got := <-status; got != websocket.StatusInvalidFramePayloadData {
		t.Fatalf("close status = %v", got)
	}
}

func TestRecvRefusesOversizeFrame(t *testing.T) {
	status := make(chan websocket.StatusCode, 1)
	srv := server(t, false, func(w http.ResponseWriter, r *http.Request) {
		c := accept(t, w, r)
		if c == nil {
			return
		}
		_ = c.Write(context.Background(), websocket.MessageBinary, make([]byte, wire.MaxFrameBytes+1))
		status <- drain(c)
	})
	s, err := Dialer{URL: srv.URL, Token: token}.Dial(timeout(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Recv(timeout(t)); err == nil {
		t.Fatal("oversize frame accepted")
	}
	if got := <-status; got != websocket.StatusMessageTooBig {
		t.Fatalf("close status = %v", got)
	}
}

type fakeHandler struct {
	mu                      sync.Mutex
	connected, disconnected int
	up                      chan int
	msgs                    chan *cadencev2.Envelope
}

func newFakeHandler() *fakeHandler {
	return &fakeHandler{up: make(chan int, 8), msgs: make(chan *cadencev2.Envelope, 8)}
}

func (h *fakeHandler) Connected(context.Context, Session) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.connected++
	// never blocks, so a link that keeps reconnecting cannot wedge the test
	select {
	case h.up <- h.disconnected:
	default:
	}
	return nil
}

func (h *fakeHandler) Message(_ context.Context, _ Session, env *cadencev2.Envelope) error {
	h.msgs <- env
	return nil
}

func (h *fakeHandler) Disconnected() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.disconnected++
}

func (h *fakeHandler) counts() (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.connected, h.disconnected
}

func fast(int) time.Duration { return 10 * time.Millisecond }

// start runs the link until stop or the test's end and waits for it to return.
func start(t *testing.T, d Dialer, h Handler, buf *syncBuf, delay func(int) time.Duration) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, d, h, slog.New(slog.NewJSONHandler(buf, nil)), delay) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("run = %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Error("run did not return after cancel")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func connected(t *testing.T, h *fakeHandler, buf *syncBuf) {
	t.Helper()
	select {
	case <-h.up:
	case <-time.After(5 * time.Second):
		t.Fatalf("no connect; log: %s", buf.String())
	}
}

func TestReconnectAfterServerClose(t *testing.T) {
	var conns atomic.Int32
	srv := server(t, false, func(w http.ResponseWriter, r *http.Request) {
		c := accept(t, w, r)
		if c == nil {
			return
		}
		if conns.Add(1) == 1 {
			_ = c.Close(websocket.StatusGoingAway, "restart")
			return
		}
		drain(c)
	})
	h, buf := newFakeHandler(), &syncBuf{}
	start(t, Dialer{URL: srv.URL, Token: token}, h, buf, fast)
	for i, wantDown := range []int{0, 1} {
		select {
		case down := <-h.up:
			if down != wantDown {
				t.Fatalf("connect %d after %d disconnects, want %d", i+1, down, wantDown)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no connect %d; log: %s", i+1, buf.String())
		}
	}
	// link_up follows Connected
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(buf.String(), `"msg":"link_up"`) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("log: %s", buf.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if out := buf.String(); !strings.Contains(out, `"msg":"link_down","attempt":1,"delay_ms":10,"reason":"closed","code":1001`) {
		t.Fatalf("log: %s", out)
	}
}

func TestUnauthorizedBacksOffAndLogsOnce(t *testing.T) {
	var dials atomic.Int32
	srv := server(t, false, func(w http.ResponseWriter, _ *http.Request) {
		dials.Add(1)
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	})
	var mu sync.Mutex
	var attempts []int
	delay := func(a int) time.Duration {
		mu.Lock()
		defer mu.Unlock()
		attempts = append(attempts, a)
		return 10 * time.Millisecond
	}
	h, buf := newFakeHandler(), &syncBuf{}
	start(t, Dialer{URL: srv.URL, Token: token}, h, buf, delay)
	deadline := time.Now().Add(5 * time.Second)
	for dials.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("dials = %d", dials.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	got := append([]int(nil), attempts[:2]...)
	mu.Unlock()
	if got[0] != 1 || got[1] != 2 {
		t.Fatalf("attempts = %v", got)
	}
	if c, _ := h.counts(); c != 0 {
		t.Fatalf("connected %d times", c)
	}
	out := buf.String()
	if n := strings.Count(out, `"msg":"link_unauthorized"`); n != 1 {
		t.Fatalf("link_unauthorized logged %d times: %s", n, out)
	}
	if !strings.Contains(out, `"msg":"link_down","attempt":2,"delay_ms":10,"reason":"unauthorized"`) {
		t.Fatalf("log: %s", out)
	}
	if strings.Contains(out, token) {
		t.Fatal("log carries the token")
	}
}

func TestUnknownMessageSkipped(t *testing.T) {
	newer := protowire.AppendVarint(protowire.AppendTag(nil, 99, protowire.VarintType), 1)
	srv := server(t, false, func(w http.ResponseWriter, r *http.Request) {
		c := accept(t, w, r)
		if c == nil {
			return
		}
		for range 2 {
			_ = c.Write(context.Background(), websocket.MessageBinary, newer)
		}
		out, _ := wire.Encode(welcome())
		_ = c.Write(context.Background(), websocket.MessageBinary, out)
		drain(c)
	})
	h, buf := newFakeHandler(), &syncBuf{}
	start(t, Dialer{URL: srv.URL, Token: token}, h, buf, fast)
	select {
	case env := <-h.msgs:
		if env.GetWelcome() == nil {
			t.Fatalf("delivered %v", env)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the link stopped delivering after an unknown frame; log: %s", buf.String())
	}
	out := buf.String()
	if strings.Contains(out, `"msg":"link_down"`) {
		t.Fatalf("an unknown frame ended the link: %s", out)
	}
	for _, want := range []string{`"msg":"link_unknown_message","count":1`, `"msg":"link_unknown_message","count":2`} {
		if !strings.Contains(out, want) {
			t.Fatalf("no %s; log: %s", want, out)
		}
	}
}

func TestPingFailureReconnects(t *testing.T) {
	quit := make(chan struct{})
	var conns atomic.Int32
	srv := server(t, false, func(w http.ResponseWriter, r *http.Request) {
		c := accept(t, w, r)
		if c == nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		if conns.Add(1) == 1 {
			// never reads, so the device's pings go unanswered
			<-quit
			return
		}
		drain(c)
	})
	// runs before srv.Close, which would wait for the parked handler
	t.Cleanup(func() { close(quit) })
	h, buf := newFakeHandler(), &syncBuf{}
	start(t, Dialer{URL: srv.URL, Token: token, PingInterval: 20 * time.Millisecond, PingTimeout: 250 * time.Millisecond}, h, buf, fast)
	connected(t, h, buf)
	connected(t, h, buf)
	// the second server reads, which answers the pings, so that link stays up
	time.Sleep(700 * time.Millisecond)
	out := buf.String()
	if !strings.Contains(out, `"msg":"link_down","attempt":1,"delay_ms":10,"reason":"ping"}`) {
		t.Fatalf("log: %s", out)
	}
	if n := strings.Count(out, `"msg":"link_down"`); n != 1 {
		t.Fatalf("link_down logged %d times: %s", n, out)
	}
}

func TestShutdownSends1001(t *testing.T) {
	got := make(chan websocket.CloseError, 1)
	srv := server(t, false, func(w http.ResponseWriter, r *http.Request) {
		c := accept(t, w, r)
		if c == nil {
			return
		}
		for {
			if _, _, err := c.Read(context.Background()); err != nil {
				var ce websocket.CloseError
				errors.As(err, &ce)
				got <- ce
				return
			}
		}
	})
	h, buf := newFakeHandler(), &syncBuf{}
	stop := start(t, Dialer{URL: srv.URL, Token: token}, h, buf, fast)
	connected(t, h, buf)
	stop()
	select {
	case ce := <-got:
		if ce.Code != websocket.StatusGoingAway || ce.Reason != "shutdown" {
			t.Fatalf("server saw close %v %q", ce.Code, ce.Reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server saw no close")
	}
}

func TestShutdownDropsSilentServer(t *testing.T) {
	quit := make(chan struct{})
	srv := server(t, false, func(w http.ResponseWriter, r *http.Request) {
		c := accept(t, w, r)
		if c == nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		// never reads, so the device's close is never answered
		<-quit
	})
	t.Cleanup(func() { close(quit) })
	h, buf := newFakeHandler(), &syncBuf{}
	stop := start(t, Dialer{URL: srv.URL, Token: token, closeWait: 100 * time.Millisecond}, h, buf, fast)
	connected(t, h, buf)
	began := time.Now()
	stop()
	// the library alone would wait 5 s for the close reply
	if took := time.Since(began); took > time.Second {
		t.Fatalf("shutdown took %v", took)
	}
}
