package uniproxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

const (
	maxMessageBytes  = 1 << 20
	mediaPrefixBytes = 4
	// a legacy json class shares the socket with protobuf: never protobuf-parse it
	jsonOpen         = '{'
	speechkitVersion = "4.16.1"
	closeWindow      = 2 * time.Second
)

// the stock speaker's factory identity; the upgrade headers and AppInfo both carry it
const (
	factoryAppID        = "aliced"
	factoryAppVersion   = "1.0"
	factoryPlatform     = "Linux"
	factoryManufacturer = "Yandex"
	factoryOSVersion    = "1.0"
)

// outcome is why a socket ended; never upstream text
type outcome string

const (
	outcomeInvalidAuth outcome = "invalid_auth"
	outcomeException   outcome = "exception"
	outcomeGoAway      outcome = "go_away"
	outcomeClosed      outcome = "closed"
	outcomeProtocol    outcome = "protocol"
	// the stream's own failures, marked on the socket before it closes it
	outcomeASRError     outcome = "asr_error"
	outcomeStreamClosed outcome = "stream_closed"
	outcomeTimeout      outcome = "timeout"
	outcomeBadStream    outcome = "bad_stream"
	outcomeDecode       outcome = "decode"
)

var terminalEvents = map[string]outcome{
	evInvalidAuth: outcomeInvalidAuth, evEventException: outcomeException, evGoAway: outcomeGoAway,
}

// dialError carries the http status a refused upgrade answered, or the dial failure's class (dns, tls, timeout…),
// never the url, headers or body
type dialError struct {
	status int
	detail string
}

func (e dialError) Error() string {
	if e.status == 0 {
		return "uniproxy: connect failed"
	}
	return "uniproxy: connect failed (http " + strconv.Itoa(e.status) + ")"
}

// handlers run on the transport's one read goroutine, in arrival order
type handlers struct {
	message func(incoming)
	media   func(streamID uint32, b []byte)
	// end runs once when the read loop stops: the ending, its outcome "" after the caller's own close
	end func(ending)
}

type transport struct {
	ws       *websocket.Conn
	ctx      context.Context
	cancel   context.CancelFunc
	h        handlers
	log      *slog.Logger
	module   string
	failCode string

	wmu     sync.Mutex
	mu      sync.Mutex
	closing bool
	done    chan struct{}

	// the socket's span and what it counts; ending is the read loop's alone
	sp     *socketSpan
	ending ending
}

func (c *config) app() appInfo {
	return appInfo{
		appID: factoryAppID, appVersion: factoryAppVersion, osVersion: factoryOSVersion, platform: factoryPlatform,
		uuid: c.uuid, deviceID: c.deviceID, lang: c.lang, deviceModel: c.deviceModel,
		deviceManufacturer: factoryManufacturer, firmware: c.firmware,
	}
}

func (c *config) upgradeHeaders() http.Header {
	h := http.Header{}
	h.Set("X-Uprx-Oauth-Token", c.oauthToken)
	h.Set("X-Uprx-Auth-Token", c.authToken)
	h.Set("X-Uprx-Device-Id", c.deviceID)
	if c.uid != "" {
		h.Set("X-Uprx-Uid", c.uid)
	}
	h.Set("X-Uprx-Uuid", c.uuid)
	// a fresh session id per socket
	h.Set("X-Uprx-Ssid", uuid.NewString())
	h.Set("X-Uprx-App-Id", factoryAppID)
	h.Set("X-Uprx-App-Version", factoryAppVersion)
	h.Set("X-Uprx-Platform", factoryPlatform)
	h.Set("X-Uprx-Device-Model", c.deviceModel)
	h.Set("X-Uprx-Device-Manufacturer", factoryManufacturer)
	h.Set("X-Uprx-Firmware", c.firmware)
	h.Set("X-Uprx-Os-Version", factoryOSVersion)
	h.Set("X-Uprx-Speechkit-Version", speechkitVersion)
	h.Set("X-Uprx-Connection-Priority", "UserAction")
	h.Set("X-Uprx-Retry-Count", "0")
	h.Set("Accept", "application/protobuf")
	return h
}

