package uniproxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	testOAuth    = "oauth-secret-token"
	testAuth     = "auth-secret-token"
	testDeviceID = "device-0123"
)

// fake is a uniproxy server: it checks the upgrade, takes SynchronizeState, then hands every later frame to the
// test's script, which answers through conn
type fake struct {
	t      *testing.T
	srv    *httptest.Server
	status int
	script func(c *fakeConn)
	header chan http.Header
}

type fakeConn struct {
	t    *testing.T
	ws   *websocket.Conn
	ctx  context.Context
	sync []byte
}

func newFake(t *testing.T, script func(c *fakeConn)) *fake {
	t.Helper()
	f := &fake{t: t, script: script, header: make(chan http.Header, 4)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.header <- r.Header.Clone()
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		ws.SetReadLimit(maxMessageBytes)
		c := &fakeConn{t: t, ws: ws, ctx: ctx}
		_, first, err := ws.Read(ctx)
		if err != nil || !bytes.HasPrefix(first, aapi) {
			_ = ws.Close(websocket.StatusPolicyViolation, "")
			return
		}
		c.sync = first[len(aapi):]
		if f.script != nil {
			f.script(c)
		}
		_ = ws.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// config is an instance pointed at the fake
func (f *fake) config(t *testing.T, extra string) *config {
	t.Helper()
	raw := `{"firmware":"1.2.3"` + extra + `}`
	c, err := newConfig(json.RawMessage(raw), map[string]string{
		"oauthToken": testOAuth, "authToken": testAuth, "deviceId": testDeviceID,
	}, "yandex-uniproxy-test", "test_failed")
	if err != nil {
		t.Fatal(err)
	}
	c.url = "ws" + strings.TrimPrefix(f.srv.URL, "http")
	c.client = f.srv.Client()
	return c
}

// next reads the next frame: a control frame's message (aapi stripped) or a media frame's stream and bytes
func (c *fakeConn) next() (control []byte, stream uint32, media []byte, ok bool) {
	_, b, err := c.ws.Read(c.ctx)
	if err != nil {
		return nil, 0, nil, false
	}
	if bytes.HasPrefix(b, aapi) {
		return b[len(aapi):], 0, nil, true
	}
	return nil, binary.BigEndian.Uint32(b), b[mediaPrefixBytes:], true
}

// nextEvent skips media up to the next control frame and returns its event's header and body field
func (c *fakeConn) nextEvent() (eventHeader, int, []byte, bool) {
	for {
		m, _, _, ok := c.next()
		if !ok {
			return eventHeader{}, 0, nil, false
		}
		if m == nil {
			continue
		}
		root, err := decode(m)
		if err != nil {
			c.t.Errorf("client sent a malformed message")
			return eventHeader{}, 0, nil, false
		}
		eb, ok := root.bytes(1)
		if !ok {
			// a stream control
			return eventHeader{}, -1, m, true
		}
		ev, _ := decode(eb)
		hb, _ := ev.bytes(1)
		h, _ := parseHeader(hb)
		for n := range 40 {
			if n == 1 {
				continue
			}
			if body, ok := ev.bytes(protoNum(n)); ok {
				return h, n, body, true
			}
		}
		return h, 0, nil, true
	}
}

func (c *fakeConn) send(b []byte) {
	_ = c.ws.Write(c.ctx, websocket.MessageBinary, b)
}

// event sends a server event: header, then body under the event field n
func (c *fakeConn) event(h eventHeader, n int, body []byte) {
	ev := sub(nil, 1, h.encode())
	ev = sub(ev, protoNum(n), body)
	c.send(append(append([]byte(nil), aapi...), sub(nil, 1, ev)...))
}

func (c *fakeConn) streamClosed(stream uint32, reason uint64) {
	var m []byte
	m = integer(m, 1, uint64(stream))
	m = sub(m, 3, integer(nil, 1, reason))
	c.send(append(append([]byte(nil), aapi...), sub(nil, 2, m)...))
}

// asr is an AsrResult body: code, end of utterance, and one recognition with normalized text
func asr(code uint64, eou bool, normalized string) []byte {
	var b []byte
	b = integer(b, 1, code)
	b = boolean(b, 3, eou)
	return sub(b, 4, str(nil, 3, normalized))
}

func protoNum(n int) protowire.Number { return protowire.Number(n) } //nolint:gosec // small test field numbers
