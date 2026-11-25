// Package mcp connects to MCP servers over streamable HTTP with the official
// go-sdk client: a transport pinned to the configured endpoint that sets the
// Authorization header and caps bodies, both protocol eras, and a supervisor
// that keeps one session per server. Errors are codes: the URL, headers,
// session ids, descriptions, schemas, arguments, results and remote text
// never reach a log or an error.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

const (
	maxURL  = 2048
	maxAuth = 4096

	defaultTimeout = 8 * time.Second
	minTimeout     = time.Second
	maxTimeout     = 30 * time.Second

	defaultMaxResultChars = 8192
	minResultChars        = 256
	maxResultChars        = 65536

	maxPages = 20
	maxTools = 512

	closeGrace = time.Second

	// modernVersion and later negotiate with server/discover, no session
	modernVersion = "2026-07-28"
)

// Status states and eras.
const (
	StateConnected   = "connected"
	StateConnecting  = "connecting"
	StateUnavailable = "unavailable"

	EraModern = "modern"
	EraLegacy = "legacy"
)

// ErrBadURL names no part of the URL, which may carry credentials.
var ErrBadURL = errors.New("mcp: url must be https, without userinfo, query, fragment, dot segments or whitespace, at most 2048 bytes")

var (
	errBadAuth    = errors.New("mcp: authorization must be printable ascii, at most 4096 bytes")
	errBadOptions = errors.New("mcp: timeout must be 1 to 30 s, result chars 256 to 65536, tool prefix ^[a-z][a-z0-9_]{0,15}$")
	prefixRe      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}$`)
)

type timings struct {
	connect, list                       time.Duration
	backoffMin, backoffMax, stableAfter time.Duration
}

var defaultTimings = timings{
	connect: 10 * time.Second, list: 10 * time.Second,
	backoffMin: time.Second, backoffMax: time.Minute, stableAfter: time.Minute,
}

type ClientConfig struct {
	URL string
	// Authorization is the whole header value, such as "Bearer …"; empty sends none
	Authorization string
	// HTTP nil is httpx.Client(nil): system roots. Only its transport is used:
	// redirects are always refused
	HTTP *http.Client
	// Timeout bounds one tool call, 1 to 30 s; zero is 8 s
	Timeout time.Duration
	// MaxResultChars caps a tool result in code points, 256 to 65536; zero is 8192
	MaxResultChars int
	// ToolPrefix names the server's tools to the model; empty leaves the
	// choice to the catalog, which knows the instance name
	ToolPrefix string
}

// Status is the connection as the admin API shows it: the host, never the URL.
type Status struct {
	State string
	// Era is the last session's protocol era; empty until one connected
	Era string
	// LastCode is the latest failure's code, kept across reconnects; empty until one
	LastCode string
	// ConnectedAt is when the current session came up; zero while not connected
	ConnectedAt time.Time
	Host        string
}

// RemoteTool is one listed tool as the server describes it: untrusted text,
// never logged.
type RemoteTool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	// Annotations are the server's hints, nil when it sent none
	Annotations *sdk.ToolAnnotations
}

// Result is a successful tools/call answer: the server's untrusted content,
// never logged.
type Result struct {
	// Structured is structuredContent, nil when the server sent none
	Structured any
	Content    []sdk.Content
}

// Client talks to one MCP server. A supervisor connects it; calls fail with
// mcp_unavailable until a session is up.
type Client struct {
	endpoint       *url.URL
	auth           string
	base           http.RoundTripper
	timeout        time.Duration
	maxResultChars int
	toolPrefix     string
	sdk            *sdk.Client
	t              timings

	// cmu serializes Connect and Close
	cmu sync.Mutex
	mu  sync.Mutex
	cur *session
	// sessions are every session not yet closed: the current one, those
	// draining after it and a refresh's own
	sessions map[*session]struct{}
	status   Status
}

func NewClient(c ClientConfig) (*Client, error) {
	u, err := parseEndpoint(c.URL)
	if err != nil {
		return nil, err
	}
	if len(c.Authorization) > maxAuth || strings.IndexFunc(c.Authorization, func(r rune) bool { return r < 0x20 || r > 0x7e }) >= 0 {
		return nil, errBadAuth
	}
	if c.Timeout == 0 {
		c.Timeout = defaultTimeout
	}
	if c.MaxResultChars == 0 {
		c.MaxResultChars = defaultMaxResultChars
	}
	if c.Timeout < minTimeout || c.Timeout > maxTimeout || c.MaxResultChars < minResultChars ||
		c.MaxResultChars > maxResultChars || (c.ToolPrefix != "" && !prefixRe.MatchString(c.ToolPrefix)) {
		return nil, errBadOptions
	}
	if c.HTTP == nil {
		c.HTTP = httpx.Client(nil)
	}
	base := c.HTTP.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	out := &Client{
		endpoint: u, auth: c.Authorization, timeout: c.Timeout,
		maxResultChars: c.MaxResultChars, toolPrefix: c.ToolPrefix, t: defaultTimings,
	}
	out.init(base)
	return out, nil
}

// init gives c its transport, its own SDK client and no session.
func (c *Client) init(base http.RoundTripper) {
	c.base = base
	c.sdk = sdk.NewClient(&sdk.Implementation{Name: "cadence", Version: "2"}, &sdk.ClientOptions{
		// no roots, sampling or elicitation; no automatic re-issue of a call
		Capabilities:   &sdk.ClientCapabilities{},
		MultiRoundTrip: &sdk.MultiRoundTripOptions{Disabled: true},
	})
	c.sessions = map[*session]struct{}{}
	c.status = Status{State: StateUnavailable, Host: c.endpoint.Host}
}

// through is a client of c's config whose requests go through base.
func (c *Client) through(base http.RoundTripper) *Client {
	out := &Client{
		endpoint: c.endpoint, auth: c.auth, timeout: c.timeout,
		maxResultChars: c.maxResultChars, toolPrefix: c.toolPrefix, t: c.t,
	}
	out.init(base)
	return out
}

func parseEndpoint(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > maxURL || strings.ContainsAny(raw, "?#\\") ||
		strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return nil, ErrBadURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return nil, ErrBadURL
	}
	for seg := range strings.SplitSeq(u.Path, "/") {
		if seg == "." || seg == ".." {
			return nil, ErrBadURL
		}
	}
	return &url.URL{Scheme: "https", Host: u.Host, Path: u.Path, RawPath: u.RawPath}, nil
}

// Timeout is the configured bound of one tool call.
func (c *Client) Timeout() time.Duration { return c.timeout }

// MaxResultChars is the configured cap of a tool result, in code points.
func (c *Client) MaxResultChars() int { return c.maxResultChars }

// ToolPrefix is the configured prefix; empty when the catalog picks one.
func (c *Client) ToolPrefix() string { return c.toolPrefix }

func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Connect opens a new session and reports it connected; an earlier session
// stops taking requests and closes once its own have finished. Never call it
// on a supervised client: the supervisor connects its own way, listing first.
func (c *Client) Connect(ctx context.Context) error {
	s, err := c.connect(ctx)
	if err != nil {
		return err
	}
	c.up(s)
	return nil
}

// connect makes a new session current, in the connecting state.
func (c *Client) connect(ctx context.Context) (*session, error) {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	c.mu.Lock()
	old := c.cur
	c.status.State, c.status.ConnectedAt = StateConnecting, time.Time{}
	c.mu.Unlock()
	if old != nil {
		c.retire(old)
	}
	s, err := c.dial(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.status.State = StateUnavailable
		if code := Code(err); code != "" {
			c.status.LastCode = code
		}
		return nil, err
	}
	c.cur = s
	c.status.Era = s.era
	s.inflight.Done()
	return s, nil
}

// up reports s connected, unless it already left service.
func (c *Client) up(s *session) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur != s || s.isBroken() {
		return false
	}
	c.status.State, c.status.ConnectedAt = StateConnected, time.Now()
	return true
}

// dial opens a session through a fresh pinned transport, held once for the
// caller (inflight), and touches neither the status nor the current session.
// A failure is the caller's own cancellation as itself, any deadline
// mcp_timeout, else a code.
func (c *Client) dial(ctx context.Context) (*session, error) {
	rt := newPinned(c.base, c.endpoint, c.auth)
	cctx, cancel := context.WithTimeout(ctx, c.t.connect)
	defer cancel()
	cctx, over := withOverflow(cctx)
	// a connect that runs out of time or is stopped fails its requests at once
	unbind := context.AfterFunc(cctx, rt.kill)
	cs, err := c.sdk.Connect(cctx, &sdk.StreamableClientTransport{
		Endpoint: c.endpoint.String(), HTTPClient: rt.httpClient(),
		MaxRetries: -1, DisableStandaloneSSE: true, MaxEventSize: maxBody,
	}, nil)
	if !unbind() && err == nil {
		_ = cs.Close()
		err = cctx.Err()
	}
	if err != nil {
		rt.kill()
		switch {
		case ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded):
			return nil, ctx.Err()
		case errors.Is(cctx.Err(), context.DeadlineExceeded):
			return nil, fail(CodeTimeout)
		case over.Load():
			return nil, &Error{Code: CodeProtocol, brk: true}
		}
		return nil, classify(err, opConnect)
	}
	era := EraLegacy
	if res := cs.InitializeResult(); res != nil && res.ProtocolVersion >= modernVersion {
		era = EraModern
	}
	sctx, scancel := context.WithCancel(context.Background())
	s := &session{cs: cs, rt: rt, era: era, ctx: sctx, cancel: scancel, broken: make(chan struct{}), closed: make(chan struct{})}
	s.inflight.Add(1)
	c.mu.Lock()
	c.sessions[s] = struct{}{}
	c.mu.Unlock()
	return s, nil
}

// Close ends every session at once: their requests in flight are aborted
// with mcp_unavailable, and calls fail the same way until the next connect.
// A supervised client is closed by its supervisor's Stop.
func (c *Client) Close() {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	// again until none is left: a refresh's own session may register while
	// the one it refreshes is closing, and is aborted with it
	for {
		c.mu.Lock()
		all := make([]*session, 0, len(c.sessions))
		for s := range c.sessions {
			all = append(all, s)
		}
		if len(all) == 0 {
			c.status.State, c.status.ConnectedAt = StateUnavailable, time.Time{}
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		for _, s := range all {
			s.cancel()
			c.retire(s)
		}
		for _, s := range all {
			<-s.closed
		}
	}
}
