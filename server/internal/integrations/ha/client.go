// Package ha talks to the owner's Home Assistant over its WebSocket API: one
// authenticated connection kept up with backoff, commands matched to their
// results by id, and event subscriptions renewed on every connection. Errors
// are codes: entity data, Home Assistant's text, the URL and the token never
// reach a log or an error.
package ha

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

const maxBaseURL = 512

// ErrBadURL names no part of the URL, which may carry credentials.
var ErrBadURL = errors.New("ha: base url must be http or https, without userinfo, query, fragment or whitespace")

var errNoToken = errors.New("ha: token missing")

type timings struct {
	backoffMin, backoffMax, stableAfter time.Duration
	pingEvery, pongWait                 time.Duration
	command, handshake, write           time.Duration
	readLimit                           int64
}

var defaultTimings = timings{
	backoffMin: time.Second, backoffMax: time.Minute, stableAfter: time.Minute,
	pingEvery: 30 * time.Second, pongWait: 10 * time.Second,
	command: 5 * time.Second, handshake: 10 * time.Second, write: 10 * time.Second,
	readLimit: 16 << 20,
}

type ClientConfig struct {
	BaseURL string
	Token   string
	// Roots verifies the server's certificate; nil is the system pool
	Roots *x509.CertPool
	// Log nil is slog's default
	Log *slog.Logger
}

// Status is the connection as the admin API shows it.
type Status struct {
	Connected bool
	// ConnectedAt is when the current connection came up; zero while disconnected
	ConnectedAt time.Time
	// LastError is the latest failure's code, kept across reconnects; empty until one
	LastError string
	// Version is the Home Assistant version the latest authentication
	// reported, kept across reconnects; empty until one or when it showed none
	Version string
}

type subscription struct {
	event string
	fn    func(json.RawMessage)
}

// Client keeps one connection to Home Assistant while Run runs.
type Client struct {
	url   string
	token string
	http  *http.Client
	log   *slog.Logger
	t     timings

	mu     sync.Mutex
	subs   []subscription
	hooks  []func(context.Context) error
	downs  []func()
	cur    *conn
	status Status
}

// NewClient checks the base URL (a path prefix is kept, trailing slashes
// dropped) and dials over verified TLS that refuses redirects.
func NewClient(c ClientConfig) (*Client, error) {
	u, err := wsURL(c.BaseURL)
	if err != nil {
		return nil, err
	}
	if c.Token == "" {
		return nil, errNoToken
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	return &Client{url: u, token: c.Token, http: httpx.Client(c.Roots), log: c.Log, t: defaultTimings}, nil
}

// wsURL is <ws|wss>://<host><prefix>/api/websocket.
func wsURL(raw string) (string, error) {
	if len(raw) > maxBaseURL || strings.ContainsAny(raw, "?#") || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return "", ErrBadURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", ErrBadURL
	}
	var scheme string
	switch u.Scheme {
	case "http":
		scheme = "ws"
	case "https":
		scheme = "wss"
	default:
		return "", ErrBadURL
	}
	return scheme + "://" + u.Host + strings.TrimRight(u.EscapedPath(), "/") + "/api/websocket", nil
}

// Subscribe asks for events of eventType on every connection, renewed after
// each reconnect; call it before Run. fn gets the event's data and runs on
// the read goroutine, so it must not block: results wait behind it.
func (c *Client) Subscribe(eventType string, fn func(data json.RawMessage)) {
	c.mu.Lock()
	c.subs = append(c.subs, subscription{eventType, fn})
	c.mu.Unlock()
}

// OnConnect runs fn on every connection after auth and the subscriptions,
// before the client reports connected; fn may send commands, and its ctx
// ends with the connection. An error closes the connection (bad_response)
// and the client reconnects with backoff. Call it before Run.
func (c *Client) OnConnect(fn func(ctx context.Context) error) {
	c.mu.Lock()
	c.hooks = append(c.hooks, fn)
	c.mu.Unlock()
}

// OnDisconnect runs fn once each authenticated connection has ended, after
// its last event; fn must not block. Call it before Run.
func (c *Client) OnDisconnect(fn func()) {
	c.mu.Lock()
	c.downs = append(c.downs, fn)
	c.mu.Unlock()
}

func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status.Connected
}

