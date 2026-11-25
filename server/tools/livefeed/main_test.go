package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// subscribed is the feed's first frame
const subscribed = `{"type":"subscribed"}`

// feed serves one connection with the bearer "adm" and sends it frames.
func feed(t *testing.T, frames ...string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer adm" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		for _, f := range frames {
			if c.Write(r.Context(), websocket.MessageText, []byte(f)) != nil {
				return
			}
		}
		// held open until the peer goes
		_, _, _ = c.Read(r.Context())
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func within(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), d)
	t.Cleanup(cancel)
	return ctx
}

func TestWaitsForCount(t *testing.T) {
	url := feed(t, subscribed,
		`{"type":"transcript","data":{"text":"private words"}}`,
		`{"type":"memory_recall","data":{"count":0,"pinned":0}}`,
		`{"type":"dropped","count":3}`,
		`{"type":"memory_recall","data":{"count":2,"pinned":1}}`)
	var out bytes.Buffer
	if err := run(within(t, 5*time.Second), url, "adm", "memory_recall", "", 1, &out); err != nil {
		t.Fatal(err)
	}
	// the type and the count only, never an event's data
	if got := out.String(); got != "connected\nmemory_recall count=2\n" {
		t.Fatalf("out %q", got)
	}
}

func TestNoMatchInTime(t *testing.T) {
	url := feed(t, subscribed, `{"type":"memory_recall","data":{"count":0}}`)
	var out bytes.Buffer
	err := run(within(t, 300*time.Millisecond), url, "adm", "memory_recall", "", 1, &out)
	if err == nil || err.Error() != "no memory_recall with count >= 1 in time" {
		t.Fatalf("err %v", err)
	}
	if out.String() != "connected\n" {
		t.Fatalf("out %q", out.String())
	}
}

// "connected" means subscribed: without the feed's confirmation first, the
// helper says neither, and an event before it does not count
func TestNeedsTheSubscriptionConfirmed(t *testing.T) {
	url := feed(t, `{"type":"memory_recall","data":{"count":2}}`, subscribed)
	var out bytes.Buffer
	err := run(within(t, 5*time.Second), url, "adm", "memory_recall", "", 1, &out)
	if err == nil || err.Error() != "the feed did not confirm its subscription" || out.Len() != 0 {
		t.Fatalf("err %v, out %q", err, out.String())
	}
}

func TestRefusedToken(t *testing.T) {
	var out bytes.Buffer
	err := run(within(t, 5*time.Second), feed(t), "wrong-secret", "memory_recall", "", 1, &out)
	if err == nil || strings.Contains(err.Error(), "wrong-secret") || out.Len() != 0 {
		t.Fatalf("err %v, out %q", err, out.String())
	}
}

