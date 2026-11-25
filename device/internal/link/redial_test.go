package link

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// a redial ends a link that is up and dials again at once: the network under
// it changed, so the old connection is not trusted to notice
func TestRedialDropsAnUpLink(t *testing.T) {
	statuses := make(chan websocket.StatusCode, 4)
	srv := server(t, false, func(w http.ResponseWriter, r *http.Request) {
		if c := accept(t, w, r); c != nil {
			statuses <- drain(c)
		}
	})
	redial := make(chan struct{}, 1)
	h, buf := newFakeHandler(), &syncBuf{}
	// an hour between attempts: only the redial can bring the second link
	start(t, Dialer{URL: srv.URL, Token: token, Redial: redial}, h, buf, func(int) time.Duration { return time.Hour })
	connected(t, h, buf)
	redial <- struct{}{}
	connected(t, h, buf)
	select {
	case code := <-statuses:
		if code != websocket.StatusGoingAway {
			t.Fatalf("first link closed with %v", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first link never closed")
	}
	if out := buf.String(); !strings.Contains(out, `"msg":"link_down","reason":"redial"`) {
		t.Fatalf("log: %s", out)
	}
}

// a redial cuts the backoff short and starts it over
func TestRedialCutsTheWait(t *testing.T) {
	var refuse atomic.Bool
	refuse.Store(true)
	srv := server(t, false, func(w http.ResponseWriter, r *http.Request) {
		if refuse.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if c := accept(t, w, r); c != nil {
			drain(c)
		}
	})
	redial := make(chan struct{}, 1)
	var attempts atomic.Int32
	delay := func(a int) time.Duration {
		attempts.Store(int32(a))
		return time.Hour
	}
	h, buf := newFakeHandler(), &syncBuf{}
	start(t, Dialer{URL: srv.URL, Token: token, Redial: redial}, h, buf, delay)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), `"reason":"dial_failed"`) {
		if time.Now().After(deadline) {
			t.Fatalf("no failed dial; log: %s", buf.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	refuse.Store(false)
	redial <- struct{}{}
	connected(t, h, buf)
	if !strings.Contains(buf.String(), `"msg":"link_redial"`) {
		t.Fatalf("log: %s", buf.String())
	}
}

// a blocked dial fails before it reaches the server
func TestBlockedDialFails(t *testing.T) {
	var dials atomic.Int32
	srv := server(t, false, func(w http.ResponseWriter, r *http.Request) {
		dials.Add(1)
		if c := accept(t, w, r); c != nil {
			drain(c)
		}
	})
	var blocked atomic.Bool
	blocked.Store(true)
	h, buf := newFakeHandler(), &syncBuf{}
	start(t, Dialer{URL: srv.URL, Token: token, Blocked: blocked.Load}, h, buf, fast)
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(buf.String(), `"reason":"dial_failed"`) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("log: %s", buf.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := dials.Load(); n != 0 {
		t.Fatalf("server saw %d dials while blocked", n)
	}
	blocked.Store(false)
	connected(t, h, buf)
}
