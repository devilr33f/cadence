package t3code

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

type timings struct {
	backoffMin, backoffMax, stableAfter time.Duration
	pingEvery, pongWait                 time.Duration
	call, handshake, write              time.Duration
	readLimit                           int64
	streamBuffer                        int
}

var defaultTimings = timings{
	backoffMin: time.Second, backoffMax: time.Minute, stableAfter: time.Minute,
	pingEvery: 10 * time.Second, pongWait: 10 * time.Second,
	call: 15 * time.Second, handshake: 10 * time.Second, write: 10 * time.Second,
	// a thread snapshot carries its whole timeline
	readLimit:    64 << 20,
	streamBuffer: 16,
}

type ClientConfig struct {
	BaseURL string
	Token   string
	// Roots verifies the server's certificate; nil is the system pool
	Roots *x509.CertPool
	// HTTP, when set, is used instead of httpx.Client(Roots) (tests)
	HTTP *http.Client
	// Log nil is slog's default
	Log *slog.Logger
}

// Status is the connection as an admin view shows it.
type Status struct {
	Connected   bool
	ConnectedAt time.Time
	// LastError is the latest failure's code, kept across reconnects
	LastError string
}

// Client keeps one connection to a T3 Code server while Run runs. Calls made
// while it is down fail unavailable; subscriptions wait for the next
// connection and resume from their cursor.
type Client struct {
	ep    endpoints
	token string
	http  *http.Client
	log   *slog.Logger
	t     timings

	mu      sync.Mutex
	cur     *conn
	changed chan struct{}
	stopped bool
	status  Status
}

// NewClient checks the base URL (a path prefix is kept, trailing slashes
// dropped) and the token; it does not connect.
func NewClient(c ClientConfig) (*Client, error) {
	ep, err := parseBase(c.BaseURL)
	if err != nil {
		return nil, err
	}
	if c.Token == "" {
		return nil, errNoToken
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	h := c.HTTP
	if h == nil {
		h = httpx.Client(c.Roots)
	}
	return &Client{ep: ep, token: c.Token, http: h, log: c.Log, t: defaultTimings, changed: make(chan struct{})}, nil
}

func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

func (c *Client) Connected() bool { return c.Status().Connected }

// current is the live connection (nil while down) and a channel closed at
// its next change; stopped once Run has returned.
func (c *Client) current() (*conn, <-chan struct{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur, c.changed, c.stopped
}

func (c *Client) setCurrent(cn *conn) {
	c.mu.Lock()
	c.cur = cn
	close(c.changed)
	c.changed = make(chan struct{})
	if cn != nil {
		c.status.Connected, c.status.ConnectedAt = true, time.Now()
	} else {
		c.status.Connected, c.status.ConnectedAt = false, time.Time{}
	}
	c.mu.Unlock()
}

// Run keeps a connection up until ctx ends: 1 s backoff doubling to 60 s,
// back to 1 s after a connection that stayed up 60 s; a refused token waits
// the longest delay. Subscriptions end when it returns; it may run again
// after it returned (a setting that named the instance again), never twice at
// once.
func (c *Client) Run(ctx context.Context) {
	c.mu.Lock()
	c.stopped = false
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.stopped = true
		close(c.changed)
		c.changed = make(chan struct{})
		c.mu.Unlock()
	}()
	delay := c.t.backoffMin
	for ctx.Err() == nil {
		up, code := c.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if up >= c.t.stableAfter {
			delay = c.t.backoffMin
		}
		wait := delay
		if code == CodeAuthFailed || code == CodeProtocol {
			wait = c.t.backoffMax
		}
		c.log.Info("t3code_reconnect", "delay_ms", wait.Milliseconds(), "code", code)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(2*delay, c.t.backoffMax)
	}
}

func (c *Client) session(ctx context.Context) (time.Duration, string) {
	ws, code := dial(ctx, c.ep, c.http, c.token, c.t)
	if code != "" {
		c.failed(ctx, code)
		return 0, code
	}
	cn := newConn(ws, c.log, c.t)
	cn.start()
	since := time.Now()
	c.setCurrent(cn)
	c.log.Info("t3code_connected")
	select {
	case <-cn.done:
	case <-ctx.Done():
		_ = cn.write(context.Background(), Frame{Tag: tagEof})
		cn.close(CodeUnavailable)
	}
	cn.wg.Wait()
	c.setCurrent(nil)
	code = cn.closedCode()
	c.failed(ctx, code)
	if ctx.Err() == nil {
		c.log.Warn("t3code_disconnected", "code", code)
	}
	return time.Since(since), code
}

func (c *Client) failed(ctx context.Context, code string) {
	if ctx.Err() != nil {
		return
	}
	c.mu.Lock()
	c.status.LastError = code
	c.mu.Unlock()
}

// Call sends one unary request with payload (marshalled) and decodes its
// result into out (nil skips it). It waits 15 s at most, or until ctx's
// earlier deadline.
func (c *Client) Call(ctx context.Context, method string, payload, out any) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	p, err := json.Marshal(payload)
	if err != nil {
		return fail(CodeBadResponse)
	}
	cn, _, _ := c.current()
	if cn == nil {
		return fail(CodeUnavailable)
	}
	cctx, cancel := context.WithTimeout(ctx, c.t.call)
	defer cancel()
	v, err := cn.call(cctx, method, p)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if out == nil {
		return nil
	}
	if len(v) == 0 || json.Unmarshal(v, out) != nil {
		return fail(CodeBadResponse)
	}
	return nil
}
