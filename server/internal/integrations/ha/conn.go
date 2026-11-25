package ha

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/coder/websocket"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// dial connects and authenticates within the handshake timeout, and names the
// version Home Assistant reported ("" for none it could show).
func dial(ctx context.Context, url string, client *http.Client, token string, t timings) (*websocket.Conn, string, string) {
	hctx, cancel := context.WithTimeout(ctx, t.handshake)
	defer cancel()
	ws, _, err := websocket.Dial(hctx, url, &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		return nil, "", transportCode(hctx)
	}
	ws.SetReadLimit(t.readLimit)
	version, code := authenticate(hctx, ws, token)
	if code != "" {
		_ = ws.CloseNow()
		return nil, "", code
	}
	return ws, version, ""
}

// authenticate answers auth_required with the token: the version auth_ok
// reports (else auth_required's), or a code.
func authenticate(ctx context.Context, ws *websocket.Conn, token string) (string, string) {
	m, code := receive(ctx, ws)
	if code != "" {
		return "", code
	}
	if m.Type != typeAuthRequired {
		return "", CodeBadResponse
	}
	version := versionToken(m.HAVersion)
	b, err := authMessage(token)
	if err != nil {
		return "", CodeBadResponse
	}
	if ws.Write(ctx, websocket.MessageText, b) != nil {
		return "", transportCode(ctx)
	}
	if m, code = receive(ctx, ws); code != "" {
		return "", code
	}
	switch m.Type {
	case typeAuthOK:
		if v := versionToken(m.HAVersion); v != "" {
			version = v
		}
		return version, ""
	case typeAuthInvalid:
		return "", CodeAuthFailed
	}
	return "", CodeBadResponse
}