func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Command sends one command of type typ with fields and returns the result
// payload. It waits 5 s at most, or until ctx's earlier deadline (timeout);
// Home Assistant's refusal is ha_error:<its code>; a cancelled ctx returns its
// own error, and a ctx cancelled before the write sends nothing.
func (c *Client) Command(ctx context.Context, typ string, fields map[string]any) (_ json.RawMessage, err error) {
	ctx, span := startSpan(ctx, typ, fields)
	defer func() { endSpan(span, err) }()
	if ctx.Err() != nil {
		return nil, ctxError(ctx)
	}
	c.mu.Lock()
	cn := c.cur
	c.mu.Unlock()
	if cn == nil {
		return nil, fail(CodeUnavailable)
	}
	cctx, cancel := context.WithTimeout(ctx, c.t.command)
	defer cancel()
	m, err := cn.exchange(cctx, typ, fields, nil)
	if err != nil {
		return nil, err
	}
	return outcome(m)
}

// Call runs domain.service with data as service_data on entityID as the
// target; nil data and an empty entityID are left out. It never asks for a
// service response.
func (c *Client) Call(ctx context.Context, domain, service string, data map[string]any, entityID string) error {
	f := map[string]any{"domain": domain, "service": service}
	if data != nil {
		f["service_data"] = data
	}
	if entityID != "" {
		f["target"] = map[string]any{"entity_id": entityID}
	}
	_, err := c.Command(ctx, "call_service", f)
	return err
}

// Run keeps a connection up until ctx ends: 1 s backoff doubling to 60 s,
// back to 1 s after a connection that stayed up 60 s. It returns at once
// after auth_invalid and never tries again, since Home Assistant bans an
// address that keeps failing to log in. Call it once.
func (c *Client) Run(ctx context.Context) {
	delay := c.t.backoffMin
	for ctx.Err() == nil {
		up, code := c.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if code == CodeAuthFailed {
			c.log.Error("ha_auth_failed")
			return
		}
		if up >= c.t.stableAfter {
			delay = c.t.backoffMin
		}
		c.log.Info("ha_reconnect", "delay_ms", delay.Milliseconds(), "code", code)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(2*delay, c.t.backoffMax)
	}
}

// session runs one connection to its end and returns how long it was
// reported connected and why it ended.
func (c *Client) session(ctx context.Context) (time.Duration, string) {
	ws, version, code := dial(ctx, c.url, c.http, c.token, c.t)
	if code != "" {
		c.ended(ctx, code)
		return 0, code
	}
	c.mu.Lock()
	c.status.Version = version
	c.mu.Unlock()
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cn := newConn(sctx, ws, c.log, c.t)
	cn.start()
	c.mu.Lock()
	c.cur = cn
	c.mu.Unlock()
	var since time.Time
	if code := c.prepare(sctx, cn); code != "" {
		cn.close(code)
	} else {
		since = time.Now()
		c.mu.Lock()
		c.status.Connected, c.status.ConnectedAt = true, since
		c.mu.Unlock()
		c.log.Info("ha_connected")
	}
	select {
	case <-cn.done:
	case <-ctx.Done():
		cn.close(CodeUnavailable)
	}
	cn.wg.Wait()
	code = cn.closedCode()
	c.mu.Lock()
	c.cur = nil
	c.status.Connected, c.status.ConnectedAt = false, time.Time{}
	downs := slices.Clone(c.downs)
	c.mu.Unlock()
	for _, fn := range downs {
		providers.Guard(c.log, "homeassistant", CodeBadResponse, fn)
	}
	c.ended(ctx, code)
	if since.IsZero() || ctx.Err() != nil {
		return 0, code
	}
	c.log.Warn("ha_disconnected", "code", code)
	return time.Since(since), code
}

func (c *Client) ended(ctx context.Context, code string) {
	if ctx.Err() != nil {
		return
	}
	c.mu.Lock()
	c.status.LastError = code
	c.mu.Unlock()
}

// prepare renews the subscriptions, then runs the OnConnect hooks; a code
// says why the connection cannot be used.
func (c *Client) prepare(ctx context.Context, cn *conn) string {
	c.mu.Lock()
	subs, hooks := slices.Clone(c.subs), slices.Clone(c.hooks)
	c.mu.Unlock()
	for _, s := range subs {
		if cn.subscribe(ctx, s.event, s.fn) != nil {
			return setupCode(cn)
		}
	}
	for _, h := range hooks {
		var err error
		if providers.Guard(c.log, "homeassistant", CodeBadResponse, func() { err = h(ctx) }) || err != nil {
			return setupCode(cn)
		}
	}
	return ""
}

// setupCode is the connection's own reason when it already failed, else
// bad_response.
func setupCode(cn *conn) string {
	if code := cn.closedCode(); code != "" {
		return code
	}
	return CodeBadResponse
}
