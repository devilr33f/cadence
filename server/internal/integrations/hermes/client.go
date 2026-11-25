// Package hermes runs background tasks on a Hermes agent server (Nous
// Research hermes-agent) through its native Runs API: plain HTTP and polling,
// one Hermes session per task. Instructions, questions, answers and outputs
// never reach a log or an error; errors are codes.
package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/trace"

	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

const (
	requestTimeout = 10 * time.Second
	maxBody        = 256 << 10
	maxRetryAfter  = 60 * time.Second
	defaultMaxRuns = 4
	maxMaxRuns     = 16
)

// ErrBadURL names no part of the URL, which may carry credentials.
var ErrBadURL = errors.New("hermes: base url must be https, without userinfo, query, fragment or dot segments")

type ClientConfig struct {
	BaseURL string
	APIKey  string
	// HTTP nil is httpx.Client(nil): system roots, no redirects
	HTTP *http.Client
	// Timeout bounds one request; zero is 10 s
	Timeout time.Duration
	// MaxRuns caps the runs a kind keeps going at once, 1 to 16; zero is 4
	MaxRuns int
}

// Client speaks the Runs API. A path prefix of the base URL is kept.
type Client struct {
	base    string
	key     string
	http    *http.Client
	timeout time.Duration
	maxRuns int
}

func NewClient(c ClientConfig) (*Client, error) {
	base, err := parseBase(c.BaseURL)
	if err != nil {
		return nil, err
	}
	if c.APIKey == "" {
		return nil, errors.New("hermes: api key missing")
	}
	if c.HTTP == nil {
		c.HTTP = httpx.Client(nil)
	}
	if c.Timeout <= 0 {
		c.Timeout = requestTimeout
	}
	switch {
	case c.MaxRuns == 0:
		c.MaxRuns = defaultMaxRuns
	case c.MaxRuns < 0, c.MaxRuns > maxMaxRuns:
		return nil, errors.New("hermes: max runs must be 1 to 16")
	}
	return &Client{base: base, key: c.APIKey, http: c.HTTP, timeout: c.Timeout, maxRuns: c.MaxRuns}, nil
}

// MaxRuns is the instance's cap on runs going at once.
func (c *Client) MaxRuns() int { return c.maxRuns }

func parseBase(raw string) (string, error) {
	if strings.ContainsAny(raw, "?#") {
		return "", ErrBadURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", ErrBadURL
	}
	for seg := range strings.SplitSeq(u.Path, "/") {
		if seg == "." || seg == ".." {
			return "", ErrBadURL
		}
	}
	return "https://" + u.Host + strings.TrimRight(u.EscapedPath(), "/") + "/", nil
}

type outcome struct {
	status int
	// body is nil when it was over the cap, not UTF-8 or not JSON
	body []byte
	// broken: the connection failed while the body was read
	broken     bool
	retryAfter time.Duration
}

// do sends one request, op's span (its name, never the run's id). A failure
// is a code; the caller's own cancellation comes back as its ctx error.
func (c *Client) do(ctx context.Context, op, method, path string, body []byte, key string) (o outcome, err error) {
	ctx, span := tracing.StartKind(ctx, "hermes", op, trace.SpanKindClient)
	defer func() { endSpan(span, o, err) }()
	if err := ctx.Err(); err != nil {
		return outcome{}, err
	}
	rctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(rctx, method, c.base+path, rd)
	if err != nil {
		return outcome{}, fail(CodeUnavailable)
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return outcome{}, c.failure(ctx, rctx)
	}
	defer func() { _ = resp.Body.Close() }()
	o = outcome{status: resp.StatusCode, retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	switch {
	case err != nil && rctx.Err() != nil:
		return outcome{}, c.failure(ctx, rctx)
	case err != nil:
		o.broken = true
	case len(b) <= maxBody && utf8.Valid(b) && json.Valid(b):
		o.body = b
	}
	return o, nil
}

func (c *Client) failure(ctx, rctx context.Context) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(rctx.Err(), context.DeadlineExceeded):
		return fail(CodeTimeout)
	}
	return fail(CodeUnavailable)
}

