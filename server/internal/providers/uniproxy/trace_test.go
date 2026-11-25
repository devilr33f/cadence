package uniproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
	"git.shino.boo/devilreef/cadence/server/internal/tracing/tracingtest"
)

// syncBuffer is a log sink the read loop and the test share
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// tracedConfig is the fake's instance with the traced http transport, as httpx gives every provider, and a log
func tracedConfig(t *testing.T, f *fake) (*config, *syncBuffer) {
	t.Helper()
	c := f.config(t, "")
	c.client = &http.Client{Transport: tracing.Transport(c.client.Transport)}
	logs := &syncBuffer{}
	c.log = slog.New(slog.NewTextHandler(logs, nil))
	return c, logs
}

func spanNamed(t *testing.T, spans []tracing.SpanView, name string) tracing.SpanView {
	t.Helper()
	for _, s := range spans {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no span %s", name)
	return tracing.SpanView{}
}

func hasEvent(s tracing.SpanView, name string) bool {
	for _, e := range s.Events {
		if e.Name == name {
			return true
		}
	}
	return false
}

// regression (the owner's stt_unavailable trace): through the traced transport the socket opens, the handshake's
// span ends ok at the upgrade, and a server close says its status on the socket's span, the event and the log
func TestSTTSocketCloseIsTraced(t *testing.T) {
	p := tracingtest.Setup(t)
	f := newFake(t, func(c *fakeConn) {
		h, _, _, ok := c.nextEvent()
		if !ok {
			return
		}
		c.event(eventHeader{refMessageID: h.messageID}, 10, nil)
		c.next()
		_ = c.ws.Close(websocket.StatusInternalError, "overloaded")
	})
	cfg, logs := tracedConfig(t, f)
	ctx, parent := tracing.Start(context.Background(), "voice", "stt.stream")
	st, err := (&stt{cfg: cfg}).Start(ctx, providers.STTOptions{})
	if err != nil {
		t.Fatalf("start through the traced transport: %v", err)
	}
	defer func() { _ = st.Close() }()
	if err := st.Write([]byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	var failed providers.STTEvent
	for e, ok := nextEvent(t, st); ok; e, ok = nextEvent(t, st) {
		if e.Kind == providers.STTFailed {
			failed = e
		}
	}
	if failed.Code != providers.CodeSTTUnavailable || failed.Detail != "closed_1011" {
		t.Fatalf("failed %+v", failed)
	}
	tracing.End(parent, nil)
	spans := tracingtest.Wait(t, p, parent.SpanContext().TraceID().String(), "uniproxy.stt", "GET 127.0.0.1")
	hs := spanNamed(t, spans, "GET 127.0.0.1")
	if hs.Status == tracing.StatusError || hs.Attrs[tracing.AttrHTTPStatus] != int64(101) {
		t.Fatalf("handshake %+v", hs)
	}
	sock := spanNamed(t, spans, "uniproxy.stt")
	if sock.StatusCode != "closed_1011" || sock.Attrs[tracing.AttrOutcome] != "closed" ||
		sock.Attrs[tracing.AttrWSCloseCode] != int64(1011) || sock.Attrs[attrCloseReason] != "overloaded" ||
		sock.Attrs[attrSentFrames].(int64) < 3 || sock.Attrs[attrReceived].(int64) < 1 {
		t.Fatalf("socket %+v", sock)
	}
	for _, e := range []string{"ws_open", "stream_opened", "first_audio_sent", "socket_end"} {
		if !hasEvent(sock, e) {
			t.Errorf("no %s event: %+v", e, sock.Events)
		}
	}
	line := logs.String()
	if !strings.Contains(line, "stt_stream_failed") || !strings.Contains(line, "close_code=1011") ||
		!strings.Contains(line, "detail=closed_1011") {
		t.Fatalf("log %q", line)
	}
	b, _ := json.Marshal(spans)
	for _, secret := range []string{testOAuth, testAuth, testDeviceID, "uni.ws"} {
		if strings.Contains(line, secret) || bytes.Contains(b, []byte(secret)) {
			t.Fatalf("%s leaked", secret)
		}
	}
}

// a recognition's error code is the stream's detail and on the socket's span
func TestSTTAsrErrorIsTraced(t *testing.T) {
	p := tracingtest.Setup(t)
	f := newFake(t, func(c *fakeConn) {
		h, _, _, ok := c.nextEvent()
		if !ok {
			return
		}
		c.event(eventHeader{refMessageID: h.messageID}, 13, asr(3, false, ""))
		c.next()
	})
	cfg, _ := tracedConfig(t, f)
	ctx, parent := tracing.Start(context.Background(), "voice", "stt.stream")
	st, err := (&stt{cfg: cfg}).Start(ctx, providers.STTOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	_ = st.Write([]byte{1})
	e, _ := nextEvent(t, st)
	if e.Kind != providers.STTFailed || e.Code != providers.CodeSTTFailed || e.Detail != "asr_error_3" {
		t.Fatalf("event %+v", e)
	}
	tracing.End(parent, nil)
	sock := spanNamed(t, tracingtest.Wait(t, p, parent.SpanContext().TraceID().String(), "uniproxy.stt"), "uniproxy.stt")
	if sock.Attrs[tracing.AttrOutcome] != "asr_error" || sock.Attrs[attrASRCode] != int64(3) || sock.StatusCode != "asr_error_3" ||
		!hasEvent(sock, "first_result") {
		t.Fatalf("socket %+v", sock)
	}
}

// a refused upgrade keeps its code and names its status in the detail
func TestSTTRefusedUpgradeDetail(t *testing.T) {
	f := newFake(t, nil)
	f.status = 401
	_, err := (&stt{cfg: f.config(t, "")}).Start(context.Background(), providers.STTOptions{})
	var de *providers.DetailError
	if !errors.As(err, &de) || err.Error() != providers.CodeSTTUnavailable || providers.Detail(err) != "dial_status_401" {
		t.Fatalf("err %#v", err)
	}
}
