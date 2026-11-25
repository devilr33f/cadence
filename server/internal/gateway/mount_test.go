package gateway_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gofiber/fiber/v3"
	"google.golang.org/protobuf/encoding/protowire"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
)

const (
	tokenOne    = "cdd_one"
	tokenTwo    = "cdd_two"
	tokenOutage = "cdd_outage"
	deviceOne   = "dev-1"
	deviceTwo   = "dev-2"
)

func fakeAuth(_ context.Context, token string) (string, error) {
	switch token {
	case tokenOne:
		return deviceOne, nil
	case tokenTwo:
		return deviceTwo, nil
	case tokenOutage:
		return "", errors.New("db down")
	}
	return "", auth.ErrUnauthorized
}

type harness struct {
	url    string
	hub    *gateway.Hub
	logs   *recorder
	closed chan string
	seen   chan string
}

// start serves the gateway; edit swaps in the handlers a test needs.
func start(t *testing.T, edit ...func(*gateway.Handlers)) *harness {
	t.Helper()
	h := &harness{hub: gateway.NewHub(), logs: newRecorder(), closed: make(chan string, 8), seen: make(chan string, 8)}
	hs := gateway.Handlers{
		OnHello: func(context.Context, gateway.Conn, *cadencev2.Hello) *cadencev2.Welcome {
			return &cadencev2.Welcome{ServerVersion: "test", SendFullReport: true}
		},
		OnMessage: func(context.Context, gateway.Conn, *cadencev2.Envelope) {},
		OnClose:   func(id string) { h.closed <- id },
		Seen:      func(_ context.Context, id string) { h.seen <- id },
	}
	for _, e := range edit {
		e(&hs)
	}
	app := fiber.New()
	gateway.Mount(app.Group("/v2"), h.hub, fakeAuth, hs, slog.New(h.logs))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	h.url = "ws://" + ln.Addr().String() + "/v2/device"
	return h
}

