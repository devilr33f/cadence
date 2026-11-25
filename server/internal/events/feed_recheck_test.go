package events

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

// the checks run at every ping; 150 ms keeps the tests quick, and the peer's
// pongs (frames too) under the feed's 10 a second
func quickChecks(f *Feed) { f.PingInterval = 150 * time.Millisecond }

// wantRevoked reads until the feed closes and wants 1008 unauthorized and the
// handler gone, logged as a revocation.
func (h *feedHarness) wantRevoked(t *testing.T, c *websocket.Conn) {
	t.Helper()
	var err error
	for err == nil {
		_, _, err = c.Read(ctxT(t))
	}
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.StatusPolicyViolation || ce.Reason != "unauthorized" {
		t.Fatalf("read %v", err)
	}
	h.waitSubs(t, 0)
	h.waitLog(t, `"msg":"feed_closed","reason":"revoked"`)
}

// waitLog blocks until the feed's log holds want, n times when n is given.
func (h *feedHarness) waitLog(t *testing.T, want string, n ...int) {
	t.Helper()
	times := 1
	if len(n) > 0 {
		times = n[0]
	}
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(h.logs.String(), want) < times {
		if time.Now().After(deadline) {
			t.Fatalf("log %s, want %s", h.logs.String(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// a deleted token's open feed closes at the next check
func TestFeedClosesWhenItsTokenIsRevoked(t *testing.T) {
	h := startFeed(t, 8, quickChecks)
	h.tokens.set("gone", auth.ScopeAdmin)
	c := h.dialAs(t, "gone", "")
	h.waitSubs(t, 1)
	h.tokens.set("gone")
	h.wantRevoked(t, c)
}

// a token whose scopes changed since the upgrade, narrowed here, closes too
func TestFeedClosesWhenItsScopesChange(t *testing.T) {
	h := startFeed(t, 8, quickChecks)
	h.tokens.set("wide", auth.ScopeAdmin)
	c := h.dialAs(t, "wide", "")
	h.waitSubs(t, 1)
	h.tokens.set("wide", auth.ScopeHA)
	h.wantRevoked(t, c)
}

// the owner's logout ends the feed its session cookie opened
func TestFeedClosesWhenItsSessionLogsOut(t *testing.T) {
	h := startFeed(t, 8, quickChecks)
	cookie := "__Host-cadence_session=owner-cookie"
	c, _, err := websocket.Dial(ctxT(t), h.url, &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {cookie}, "Origin": {h.origin}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	if m := readFrame(t, c); m["type"] != "subscribed" {
		t.Fatalf("first frame %v", m)
	}
	h.waitSubs(t, 1)
	req, _ := http.NewRequestWithContext(ctxT(t), http.MethodDelete, h.origin+"/api/v1/session", nil)
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Origin", h.origin)
	req.Header.Set("X-Cadence-CSRF", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout %d", resp.StatusCode)
	}
	h.wantRevoked(t, c)
}

// a store that cannot answer leaves the feed open, logs a code and no cause,
// and the next checks still run: a later revocation closes it
func TestFeedKeepsOnAStoreError(t *testing.T) {
	h := startFeed(t, 8, quickChecks)
	h.tokens.set("flaky", auth.ScopeAdmin)
	c := h.dialAs(t, "flaky", "")
	h.waitSubs(t, 1)
	h.tokens.failWith(errors.New("driver says secret"))
	h.waitLog(t, `"msg":"feed_recheck_failed","code":"store"`, 2)
	h.bus.Publish(Event{Type: "tool_call"})
	if m := readFrame(t, c); m["type"] != "tool_call" {
		t.Fatalf("frame %v", m)
	}
	h.tokens.failWith(nil)
	h.tokens.set("flaky")
	h.wantRevoked(t, c)
	if strings.Contains(h.logs.String(), "secret") {
		t.Fatalf("log %s", h.logs.String())
	}
}

// a check that hangs holds nothing up: events still flow, and it gives up
// after its timeout with a code of its own
func TestFeedCheckThatHangsTimesOut(t *testing.T) {
	h := startFeed(t, 8, quickChecks, func(f *Feed) { f.recheckTimeout = 100 * time.Millisecond })
	h.tokens.set("slow", auth.ScopeAdmin)
	c := h.dialAs(t, "slow", "")
	h.waitSubs(t, 1)
	release := h.tokens.hang()
	defer release()
	h.waitLog(t, `"msg":"feed_recheck_failed","code":"timeout"`)
	h.bus.Publish(Event{Type: "tool_call"})
	if m := readFrame(t, c); m["type"] != "tool_call" {
		t.Fatalf("frame %v", m)
	}
}
