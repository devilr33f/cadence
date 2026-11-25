package events

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func send(t *testing.T, c *websocket.Conn, msg string) {
	t.Helper()
	if err := c.Write(ctxT(t), websocket.MessageText, []byte(msg)); err != nil {
		t.Fatal(err)
	}
}

// subscribe sends msg and wants the answer frame want, as written
func subscribe(t *testing.T, c *websocket.Conn, msg, want string) {
	t.Helper()
	send(t, c, msg)
	_, b, err := c.Read(ctxT(t))
	if err != nil || string(b) != want {
		t.Fatalf("%s: answered %s %v, want %s", msg, b, err, want)
	}
}

// one event of each topic, in this order: the first one a feed reads tells
// what it selects
var oneOfEach = []Event{
	{Type: "transcript", DeviceID: "d1"},
	{Type: "tool_call", DeviceID: "d1"},
	{Type: TaskEvent, DeviceID: "d1"},
	{Type: LogLine, DeviceID: "d1"},
	{Type: MediaState, DeviceID: "d1"},
}

func (h *feedHarness) publishAll(events ...Event) {
	for _, e := range events {
		h.bus.Publish(e)
	}
}

// reads the types of the next n frames
func readTypes(t *testing.T, c *websocket.Conn, n int) []string {
	t.Helper()
	var got []string
	for range n {
		m := readFrame(t, c)
		s, _ := m["type"].(string)
		got = append(got, s)
	}
	return got
}

// without a subscribe the feed writes every topic its scope allows but logs,
// which only a subscription to it gets (Ruling M7a-R5): all the others for
// admin, devices, media and vitals for ha (M7a-D7, M8-D3)
func TestFeedDefaultsToTheScopesTopics(t *testing.T) {
	h := startFeed(t, 16)
	adm := h.dial(t, "")
	ha := h.dialAs(t, "ha", "")
	h.waitSubs(t, 2)
	h.publishAll(oneOfEach...)
	if got := strings.Join(readTypes(t, adm, 4), ","); got != "transcript,tool_call,task_event,media_state" {
		t.Fatalf("admin read %s", got)
	}
	if got := readTypes(t, ha, 1); got[0] != MediaState {
		t.Fatalf("ha read %v", got)
	}
}

// presence, volume and vitals reach an ha feed without a subscription, and one
// that subscribed to them; a cookie session subscribed to logs alone reads none
// of them (M8-D2, M8-D3)
func TestFeedPresenceAndVitalsReachHA(t *testing.T) {
	h := startFeed(t, 32)
	ha := h.dialAs(t, "ha", "")
	vitals := h.dialAs(t, "ha", "")
	opts := &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {"__Host-cadence_session=owner-cookie"}, "Origin": {h.origin}}}
	owner, _, err := websocket.Dial(ctxT(t), h.url, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.CloseNow() })
	if m := readFrame(t, owner); m["type"] != "subscribed" {
		t.Fatalf("first frame %v", m)
	}
	h.waitSubs(t, 3)
	subscribe(t, vitals, `{"subscribe":["vitals"]}`, `{"type":"subscribed","topics":["vitals"]}`)
	subscribe(t, owner, `{"subscribe":["logs"]}`, `{"type":"subscribed","topics":["logs"]}`)
	presence := []string{DeviceAdded, DeviceOnline, DeviceVolume, DeviceRenamed, DeviceVitals, DeviceOffline, DeviceDeleted}
	for _, typ := range presence {
		h.bus.Publish(Event{Type: typ, DeviceID: "d1"})
	}
	h.bus.Publish(Event{Type: LogLine, DeviceID: "d1"})
	if got := strings.Join(readTypes(t, ha, len(presence)), ","); got != strings.Join(presence, ",") {
		t.Fatalf("ha read %s", got)
	}
	if got := readTypes(t, vitals, 1); got[0] != DeviceVitals {
		t.Fatalf("vitals read %v", got)
	}
	if got := readTypes(t, owner, 1); got[0] != LogLine {
		t.Fatalf("the logs feed read %v", got)
	}
	subscribe(t, ha, `{"subscribe":["devices","media","vitals"]}`, `{"type":"subscribed","topics":["devices","media","vitals"]}`)
}

// a subscribe narrows the feed, answers what it selects, and a later one
// replaces it
func TestFeedSubscribeNarrowsAndReplaces(t *testing.T) {
	h := startFeed(t, 16)
	c := h.dial(t, "")
	h.waitSubs(t, 1)
	subscribe(t, c, `{"subscribe":["tasks","agent","tasks"]}`, `{"type":"subscribed","topics":["agent","tasks"]}`)
	h.publishAll(oneOfEach...)
	if got := strings.Join(readTypes(t, c, 2), ","); got != "tool_call,task_event" {
		t.Fatalf("read %s", got)
	}
	subscribe(t, c, `{"subscribe":["media"]}`, `{"type":"subscribed","topics":["media"]}`)
	h.publishAll(oneOfEach...)
	if got := readTypes(t, c, 1); got[0] != MediaState {
		t.Fatalf("after replace read %v", got)
	}
	// nothing at all is a subscription too: the feed then writes control frames only
	subscribe(t, c, `{"subscribe":[]}`, `{"type":"subscribed","topics":[]}`)
	h.publishAll(oneOfEach...)
	subscribe(t, c, `{"subscribe":["logs"]}`, `{"type":"subscribed","topics":["logs"]}`)
	h.publishAll(oneOfEach...)
	if got := readTypes(t, c, 1); got[0] != LogLine {
		t.Fatalf("read %v", got)
	}
}