// dial opens a socket and sends SynchronizeState; the server takes requests before it answers, so nothing waits for it.
// ctx bounds the handshake alone: the socket then lives until close or the server ends it
func dial(ctx context.Context, c *config, h handlers) (*transport, error) {
	sctx, sp := startSocketSpan(ctx, c)
	hctx, cancel := context.WithTimeout(sctx, c.handshakeTimeout)
	defer cancel()
	ws, resp, err := websocket.Dial(hctx, c.url, &websocket.DialOptions{
		HTTPClient: c.client, HTTPHeader: c.upgradeHeaders(), CompressionMode: websocket.CompressionNoContextTakeover,
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		e := dialError{detail: "dial_" + tracing.ErrorCode(err)}
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			e.status = resp.StatusCode
			e.detail = "dial_status_" + strconv.Itoa(resp.StatusCode)
		}
		sp.dialFailed(c.log, err, e)
		return nil, e
	}
	sp.opened()
	ws.SetReadLimit(maxMessageBytes)
	tctx, tcancel := context.WithCancel(context.Background())
	t := &transport{ws: ws, ctx: tctx, cancel: tcancel, h: h, log: c.log, module: c.module, failCode: c.failCode,
		done: make(chan struct{}), sp: sp, ending: ending{closeCode: -1}}
	if err := t.sendEvent(synchronizeState(c.oauthToken, c.authToken, c.app())); err != nil {
		t.close()
		return nil, dialError{detail: "sync_failed"}
	}
	go t.read()
	return t, nil
}

// detail is the dial failure's cause as a token, for the stream's caller
func (e dialError) cause() string {
	if e.detail == "" {
		return "dial_error"
	}
	return e.detail
}

func (t *transport) send(b []byte) error {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	err := t.ws.Write(t.ctx, websocket.MessageBinary, b)
	if err == nil {
		t.sp.sent(len(b))
	}
	return err
}

func (t *transport) sendEvent(m []byte) error {
	return t.send(append(append([]byte(nil), aapi...), m...))
}

func (t *transport) sendMedia(streamID uint32, b []byte) error {
	frame := make([]byte, mediaPrefixBytes, mediaPrefixBytes+len(b))
	binary.BigEndian.PutUint32(frame, streamID)
	err := t.send(append(frame, b...))
	if err == nil {
		t.sp.audioSent()
	}
	return err
}

// close ends the socket; end then runs with "" and close waits for the read loop
func (t *transport) close() {
	t.mu.Lock()
	t.closing = true
	t.mu.Unlock()
	cctx, cancel := context.WithTimeout(context.Background(), closeWindow)
	defer cancel()
	go func() {
		<-cctx.Done()
		_ = t.ws.CloseNow()
	}()
	_ = t.ws.Close(websocket.StatusNormalClosure, "")
	t.cancel()
	select {
	case <-t.done:
	case <-cctx.Done():
	}
}

func (t *transport) read() {
	defer func() {
		t.mu.Lock()
		closing := t.closing
		t.mu.Unlock()
		end := t.ending
		if closing {
			end.outcome = ""
		}
		_ = t.ws.CloseNow()
		t.cancel()
		close(t.done)
		// the socket's span ends with what failed: the server's end, else a failure the stream marked
		t.sp.end(t.log, end)
		if t.h.end != nil {
			t.h.end(end)
		}
	}()
	if providers.Guard(t.log, t.module, t.failCode, func() { t.ending.outcome = t.readLoop() }) {
		t.ending.outcome = outcomeProtocol
	}
}

// mark records why the stream failed on the socket's span, before the stream closes the socket
func (t *transport) mark(o outcome, detail string, kv ...attribute.KeyValue) {
	if t != nil {
		t.sp.mark(o, detail, kv...)
	}
}

func (t *transport) readLoop() outcome {
	for {
		typ, frame, err := t.ws.Read(t.ctx)
		if err != nil {
			t.ending.closed(err)
			return outcomeClosed
		}
		t.sp.gotFrame()
		// a legacy json frame, or one too short for a prefix: nothing to route
		if typ != websocket.MessageBinary || len(frame) < mediaPrefixBytes || frame[0] == jsonOpen {
			continue
		}
		if !bytes.Equal(frame[:mediaPrefixBytes], aapi) {
			t.sp.audioReceived()
			if t.h.media != nil {
				t.h.media(binary.BigEndian.Uint32(frame), frame[mediaPrefixBytes:])
			}
			continue
		}
		in, err := parseServerMessage(frame[mediaPrefixBytes:])
		if err != nil {
			return outcomeProtocol
		}
		if !in.control {
			if in.header.hasAck {
				_ = t.sendEvent(ack(in.header))
			}
			if o, ok := terminalEvents[in.name]; ok {
				t.ending.event = in.name
				return o
			}
			t.sp.milestone(in.name)
			// well formed but nothing cadence acts on
			if in.name == evUnknown {
				continue
			}
		}
		if t.h.message != nil {
			t.h.message(in)
		}
	}
}