// versionRe is a version as Home Assistant writes one (2026.11.0, 2025.1.0b3,
// 2024.12.0.dev20241101): what the status shows of it, no other text.
var versionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,31}$`)

func versionToken(v string) string {
	if versionRe.MatchString(v) {
		return v
	}
	return ""
}

// receive reads one message; a failure is a code.
func receive(ctx context.Context, ws *websocket.Conn) (inbound, string) {
	typ, b, err := ws.Read(ctx)
	switch {
	case errors.Is(err, websocket.ErrMessageTooBig):
		return inbound{}, CodeBadResponse
	case err != nil:
		return inbound{}, transportCode(ctx)
	}
	var m inbound
	if typ != websocket.MessageText || json.Unmarshal(b, &m) != nil || m.Type == "" {
		return inbound{}, CodeBadResponse
	}
	return m, ""
}

func transportCode(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return CodeTimeout
	}
	return CodeUnavailable
}

// ctxError is a cancellation as the caller's own error and a deadline as timeout.
func ctxError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fail(CodeTimeout)
	}
	return ctx.Err()
}

// conn is one authenticated connection. Ids are taken and frames written
// under wmu, so ids reach Home Assistant in increasing order (it refuses any
// other); the read goroutine matches replies to commands by id.
type conn struct {
	ws   *websocket.Conn
	log  *slog.Logger
	t    timings
	life context.Context

	wmu    sync.Mutex
	nextID int64

	mu      sync.Mutex
	pending map[int64]chan inbound
	subs    map[int64]func(json.RawMessage)
	code    string

	done chan struct{}
	wg   sync.WaitGroup
}

// newConn takes over ws; ending life closes it.
func newConn(life context.Context, ws *websocket.Conn, log *slog.Logger, t timings) *conn {
	return &conn{
		ws: ws, log: log, t: t, life: life,
		pending: map[int64]chan inbound{}, subs: map[int64]func(json.RawMessage){}, done: make(chan struct{}),
	}
}

func (c *conn) start() {
	c.wg.Add(2)
	go func() {
		defer c.wg.Done()
		if providers.Guard(c.log, "homeassistant", CodeBadResponse, func() { c.close(c.dispatch()) }) {
			c.close(CodeBadResponse)
		}
	}()
	go func() {
		defer c.wg.Done()
		c.pingLoop()
	}()
}

// dispatch routes messages until the connection fails, and returns why.
func (c *conn) dispatch() string {
	for {
		m, code := receive(c.life, c.ws)
		if code != "" {
			return code
		}
		switch m.Type {
		case typeResult, typePong:
			c.deliver(m)
		case typeEvent:
			if fn := c.handler(m.ID); fn != nil && m.Event != nil {
				fn(m.Event.Data)
			}
		}
	}
}

// close records the first reason, fails every pending command and drops the
// socket.
func (c *conn) close(code string) {
	c.mu.Lock()
	if c.code != "" {
		c.mu.Unlock()
		return
	}
	c.code = code
	pending := c.pending
	c.pending, c.subs = nil, nil
	c.mu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
	_ = c.ws.CloseNow()
	close(c.done)
}

func (c *conn) closedCode() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.code
}

func (c *conn) deliver(m inbound) {
	c.mu.Lock()
	ch, ok := c.pending[m.ID]
	delete(c.pending, m.ID)
	c.mu.Unlock()
	if ok {
		ch <- m
	}
}

func (c *conn) handler(id int64) func(json.RawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subs[id]
}

func (c *conn) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	delete(c.subs, id)
	c.mu.Unlock()
}

// exchange sends one command and waits for the message that answers its id;
// sub, when set, receives the events of that id.
func (c *conn) exchange(ctx context.Context, typ string, fields map[string]any, sub func(json.RawMessage)) (inbound, error) {
	ch := make(chan inbound, 1)
	id, err := c.send(ctx, typ, fields, ch, sub)
	if err != nil {
		return inbound{}, err
	}
	return c.wait(ctx, id, ch)
}

// wait takes the reply to id from ch; a reply already there when ctx ends
// still wins, since select picks at random among ready cases.
func (c *conn) wait(ctx context.Context, id int64, ch <-chan inbound) (inbound, error) {
	select {
	case m, ok := <-ch:
		return reply(m, ok)
	case <-ctx.Done():
	}
	select {
	case m, ok := <-ch:
		return reply(m, ok)
	default:
	}
	c.forget(id)
	return inbound{}, ctxError(ctx)
}

// reply is a message from a pending chan; a closed one means the connection
// dropped.
func reply(m inbound, ok bool) (inbound, error) {
	if !ok {
		return inbound{}, fail(CodeUnavailable)
	}
	return m, nil
}

// send checks ctx before the write but never writes under it: a write cut
// short closes the whole connection, so it runs under the connection's own
// write timeout.
func (c *conn) send(ctx context.Context, typ string, fields map[string]any, ch chan inbound, sub func(json.RawMessage)) (int64, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if ctx.Err() != nil {
		return 0, ctxError(ctx)
	}
	c.nextID++
	id := c.nextID
	b, err := command(id, typ, fields)
	if err != nil {
		return 0, fail(CodeBadResponse)
	}
	c.mu.Lock()
	if c.code != "" {
		c.mu.Unlock()
		return 0, fail(CodeUnavailable)
	}
	c.pending[id] = ch
	if sub != nil {
		c.subs[id] = sub
	}
	c.mu.Unlock()
	wctx, cancel := context.WithTimeout(c.life, c.t.write)
	defer cancel()
	if c.ws.Write(wctx, websocket.MessageText, b) != nil {
		c.close(CodeUnavailable)
		return 0, fail(CodeUnavailable)
	}
	return id, nil
}

func (c *conn) subscribe(ctx context.Context, eventType string, fn func(json.RawMessage)) error {
	cctx, cancel := context.WithTimeout(ctx, c.t.command)
	defer cancel()
	m, err := c.exchange(cctx, "subscribe_events", map[string]any{"event_type": eventType}, fn)
	if err != nil {
		return err
	}
	_, err = outcome(m)
	return err
}

// pingLoop sends Home Assistant's own ping; a pong late by more than pongWait
// closes the connection as a timeout.
func (c *conn) pingLoop() {
	tick := time.NewTicker(c.t.pingEvery)
	defer tick.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-tick.C:
		}
		ctx, cancel := context.WithTimeout(c.life, c.t.pongWait)
		m, err := c.exchange(ctx, "ping", nil, nil)
		cancel()
		switch {
		case Code(err) == CodeTimeout:
			c.close(CodeTimeout)
			return
		case err != nil:
			c.close(CodeUnavailable)
			return
		case m.Type != typePong:
			c.close(CodeBadResponse)
			return
		}
	}
}