// an explicit subscription leaves out event types no topic maps; only the
// admin's default feed has them
func TestFeedSubscriptionLeavesOutUnmappedTypes(t *testing.T) {
	h := startFeed(t, 16)
	c := h.dial(t, "")
	h.waitSubs(t, 1)
	subscribe(t, c, `{"subscribe":["devices","sessions","agent","tasks","media","logs"]}`,
		`{"type":"subscribed","topics":["devices","sessions","agent","tasks","media","logs"]}`)
	h.publishAll(Event{Type: "something_new"}, Event{Type: "turn_end"})
	if got := readTypes(t, c, 1); got[0] != "turn_end" {
		t.Fatalf("read %v", got)
	}
}

// an unknown topic, or one the scope does not allow, is answered with an
// error frame; the connection and the subscription before stay
func TestFeedRefusesTopics(t *testing.T) {
	h := startFeed(t, 16)
	c := h.dialAs(t, "ha", "")
	h.waitSubs(t, 1)
	subscribe(t, c, `{"subscribe":["media","bogus"]}`, `{"error":"topic_unknown","detail":"bogus"}`)
	subscribe(t, c, `{"subscribe":["Not A Token!"]}`, `{"error":"topic_unknown"}`)
	subscribe(t, c, `{"subscribe":["media","sessions"]}`, `{"error":"forbidden","detail":"sessions"}`)
	subscribe(t, c, `{"subscribe":["agent"]}`, `{"error":"forbidden","detail":"agent"}`)
	h.publishAll(oneOfEach...)
	if got := readTypes(t, c, 1); got[0] != MediaState {
		t.Fatalf("read %v", got)
	}
	subscribe(t, c, `{"subscribe":["devices"]}`, `{"type":"subscribed","topics":["devices"]}`)
	out := h.logs.String()
	if !strings.Contains(out, `"msg":"api_forbidden","op":"live","scope":"admin","topic":"sessions"`) || strings.Contains(out, "bogus") ||
		strings.Contains(out, "Not A Token") {
		t.Fatalf("log %s", out)
	}
}

// a frame that is no subscribe is answered invalid, the connection kept
func TestFeedAnswersInvalidFrames(t *testing.T) {
	h := startFeed(t, 16)
	c := h.dial(t, "")
	h.waitSubs(t, 1)
	for _, msg := range []string{`not json`, `{"subscribe":"agent"}`, `{"subscribe":["agent"],"x":1}`, `null`, `{}`,
		`{"subscribe":null}`, `{"subscribe":["agent"]} {}`, `[]`, `{"subscribe":["agent"],"deviceId":3}`} {
		subscribe(t, c, msg, `{"error":"invalid"}`)
	}
	if err := c.Write(ctxT(t), websocket.MessageBinary, []byte(`{"subscribe":["agent"]}`)); err != nil {
		t.Fatal(err)
	}
	if _, b, err := c.Read(ctxT(t)); err != nil || string(b) != `{"error":"invalid"}` {
		t.Fatalf("binary: %s %v", b, err)
	}
	h.publishAll(Event{Type: "tool_call"})
	if got := readTypes(t, c, 1); got[0] != "tool_call" {
		t.Fatalf("read %v", got)
	}
}

// the query's deviceId keeps filtering; a subscribe's overrides it, "" means
// every device, and a subscribe without one falls back to the query's
func TestFeedSubscribeDeviceOverridesTheQuery(t *testing.T) {
	h := startFeed(t, 16)
	c := h.dial(t, "?deviceId=d1")
	h.waitSubs(t, 1)
	subscribe(t, c, `{"subscribe":["agent"],"deviceId":"d2"}`, `{"type":"subscribed","topics":["agent"],"deviceId":"d2"}`)
	h.publishAll(Event{Type: "tool_call", DeviceID: "d1"}, Event{Type: "llm_text", DeviceID: "d2"})
	if m := readFrame(t, c); m["type"] != "llm_text" || m["deviceId"] != "d2" {
		t.Fatalf("frame %v", m)
	}
	subscribe(t, c, `{"subscribe":["agent"]}`, `{"type":"subscribed","topics":["agent"],"deviceId":"d1"}`)
	h.publishAll(Event{Type: "llm_text", DeviceID: "d2"}, Event{Type: "tool_call", DeviceID: "d1"})
	if m := readFrame(t, c); m["type"] != "tool_call" || m["deviceId"] != "d1" {
		t.Fatalf("frame %v", m)
	}
	subscribe(t, c, `{"subscribe":["agent"],"deviceId":""}`, `{"type":"subscribed","topics":["agent"]}`)
	h.publishAll(Event{Type: "llm_text", DeviceID: "d2"})
	if m := readFrame(t, c); m["deviceId"] != "d2" {
		t.Fatalf("frame %v", m)
	}
}

