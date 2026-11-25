package events

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

type fakePinger struct{}

func (fakePinger) Ping(context.Context) error { return nil }

type feedHarness struct {
	bus      *Bus
	feed     *Feed
	logs     *lockedBuffer
	tokens   *fakeTokens
	sessions *ownerSessions
	url      string
	// the server's own origin, which the request's Host gives
	origin string
}

// ownerSessions takes one session cookie token until a logout revokes it.
type ownerSessions struct {
	mu      sync.Mutex
	revoked bool
}

func (*ownerSessions) Login(context.Context, string, []string, string) (auth.Grant, error) {
	return auth.Grant{}, auth.ErrUnauthorized
}

func (s *ownerSessions) Validate(_ context.Context, token string) (auth.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == "owner-cookie" && !s.revoked {
		return auth.Session{ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	return auth.Session{}, auth.ErrUnauthorized
}

func (s *ownerSessions) Logout(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked = s.revoked || token == "owner-cookie"
	return nil
}

// fakeTokens are the rig's bearers: adm, ha and met tokens of those scopes,
// until a test changes them; fail makes every check a store error.
type fakeTokens struct {
	mu     sync.Mutex
	scopes map[string]auth.Scopes
	fail   error
	// while set, a check waits for it to close or for its context to end
	hold chan struct{}
}

func (f *fakeTokens) verify(ctx context.Context, bearer string) (auth.Grants, error) {
	f.mu.Lock()
	hold := f.hold
	f.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return auth.Grants{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return auth.Grants{}, f.fail
	}
	if s, ok := f.scopes[bearer]; ok {
		return auth.Grants{Scopes: s}, nil
	}
	return auth.Grants{}, auth.ErrUnauthorized
}

// hang makes every check wait until release.
func (f *fakeTokens) hang() (release func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	hold := make(chan struct{})
	f.hold = hold
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.hold = nil
		close(hold)
	}
}

// set gives bearer scopes; none revokes it.
func (f *fakeTokens) set(bearer string, scopes ...auth.Scope) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(scopes) == 0 {
		delete(f.scopes, bearer)
		return
	}
	f.scopes[bearer] = scopes
}

func (f *fakeTokens) failWith(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = err
}