// with -source, only an event whose data names that source counts; its data is still never printed
func TestWaitsForSource(t *testing.T) {
	url := feed(t, subscribed,
		`{"type":"log_line","data":{"source":"cadenced","body":"private line"}}`,
		`{"type":"transcript","data":{"source":"sim-probe"}}`,
		`{"type":"log_line","data":{"source":"sim-probe","body":"private probe"}}`)
	var out bytes.Buffer
	if err := run(within(t, 5*time.Second), url, "adm", "log_line", "sim-probe", 0, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "connected\nlog_line count=0\n" {
		t.Fatalf("out %q", got)
	}
}

// scripted serves one connection: it refuses a cross origin with 403, writes the first frame, answers a subscription
// with answer, then sends frames.
func scripted(t *testing.T, answer string, frames ...string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "sid=abc" || r.Header.Get("Origin") != "http://ok" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		_ = c.Write(r.Context(), websocket.MessageText, []byte(subscribed))
		if answer != "" {
			if _, _, err := c.Read(r.Context()); err != nil {
				return
			}
			_ = c.Write(r.Context(), websocket.MessageText, []byte(answer))
		}
		for _, f := range frames {
			_ = c.Write(r.Context(), websocket.MessageText, []byte(f))
		}
		_, _, _ = c.Read(r.Context())
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestCookieOriginAndRefusals(t *testing.T) {
	url := scripted(t, "")
	var out bytes.Buffer
	o := options{url: url, cookie: "sid=abc", origin: "http://evil", expectStatus: 403}
	if err := runOpts(within(t, 5*time.Second), o, &out); err != nil || out.String() != "refused status=403\n" {
		t.Fatalf("err %v, out %q", err, out.String())
	}
	o.origin = "http://ok"
	if err := runOpts(within(t, 5*time.Second), o, &out); err == nil {
		t.Fatal("an upgrade that went through passed as refused")
	}
}

func TestSubscriptionRefusedAndOnly(t *testing.T) {
	var out bytes.Buffer
	o := options{cookie: "sid=abc", origin: "http://ok", subscribe: []string{"agent"}, subscribeSet: true, expectError: "forbidden"}
	o.url = scripted(t, `{"error":"forbidden","detail":"agent"}`)
	if err := runOpts(within(t, 5*time.Second), o, &out); err != nil || out.String() != "refused error=forbidden\n" {
		t.Fatalf("err %v, out %q", err, out.String())
	}
	out.Reset()
	o = options{cookie: "sid=abc", origin: "http://ok", subscribe: []string{"media"}, subscribeSet: true, typ: "media_state",
		only: []string{"media_state"}, linger: 200 * time.Millisecond}
	o.url = scripted(t, subscribed, `{"type":"media_state"}`)
	if err := runOpts(within(t, 5*time.Second), o, &out); err != nil || out.String() != "connected\nmedia_state count=0\nquiet\n" {
		t.Fatalf("err %v, out %q", err, out.String())
	}
	// any other type fails the run, before the event or while it lingers
	for _, frames := range [][]string{
		{`{"type":"media_state"}`, `{"type":"memory_recall"}`},
		{`{"type":"learning_op"}`, `{"type":"media_state"}`},
	} {
		o.url = scripted(t, subscribed, frames...)
		if err := runOpts(within(t, 5*time.Second), o, &out); err == nil || !strings.Contains(err.Error(), "arrived") {
			t.Fatalf("%v: err %v", frames, err)
		}
	}
}

// a feed that closes while the helper lingers is a failure, not quiet: the
// events it would have sent were never seen
func TestLingerFailsWhenTheFeedCloses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_ = c.Write(r.Context(), websocket.MessageText, []byte(subscribed))
		_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"type":"media_state"}`))
		_ = c.Close(websocket.StatusPolicyViolation, "unauthorized")
	}))
	t.Cleanup(srv.Close)
	o := options{url: "ws" + strings.TrimPrefix(srv.URL, "http"), token: "adm", typ: "media_state", linger: 2 * time.Second}
	var out bytes.Buffer
	if err := runOpts(within(t, 5*time.Second), o, &out); err == nil || out.String() != "connected\nmedia_state count=0\n" {
		t.Fatalf("err %v, out %q", err, out.String())
	}
}

// with -trigger, only an event whose data names that trigger counts
func TestWaitsForTrigger(t *testing.T) {
	url := feed(t, subscribed,
		`{"type":"turn_start","data":{"trigger":"wake"}}`,
		`{"type":"turn_start","data":{"trigger":"TRIGGER_BUTTON"}}`,
		`{"type":"turn_start","data":{"trigger":"button"}}`)
	var out bytes.Buffer
	if err := runOpts(within(t, 5*time.Second), options{url: url, token: "adm", typ: "turn_start", trigger: "button"}, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "connected\nturn_start count=0\n" {
		t.Fatalf("out %q", got)
	}
	url = feed(t, subscribed, `{"type":"turn_start","data":{"trigger":"TRIGGER_BUTTON"}}`)
	if err := runOpts(within(t, 300*time.Millisecond), options{url: url, token: "adm", typ: "turn_start", trigger: "button"}, &out); err == nil {
		t.Fatal("a proto name counted")
	}
}