func timeout(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func (h *harness) dial(t *testing.T, token string) *websocket.Conn {
	t.Helper()
	return h.dialWith(t, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
}

func (h *harness) dialWith(t *testing.T, opts *websocket.DialOptions) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.Dial(timeout(t), h.url, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

func send(t *testing.T, c *websocket.Conn, env *cadencev2.Envelope) {
	t.Helper()
	b, err := wire.Encode(env)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Write(timeout(t), websocket.MessageBinary, b); err != nil {
		t.Fatal(err)
	}
}

func recv(t *testing.T, c *websocket.Conn) *cadencev2.Envelope {
	t.Helper()
	_, b, err := c.Read(timeout(t))
	if err != nil {
		t.Fatal(err)
	}
	env, err := wire.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func hello() *cadencev2.Envelope { return helloAt(2) }

func helloAt(protocol uint32) *cadencev2.Envelope {
	return &cadencev2.Envelope{Body: &cadencev2.Envelope_Hello{Hello: &cadencev2.Hello{Protocol: protocol, Board: "sim"}}}
}

func greet(t *testing.T, c *websocket.Conn) *cadencev2.Welcome {
	t.Helper()
	send(t, c, hello())
	w := recv(t, c).GetWelcome()
	if w == nil {
		t.Fatal("first frame is not a welcome")
	}
	return w
}

// closeStatus reads until the link ends and returns the server's close code.
func closeStatus(t *testing.T, c *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx := timeout(t)
	for {
		if _, _, err := c.Read(ctx); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func next(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
		return ""
	}
}

func stateDelta() *cadencev2.Envelope {
	return &cadencev2.Envelope{Body: &cadencev2.Envelope_StateDelta{StateDelta: &cadencev2.StateDelta{Capability: "volume", Json: "{}"}}}
}

func TestRejectsMissingToken(t *testing.T) {
	h := start(t)
	for _, tc := range []struct {
		name, header, body string
		status             int
	}{
		{"missing", "", `{"error":"unauthorized"}`, http.StatusUnauthorized},
		{"unknown", "Bearer cdd_nope", `{"error":"unauthorized"}`, http.StatusUnauthorized},
		{"outage", "Bearer " + tokenOutage, `{"error":"unavailable"}`, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := &websocket.DialOptions{}
			if tc.header != "" {
				opts.HTTPHeader = http.Header{"Authorization": {tc.header}}
			}
			_, resp, err := websocket.Dial(timeout(t), h.url, opts)
			if err == nil {
				t.Fatal("upgraded")
			}
			if resp == nil {
				t.Fatalf("no response: %v", err)
			}
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tc.status || string(b) != tc.body {
				t.Fatalf("got %d %s", resp.StatusCode, b)
			}
		})
	}
	h.logs.wait(t, "auth_unavailable")
}

// a protocol-3 daemon (one that can run its own update plans) links like a 2 one: the server
// refuses only what it cannot speak
func TestHelloProtocol3Links(t *testing.T) {
	h := start(t)
	c := h.dial(t, tokenOne)
	send(t, c, helloAt(3))
	if env := recv(t, c); env.GetWelcome() == nil {
		t.Fatalf("first frame %v, want a welcome", env.GetBody())
	}
	if !h.hub.Online(deviceOne) {
		t.Fatal("a protocol-3 hello was not linked")
	}
}

func TestHelloWelcome(t *testing.T) {
	h := start(t)
	w := greet(t, h.dial(t, tokenOne))
	if w.GetServerVersion() != "test" || !w.GetSendFullReport() {
		t.Fatalf("welcome %v", w)
	}
	if got := next(t, h.seen); got != deviceOne {
		t.Fatalf("seen %q", got)
	}
	if !h.hub.Online(deviceOne) {
		t.Fatal("not online after welcome")
	}
	if r := h.logs.wait(t, "link_open"); r.attrs["device_id"] != deviceOne {
		t.Fatalf("link_open %v", r.attrs)
	}
}

// newerType is an envelope whose body only a newer peer knows.
func newerType() []byte {
	return protowire.AppendVarint(protowire.AppendTag(nil, 99, protowire.VarintType), 1)
}

func TestFirstMessageMustBeHello(t *testing.T) {
	notHello, err := wire.Encode(stateDelta())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		frame []byte
	}{
		{"garbage", []byte{0xff, 0xff}},
		{"not hello", notHello},
		{"unknown type", newerType()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := start(t)
			c := h.dial(t, tokenOne)
			if err := c.Write(timeout(t), websocket.MessageBinary, tc.frame); err != nil {
				t.Fatal(err)
			}
			if got := closeStatus(t, c); got != websocket.StatusInvalidFramePayloadData {
				t.Fatalf("close %v", got)
			}
			r := h.logs.wait(t, "link_invalid")
			if len(r.attrs) != 1 || r.attrs["device_id"] != deviceOne {
				t.Fatalf("link_invalid %v", r.attrs)
			}
			if h.hub.Online(deviceOne) {
				t.Fatal("online without a hello")
			}
		})
	}
}

func TestUnknownMessageSkipped(t *testing.T) {
	got := make(chan *cadencev2.Envelope, 4)
	h := start(t, func(hs *gateway.Handlers) {
		hs.OnMessage = func(_ context.Context, _ gateway.Conn, env *cadencev2.Envelope) { got <- env }
	})
	c := h.dial(t, tokenOne)
	greet(t, c)
	for range 2 {
		if err := c.Write(timeout(t), websocket.MessageBinary, newerType()); err != nil {
			t.Fatal(err)
		}
	}
	send(t, c, stateDelta())
	select {
	case env := <-got:
		if env.GetStateDelta() == nil {
			t.Fatalf("delivered %v", env)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the link stopped delivering after an unknown frame")
	}
	if !h.hub.Online(deviceOne) || h.logs.contains("link_invalid") {
		t.Fatal("an unknown frame closed the link")
	}
	r := h.logs.wait(t, "link_unknown_message")
	if r.attrs["device_id"] != deviceOne || r.attrs["count"] != "1" {
		t.Fatalf("link_unknown_message %v", r.attrs)
	}
}

func TestOversizeFrameCloses(t *testing.T) {
	h := start(t)
	c := h.dial(t, tokenOne)
	greet(t, c)
	if err := c.Write(timeout(t), websocket.MessageBinary, make([]byte, wire.MaxFrameBytes+1)); err != nil {
		t.Fatal(err)
	}
	if got := closeStatus(t, c); got != websocket.StatusMessageTooBig {
		t.Fatalf("close %v", got)
	}
}

func TestNewConnectionReplacesOld(t *testing.T) {
	h := start(t)
	old := h.dial(t, tokenOne)
	greet(t, old)
	cur := h.dial(t, tokenOne)
	greet(t, cur)
	if got := closeStatus(t, old); got != websocket.StatusGoingAway {
		t.Fatalf("old close %v", got)
	}
	if !h.hub.Online(deviceOne) {
		t.Fatal("offline after replacement")
	}
	if err := h.hub.Send(deviceOne, stateDelta()); err != nil {
		t.Fatal(err)
	}
	if recv(t, cur).GetStateDelta() == nil {
		t.Fatal("hub did not reach the new connection")
	}
}

func TestOldCloseKeepsNewConnection(t *testing.T) {
	h := start(t)
	old := h.dial(t, tokenOne)
	greet(t, old)
	cur := h.dial(t, tokenOne)
	greet(t, cur)
	_ = old.CloseNow()
	h.logs.wait(t, "link_closed")
	if !h.hub.Online(deviceOne) {
		t.Fatal("old close unregistered the new connection")
	}
	select {
	case id := <-h.closed:
		t.Fatalf("OnClose(%s) for the replaced connection", id)
	default:
	}
	_ = cur.Close(websocket.StatusNormalClosure, "")
	if got := next(t, h.closed); got != deviceOne {
		t.Fatalf("OnClose(%s)", got)
	}
	if h.hub.Online(deviceOne) {
		t.Fatal("online after the last close")
	}
}

func TestMessagesSerializeWithReplacement(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var mu sync.Mutex
	var order []string
	var oldCtx context.Context
	messages, hellos := 0, 0
	h := start(t, func(hs *gateway.Handlers) {
		hs.OnHello = func(context.Context, gateway.Conn, *cadencev2.Hello) *cadencev2.Welcome {
			mu.Lock()
			defer mu.Unlock()
			if hellos++; hellos == 2 {
				order = append(order, fmt.Sprintf("hello, old ctx done %v", oldCtx.Err() != nil))
			}
			return &cadencev2.Welcome{}
		}
		hs.OnMessage = func(ctx context.Context, _ gateway.Conn, _ *cadencev2.Envelope) {
			mu.Lock()
			messages++
			first := messages == 1
			if first {
				oldCtx = ctx
			}
			mu.Unlock()
			if first {
				entered <- struct{}{}
				<-release
				mu.Lock()
				order = append(order, "message done")
				mu.Unlock()
			}
		}
	})
	old := h.dial(t, tokenOne)
	greet(t, old)
	send(t, old, stateDelta())
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("message not delivered")
	}

	cur := h.dial(t, tokenOne)
	send(t, cur, hello())
	ctx := timeout(t)
	welcomed := make(chan error, 1)
	go func() {
		_, _, err := cur.Read(ctx)
		welcomed <- err
	}()
	next(t, h.seen)
	next(t, h.seen) // the new link is about to attach
	select {
	case err := <-welcomed:
		t.Fatalf("new link read %v while the old link's message ran", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-welcomed; err != nil {
		t.Fatal(err)
	}
	// sent after the replacement, so it must not be delivered
	send(t, old, stateDelta())
	if got := closeStatus(t, old); got != websocket.StatusGoingAway {
		t.Fatalf("old close %v", got)
	}
	h.logs.wait(t, "link_closed")
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"message done", "hello, old ctx done true"}; !slices.Equal(order, want) || messages != 1 {
		t.Fatalf("order %q, %d messages", order, messages)
	}
}

func TestHubCloseEndsLinks(t *testing.T) {
	h := start(t)
	one := h.dial(t, tokenOne)
	greet(t, one)
	two := h.dial(t, tokenTwo)
	greet(t, two)
	ctx := timeout(t)
	got := make(chan websocket.CloseError, 2)
	for _, c := range []*websocket.Conn{one, two} {
		go func() {
			_, _, err := c.Read(ctx)
			var ce websocket.CloseError
			errors.As(err, &ce)
			got <- ce
		}()
	}
	h.hub.Close(1001, "restart")
	for range 2 {
		select {
		case ce := <-got:
			if ce.Code != websocket.StatusGoingAway || ce.Reason != "restart" {
				t.Fatalf("close %v %q", ce.Code, ce.Reason)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("link not closed")
		}
	}
	if h.hub.Online(deviceOne) || h.hub.Online(deviceTwo) {
		t.Fatal("online after Close")
	}
	late := h.dial(t, tokenOne)
	send(t, late, hello())
	if got := closeStatus(t, late); got != websocket.StatusGoingAway {
		t.Fatalf("late link close %v", got)
	}
	if h.hub.Online(deviceOne) {
		t.Fatal("late link online after Close")
	}
}

func TestProtocolMismatch1002(t *testing.T) {
	for _, v := range []uint32{1, 4} {
		t.Run(fmt.Sprint(v), func(t *testing.T) {
			h := start(t)
			c := h.dial(t, tokenOne)
			send(t, c, helloAt(v))
			if got := closeStatus(t, c); got != websocket.StatusProtocolError {
				t.Fatalf("close %v", got)
			}
			r := h.logs.wait(t, "link_refused")
			if len(r.attrs) != 2 || r.attrs["device_id"] != deviceOne || r.attrs["reason"] != "protocol" {
				t.Fatalf("link_refused %v", r.attrs)
			}
			if h.hub.Online(deviceOne) || len(h.seen) != 0 {
				t.Fatal("a hello of another protocol was accepted")
			}
		})
	}
}

const idle = 150 * time.Millisecond

func lively(hs *gateway.Handlers) { hs.PingInterval, hs.IdleTimeout = 20*time.Millisecond, idle }

func TestSilentPeerGoesOffline(t *testing.T) {
	h := start(t, lively)
	c := h.dial(t, tokenOne)
	began := time.Now()
	greet(t, c)
	// the client reads no more, so it answers none of the server's pings
	if got := next(t, h.closed); got != deviceOne {
		t.Fatalf("OnClose(%s)", got)
	}
	if took := time.Since(began); took < idle {
		t.Fatalf("offline after %v, before the idle timeout", took)
	}
	if h.hub.Online(deviceOne) {
		t.Fatal("silent link still online")
	}
	if r := h.logs.wait(t, "link_idle"); len(r.attrs) != 1 || r.attrs["device_id"] != deviceOne {
		t.Fatalf("link_idle %v", r.attrs)
	}
}

func TestPongKeepsLink(t *testing.T) {
	h := start(t, lively)
	var pings atomic.Int32
	c := h.dialWith(t, &websocket.DialOptions{
		HTTPHeader:     http.Header{"Authorization": {"Bearer " + tokenOne}},
		OnPingReceived: func(context.Context, []byte) bool { pings.Add(1); return true },
	})
	greet(t, c)
	ctx := timeout(t)
	// reading answers the pings; the client sends nothing else
	go func() {
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	}()
	time.Sleep(3 * idle)
	if !h.hub.Online(deviceOne) || h.logs.contains("link_idle") {
		t.Fatal("a link that answers its pings went idle")
	}
	if n := pings.Load(); n < 3 {
		t.Fatalf("%d pings in %v", n, 3*idle)
	}
}

func TestPanicLogsEventOnly(t *testing.T) {
	h := start(t, func(hs *gateway.Handlers) {
		hs.OnMessage = func(context.Context, gateway.Conn, *cadencev2.Envelope) { panic("secret-xyz") }
	})
	c := h.dial(t, tokenOne)
	greet(t, c)
	send(t, c, stateDelta())
	if r := h.logs.wait(t, "link_panic"); len(r.attrs) != 1 || r.attrs["device_id"] != deviceOne {
		t.Fatalf("link_panic %v", r.attrs)
	}
	if h.logs.contains("secret-xyz") {
		t.Fatal("panic value logged")
	}
	if got := next(t, h.closed); got != deviceOne {
		t.Fatalf("OnClose(%s)", got)
	}
	if h.hub.Online(deviceOne) {
		t.Fatal("panicked link still online")
	}
}

// a deleted device's link ends with 1008 and its services hear it gone; one
// its token opened before the delete never attaches after it
func TestRemoveEndsTheDevicesLink(t *testing.T) {
	hellos := make(chan string, 4)
	h := start(t, func(hs *gateway.Handlers) {
		hs.OnHello = func(_ context.Context, c gateway.Conn, _ *cadencev2.Hello) *cadencev2.Welcome {
			hellos <- c.DeviceID()
			return &cadencev2.Welcome{}
		}
	})
	linked := h.dial(t, tokenOne)
	greet(t, linked)
	next(t, hellos)
	// upgraded, so past the token check, but not yet attached
	waiting := h.dial(t, tokenOne)
	other := h.dial(t, tokenTwo)
	greet(t, other)
	next(t, hellos)

	h.hub.Remove(deviceOne)
	if got := closeStatus(t, linked); got != websocket.StatusPolicyViolation {
		t.Fatalf("linked close %v", got)
	}
	if got := next(t, h.closed); got != deviceOne {
		t.Fatalf("OnClose(%s)", got)
	}
	send(t, waiting, hello())
	if got := closeStatus(t, waiting); got != websocket.StatusPolicyViolation {
		t.Fatalf("waiting close %v", got)
	}
	h.logs.waitN(t, "link_closed", 2)
	select {
	case id := <-hellos:
		t.Fatalf("hello of %s after the remove", id)
	case id := <-h.closed:
		t.Fatalf("OnClose(%s) for a refused link", id)
	default:
	}
	if h.hub.Online(deviceOne) {
		t.Fatal("removed device online")
	}
	if err := h.hub.Send(deviceTwo, stateDelta()); err != nil {
		t.Fatal(err)
	}
	if recv(t, other).GetStateDelta() == nil {
		t.Fatal("another device's link did not stay")
	}
}

type record struct {
	msg   string
	attrs map[string]string
}

// recorder is a slog handler the tests wait on.
type recorder struct {
	mu   sync.Mutex
	recs []record
	note chan struct{}
}

func newRecorder() *recorder { return &recorder{note: make(chan struct{}, 1)} }

func (*recorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *recorder) WithAttrs([]slog.Attr) slog.Handler     { return r }
func (r *recorder) WithGroup(string) slog.Handler          { return r }

func (r *recorder) Handle(_ context.Context, rec slog.Record) error {
	attrs := map[string]string{}
	rec.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	r.mu.Lock()
	r.recs = append(r.recs, record{rec.Message, attrs})
	r.mu.Unlock()
	select {
	case r.note <- struct{}{}:
	default:
	}
	return nil
}

func (r *recorder) contains(s string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.recs {
		if strings.Contains(rec.msg, s) {
			return true
		}
		for _, v := range rec.attrs {
			if strings.Contains(v, s) {
				return true
			}
		}
	}
	return false
}

func (r *recorder) wait(t *testing.T, msg string) record {
	t.Helper()
	return r.waitN(t, msg, 1)
}

// waitN returns the nth record of msg once it is logged.
func (r *recorder) waitN(t *testing.T, msg string, n int) record {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		r.mu.Lock()
		seen := 0
		for _, rec := range r.recs {
			if rec.msg == msg {
				if seen++; seen == n {
					r.mu.Unlock()
					return rec
				}
			}
		}
		r.mu.Unlock()
		select {
		case <-r.note:
		case <-deadline:
			t.Fatalf("%d %s logs, want %d", seen, msg, n)
		}
	}
}