func startFeed(t *testing.T, buffer int, edit ...func(*Feed)) *feedHarness {
	t.Helper()
	h := &feedHarness{bus: NewBus(buffer), logs: &lockedBuffer{}, sessions: &ownerSessions{}, tokens: &fakeTokens{scopes: map[string]auth.Scopes{
		"adm": auth.AdminScopes, "ha": {auth.ScopeHA}, "met": {auth.ScopeMetrics},
	}}}
	log := slog.New(slog.NewJSONHandler(h.logs, nil))
	h.feed = NewFeed(h.bus, log)
	for _, e := range edit {
		e(h.feed)
	}
	app := api.NewApp(api.Options{
		DB: fakePinger{}, Version: "t",
		VerifyToken: h.tokens.verify,
		Sessions:    h.sessions,
		Admin:       []func(huma.API){func(a huma.API) { api.RegisterSession(a, h.sessions) }},
		Plain:       []api.PlainRoute{h.feed.Route()},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	h.url = "ws://" + ln.Addr().String() + "/api/v1/live"
	h.origin = "http://" + ln.Addr().String()
	return h
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// dial opens a feed as the admin token and reads its first frame, the
// subscription's confirmation.
func (h *feedHarness) dial(t *testing.T, query string) *websocket.Conn {
	t.Helper()
	return h.dialAs(t, "adm", query)
}

func (h *feedHarness) dialAs(t *testing.T, bearer, query string) *websocket.Conn {
	t.Helper()
	opts := &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + bearer}}}
	c, _, err := websocket.Dial(ctxT(t), h.url+query, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	if m := readFrame(t, c); len(m) != 1 || m["type"] != "subscribed" {
		t.Fatalf("first frame %v", m)
	}
	return c
}

func readFrame(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	typ, b, err := c.Read(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("frame type %v", typ)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// waitSubs blocks until the bus has n subscribers: the dial returns before
// the handler subscribes.
func (h *feedHarness) waitSubs(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.bus.mu.RLock()
		got := len(h.bus.subs)
		h.bus.mu.RUnlock()
		if got == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("subscribers %d, want %d", got, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestFeedRequiresBearer(t *testing.T) {
	h := startFeed(t, 8)
	for name, hdr := range map[string]http.Header{
		"none":  nil,
		"wrong": {"Authorization": {"Bearer nope"}},
	} {
		_, resp, err := websocket.Dial(ctxT(t), h.url, &websocket.DialOptions{HTTPHeader: hdr})
		if err == nil || resp == nil {
			t.Fatalf("%s: dial succeeded", name)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 401 || string(b) != `{"error":"unauthorized"}` {
			t.Fatalf("%s: %d %s", name, resp.StatusCode, b)
		}
	}
	h.waitSubs(t, 0)
}

// the owner's cookie opens the feed only from the server's own origin, so
// another site's page cannot read it; the bearer's upgrade is unchanged (M7a-D5)
func TestFeedCookieNeedsSameOrigin(t *testing.T) {
	h := startFeed(t, 8)
	cookie := "__Host-cadence_session=owner-cookie"
	for _, tc := range []struct {
		name   string
		header http.Header
		opens  bool
	}{
		{"cookie, cross origin", http.Header{"Cookie": {cookie}, "Origin": {"https://evil.example"}}, false},
		{"cookie, no origin", http.Header{"Cookie": {cookie}}, false},
		{"cookie, same origin", http.Header{"Cookie": {cookie}, "Origin": {h.origin}}, true},
		{"bearer, cross origin", http.Header{"Authorization": {"Bearer adm"}, "Origin": {"https://evil.example"}}, true},
		{"bearer, no origin", http.Header{"Authorization": {"Bearer adm"}}, true},
	} {
		c, resp, err := websocket.Dial(ctxT(t), h.url, &websocket.DialOptions{HTTPHeader: tc.header})
		if !tc.opens {
			if err == nil || resp == nil {
				t.Fatalf("%s: dial succeeded", tc.name)
			}
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != 403 || string(b) != `{"error":"csrf"}` {
				t.Fatalf("%s: %d %s", tc.name, resp.StatusCode, b)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if m := readFrame(t, c); m["type"] != "subscribed" {
			t.Fatalf("%s: first frame %v", tc.name, m)
		}
		_ = c.CloseNow()
	}
}

func TestFeedStreamsEvents(t *testing.T) {
	h := startFeed(t, 8)
	c := h.dial(t, "?deviceId=d1")
	h.waitSubs(t, 1)
	at := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	h.bus.Publish(Event{Type: "turn_start", DeviceID: "d2", At: at})
	h.bus.Publish(Event{Type: "tool_call", DeviceID: "d1", SessionID: "s1", TurnID: "t1", At: at, Data: map[string]any{"name": "weather"}})
	m := readFrame(t, c)
	if m["type"] != "tool_call" || m["deviceId"] != "d1" || m["sessionId"] != "s1" || m["turnId"] != "t1" ||
		m["at"] != "2026-11-08T10:00:00Z" || m["data"].(map[string]any)["name"] != "weather" {
		t.Fatalf("frame %v", m)
	}
}

// a peer that read the confirmation misses no event published after it
func TestFeedConfirmsSubscription(t *testing.T) {
	h := startFeed(t, 8)
	c := h.dial(t, "?deviceId=d1")
	h.bus.Publish(Event{Type: "memory_recall", DeviceID: "d1", Data: map[string]any{"count": 1}})
	if m := readFrame(t, c); m["type"] != "memory_recall" {
		t.Fatalf("frame %v", m)
	}
}

func TestFeedReportsDrops(t *testing.T) {
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	h := startFeed(t, 2, func(f *Feed) {
		f.beforeWrite = func() {
			select {
			case entered <- struct{}{}:
				<-gate
			default:
			}
		}
	})
	c := h.dial(t, "")
	h.waitSubs(t, 1)
	h.bus.Publish(Event{Type: "e1"})
	<-entered
	for _, ty := range []string{"e2", "e3", "e4", "e5", "e6"} {
		h.bus.Publish(Event{Type: ty})
	}
	close(gate)
	var got []string
	for len(got) < 4 {
		_, b, err := c.Read(ctxT(t))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		s, _ := m["type"].(string)
		// these bytes exactly: the home assistant component's fake server sends them (it has no fixture of them)
		if s == "dropped" && string(b) != `{"type":"dropped","count":3}` {
			t.Fatalf("dropped frame %s", b)
		}
		got = append(got, s)
	}
	want := []string{"e1", "dropped", "e5", "e6"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("frames %v, want %v", got, want)
		}
	}
}

func TestFeedClosesOnShutdown(t *testing.T) {
	h := startFeed(t, 8)
	c := h.dial(t, "")
	h.waitSubs(t, 1)
	go h.feed.Close(int(websocket.StatusGoingAway), "")
	_, _, err := c.Read(ctxT(t))
	if websocket.CloseStatus(err) != websocket.StatusGoingAway {
		t.Fatalf("close %v", err)
	}
	h.waitSubs(t, 0)
	h.waitLog(t, `"msg":"feed_closed","reason":"shutdown"`)
	// later links are refused too
	c2, _, err := websocket.Dial(ctxT(t), h.url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer adm"}}})
	if err == nil {
		_, _, err = c2.Read(ctxT(t))
	}
	if err == nil {
		t.Fatal("link accepted after close")
	}
}

func TestFeedDropsIdlePeer(t *testing.T) {
	h := startFeed(t, 8, func(f *Feed) { f.IdleTimeout = 150 * time.Millisecond; f.PingInterval = time.Hour })
	c := h.dial(t, "")
	h.waitSubs(t, 1)
	// the client never reads, so it never pongs
	_ = c
	h.waitSubs(t, 0)
}

// fakeNow is a clock a test moves by hand
type fakeNow struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeNow) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeNow) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// log lines pass at most lineRate a second to each peer that subscribed to
// logs; the rest are dropped and counted in one log_line_dropped a second,
// other events untouched; a peer on the default topics hears of neither
func TestFeedThrottlesLogLines(t *testing.T) {
	clock := &fakeNow{now: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	h := startFeed(t, 512, func(f *Feed) { f.now = clock.Now; f.lineEvery = 50 * time.Millisecond })
	c := h.dial(t, "")
	other := h.dial(t, "?deviceId=d2")
	quiet := h.dial(t, "")
	h.waitSubs(t, 3)
	subscribe(t, c, `{"subscribe":["tasks","logs"]}`, `{"type":"subscribed","topics":["tasks","logs"]}`)
	subscribe(t, other, `{"subscribe":["logs"]}`, `{"type":"subscribed","topics":["logs"],"deviceId":"d2"}`)
	for i := range 80 {
		h.bus.Publish(Event{Type: LogLine, DeviceID: "d1", Data: map[string]any{"n": i}})
	}
	h.bus.Publish(Event{Type: "task_event", DeviceID: "d1"})
	h.bus.Publish(Event{Type: LogLine, DeviceID: "d2", Data: map[string]any{"n": 0}})
	lines, dropped, task := 0, 0.0, false
	for !task || dropped < 31 {
		m := readFrame(t, c)
		switch m["type"] {
		case "log_line_dropped":
			dropped += m["data"].(map[string]any)["count"].(float64)
		case "task_event":
			task = true
		}
		if m["type"] == LogLine {
			if n := m["data"].(map[string]any)["n"].(float64); int(n) != lines && m["deviceId"] == "d1" {
				t.Fatalf("line %v after %d", n, lines)
			}
			lines++
		}
	}
	// 50 of d1's lines, then the task event, then d2's line was the 81st log line
	if lines != 50 || dropped != 31 {
		t.Fatalf("passed %d, dropped %v", lines, dropped)
	}
	// the other peer filters by device first: its one line passes
	if m := readFrame(t, other); m["type"] != LogLine || m["deviceId"] != "d2" {
		t.Fatalf("other peer %v", m)
	}
	// a new second lets lines through again, and nothing was dropped since
	clock.Add(time.Second)
	h.bus.Publish(Event{Type: LogLine, DeviceID: "d1", Data: map[string]any{"n": 99}})
	if m := readFrame(t, c); m["type"] != LogLine || m["data"].(map[string]any)["n"] != 99.0 {
		t.Fatalf("after a second %v", m)
	}
	// the default feed got the task events alone, no line and no count of them
	h.bus.Publish(Event{Type: "task_event", DeviceID: "d1"})
	if got := strings.Join(readTypes(t, quiet, 2), ","); got != "task_event,task_event" {
		t.Fatalf("default feed read %s", got)
	}
}