// a client frame past 4 KiB closes the feed with 1008; one of 4 KiB is read
func TestFeedClosesOnOversizedFrames(t *testing.T) {
	h := startFeed(t, 16)
	c := h.dial(t, "")
	h.waitSubs(t, 1)
	msg := `{"subscribe":["agent"]}`
	subscribe(t, c, msg+strings.Repeat(" ", maxClientFrame-len(msg)), `{"type":"subscribed","topics":["agent"]}`)
	send(t, c, msg+strings.Repeat(" ", maxClientFrame+1-len(msg)))
	if _, _, err := c.Read(ctxT(t)); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("read %v", err)
	}
	h.waitSubs(t, 0)
	if !strings.Contains(h.logs.String(), `"msg":"feed_violation","reason":"too_large"`) {
		t.Fatalf("log %s", h.logs.String())
	}
}

// ten client frames in a second are read; the eleventh closes the feed with 1008
func TestFeedClosesOnFloods(t *testing.T) {
	h := startFeed(t, 16)
	c := h.dial(t, "")
	h.waitSubs(t, 1)
	for range maxClientFrames {
		send(t, c, `{"subscribe":["agent"]}`)
	}
	for range maxClientFrames {
		if _, b, err := c.Read(ctxT(t)); err != nil || string(b) != `{"type":"subscribed","topics":["agent"]}` {
			t.Fatalf("answer %s %v", b, err)
		}
	}
	send(t, c, `{"subscribe":["agent"]}`)
	if _, _, err := c.Read(ctxT(t)); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("read %v", err)
	}
	h.waitSubs(t, 0)
	if !strings.Contains(h.logs.String(), `"msg":"feed_violation","reason":"rate_limited"`) {
		t.Fatalf("log %s", h.logs.String())
	}
}

// a token whose scopes allow no topic gets no feed
func TestFeedRefusesATokenWithoutTopics(t *testing.T) {
	h := startFeed(t, 8)
	_, resp, err := websocket.Dial(ctxT(t), h.url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer met"}}})
	if err == nil || resp == nil {
		t.Fatal("dial succeeded")
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 403 || string(b) != `{"error":"forbidden"}` {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	h.waitSubs(t, 0)
}

// pings count in the bound too: the eleventh in a second closes the feed
func TestFeedCountsPingsInTheBound(t *testing.T) {
	h := startFeed(t, 16)
	c := h.dial(t, "")
	h.waitSubs(t, 1)
	ctx := ctxT(t)
	closed := make(chan error, 1)
	go func() {
		for {
			if _, _, err := c.Read(ctx); err != nil {
				closed <- err
				return
			}
		}
	}()
	for i := range maxClientFrames {
		if err := c.Ping(ctx); err != nil {
			t.Fatalf("ping %d: %v", i+1, err)
		}
	}
	_ = c.Ping(ctx)
	if err := <-closed; websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("read %v", err)
	}
	h.waitSubs(t, 0)
	if !strings.Contains(h.logs.String(), `"msg":"feed_violation","reason":"rate_limited"`) {
		t.Fatalf("log %s", h.logs.String())
	}
}

// the feed tells whether any peer subscribed to logs, so the log store
// publishes nothing while none tails: a default feed does not count, a
// subscription counts once, and a peer counts no more once it narrowed or went
func TestFeedSaysWhetherAPeerTailsLogs(t *testing.T) {
	h := startFeed(t, 16)
	tails := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for h.feed.LogsSubscribed() != want {
			if time.Now().After(deadline) {
				t.Fatalf("logs subscribed %v, want %v", !want, want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	a := h.dial(t, "")
	b := h.dial(t, "")
	h.waitSubs(t, 2)
	tails(false)
	subscribe(t, a, `{"subscribe":["logs"]}`, `{"type":"subscribed","topics":["logs"]}`)
	tails(true)
	subscribe(t, a, `{"subscribe":["logs","tasks"]}`, `{"type":"subscribed","topics":["tasks","logs"]}`)
	subscribe(t, b, `{"subscribe":["logs"]}`, `{"type":"subscribed","topics":["logs"]}`)
	subscribe(t, a, `{"subscribe":["tasks"]}`, `{"type":"subscribed","topics":["tasks"]}`)
	tails(true)
	_ = b.CloseNow()
	h.waitSubs(t, 1)
	tails(false)
	// a refused subscription leaves the count as it was
	subscribe(t, a, `{"subscribe":["logs"]}`, `{"type":"subscribed","topics":["logs"]}`)
	subscribe(t, a, `{"subscribe":["nope"]}`, `{"error":"topic_unknown","detail":"nope"}`)
	tails(true)
	_ = a.CloseNow()
	h.waitSubs(t, 0)
	tails(false)
}