func parseRetryAfter(h string, now time.Time) time.Duration {
	if h == "" {
		return 0
	}
	var d time.Duration
	if n, err := strconv.Atoi(h); err == nil {
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(h); err == nil {
		d = t.Sub(now)
	}
	return min(max(d, 0), maxRetryAfter)
}

func unavailable(o outcome) error { return &Error{Code: CodeUnavailable, retryAfter: o.retryAfter} }

func runPath(id string) string { return "v1/runs/" + url.PathEscape(id) }

// Probe checks that the server offers native runs, status, stop and approval
// routes, durable idempotency keys and unknown-run resolution; any explicit
// false vetoes it.
func (c *Client) Probe(ctx context.Context) error {
	o, err := c.do(ctx, "hermes.probe", http.MethodGet, "v1/capabilities", nil, "")
	if err != nil {
		return err
	}
	if o.status == http.StatusTooManyRequests || o.status >= 500 || o.broken {
		return unavailable(o)
	}
	if o.status != http.StatusOK || !capabilitiesOK(o.body) {
		return fail(CodeIncompatible)
	}
	return nil
}

// CreateRun submits body under an idempotency key: a replay with the same key
// and body returns the same run.
func (c *Client) CreateRun(ctx context.Context, body []byte, key string) (string, error) {
	o, err := c.do(ctx, "hermes.create_run", http.MethodPost, "v1/runs", body, key)
	if err != nil {
		return "", err
	}
	switch {
	case o.status == http.StatusConflict:
		return "", fail(CodeConflict)
	case o.status == http.StatusOK || o.status == http.StatusAccepted:
		id, ok := parseAccepted(o.body)
		if !ok {
			return "", unavailable(o)
		}
		return id, nil
	case o.status >= 400 && o.status < 500 && o.status != http.StatusTooManyRequests:
		return "", fail(CodeRefused)
	}
	return "", unavailable(o)
}

// GetRun polls a run. Queued, waiting_for_approval and stopping are running;
// an upstream unknown is CodeUnknown and a missing run CodeGone.
func (c *Client) GetRun(ctx context.Context, id string) (Run, error) {
	o, err := c.do(ctx, "hermes.get_run", http.MethodGet, runPath(id), nil, "")
	if err != nil {
		return Run{}, err
	}
	switch {
	case o.status == http.StatusNotFound:
		return Run{}, fail(CodeGone)
	case o.status == http.StatusOK && !o.broken:
	default:
		return Run{}, unavailable(o)
	}
	return parseRun(o.body, id)
}

// StopRun asks Hermes to stop a run and returns the status its receipt names.
// The receipt is no proof: only a poll shows the run ended. A missing run is
// CodeGone; any other failure CodeUnknown.
func (c *Client) StopRun(ctx context.Context, id string) (string, error) {
	o, err := c.do(ctx, "hermes.stop_run", http.MethodPost, runPath(id)+"/stop", nil, "")
	switch {
	case err != nil && ctx.Err() != nil:
		return "", ctx.Err()
	case err != nil:
		return "", fail(CodeUnknown)
	case o.status == http.StatusNotFound:
		return "", fail(CodeGone)
	case o.status != http.StatusOK && o.status != http.StatusAccepted:
		return "", fail(CodeUnknown)
	}
	status, ok := parseStopReceipt(o.body, id)
	if !ok {
		return "", fail(CodeUnknown)
	}
	return status, nil
}

// Clarify answers a clarify prompt of that exact generation.
func (c *Client) Clarify(ctx context.Context, run, prompt string, generation int64, answer string) error {
	return c.control(ctx, run, prompt, "clarify", map[string]any{"request_id": prompt, "execution_generation": generation, "answer": answer})
}

// Approve answers an approval prompt of that exact generation with once or deny.
func (c *Client) Approve(ctx context.Context, run, prompt string, generation int64, choice string) error {
	return c.control(ctx, run, prompt, "approval", map[string]any{"request_id": prompt, "execution_generation": generation, "choice": choice})
}

// control posts an answer. already_resolved proves nothing landed, so it is a
// conflict like a 409.
func (c *Client) control(ctx context.Context, run, prompt, kind string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fail(CodeBadInput)
	}
	o, err := c.do(ctx, "hermes."+kind, http.MethodPost, runPath(run)+"/"+kind, body, "")
	if err != nil {
		return err
	}
	switch o.status {
	case http.StatusNotFound:
		return fail(CodeGone)
	case http.StatusConflict:
		return fail(CodeConflict)
	case http.StatusBadRequest:
		return fail(CodeRefused)
	case http.StatusOK, http.StatusAccepted:
		resolved, ok := parseControlReceipt(o.body, run, prompt)
		switch {
		case !ok:
			return unavailable(o)
		case !resolved:
			return fail(CodeConflict)
		}
		return nil
	}
	return unavailable(o)
}
