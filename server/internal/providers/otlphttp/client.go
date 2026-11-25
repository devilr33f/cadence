// Package otlphttp posts OTLP/HTTP protobuf requests to one collector: the
// provider kind otlp, which the server forwards its stored telemetry to.
// Errors are codes and statuses, never a URL, a header or a response body.
package otlphttp

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

const (
	maxURL         = 2048
	maxAuth        = 4096
	defaultTimeout = 10 * time.Second
	minTimeout     = time.Second
	maxTimeout     = 30 * time.Second
	// a response body is read this far and dropped, so the connection is kept
	maxResponse = 64 << 10
	// the longest Retry-After honoured
	maxWait = time.Minute
)

var (
	// ErrBadURL names no part of the URL, which may carry credentials.
	ErrBadURL     = errors.New("otlphttp: url must be https, without userinfo, query, fragment or dot segments")
	errBadAuth    = errors.New("otlphttp: authorization must be printable ascii, at most 4096 bytes")
	errBadTimeout = errors.New("otlphttp: timeout must be 1 to 30 s")
	errBadSignal  = errors.New("otlphttp: signal must be logs, metrics or traces")
)

// Error is a failed post: Code is tls, redirect, timeout, unavailable or
// status (then Status is the HTTP status). Permanent says the request itself
// was refused, so sending it again cannot help; Wait is the collector's
// Retry-After, at most a minute.
type Error struct {
	code      string
	status    int
	permanent bool
	wait      time.Duration
}

func (e *Error) Error() string {
	if e.status != 0 {
		return "otlphttp: " + e.code + " " + strconv.Itoa(e.status)
	}
	return "otlphttp: " + e.code
}

func (e *Error) Code() string        { return e.code }
func (e *Error) Status() int         { return e.status }
func (e *Error) Permanent() bool     { return e.permanent }
func (e *Error) Wait() time.Duration { return e.wait }

type ClientConfig struct {
	// URL is the collector's base: /v1/logs, /v1/metrics and /v1/traces are posted under it
	URL string
	// Authorization is the whole header value, such as "Bearer …"; empty sends none
	Authorization string
	// HTTP nil is httpx.Client(nil): system roots, no redirects
	HTTP *http.Client
	// Timeout bounds one post, 1 to 30 s; zero is 10 s
	Timeout time.Duration
}

// Client posts to the three signal URLs of its base and nowhere else.
type Client struct {
	urls    map[string]string
	host    string
	auth    string
	http    *http.Client
	timeout time.Duration
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
	if c.Timeout < minTimeout || c.Timeout > maxTimeout {
		return nil, errBadTimeout
	}
	if c.HTTP == nil {
		c.HTTP = httpx.Client(nil)
	}
	base := "https://" + u.Host + strings.TrimRight(u.EscapedPath(), "/")
	return &Client{
		urls: map[string]string{"logs": base + "/v1/logs", "metrics": base + "/v1/metrics", "traces": base + "/v1/traces"},
		host: u.Host, auth: c.Authorization, http: c.HTTP, timeout: c.Timeout,
	}, nil
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
	return u, nil
}

// Host is the collector's host (and port), never its path.
func (c *Client) Host() string { return c.host }

// Timeout is the configured bound of one post.
func (c *Client) Timeout() time.Duration { return c.timeout }

// Post sends one OTLP protobuf request of signal (logs, metrics or traces), gzipped.
// A failure is an *Error; the caller's own cancellation comes back as its
// ctx error.
func (c *Client) Post(ctx context.Context, signal string, request []byte) error {
	target, ok := c.urls[signal]
	if !ok {
		return errBadSignal
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var body bytes.Buffer
	zw := gzip.NewWriter(&body)
	if _, err := zw.Write(request); err != nil {
		return &Error{code: "unavailable"}
	}
	if err := zw.Close(); err != nil {
		return &Error{code: "unavailable"}
	}
	rctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, target, &body)
	if err != nil {
		return &Error{code: "unavailable"}
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("User-Agent", "cadence-server")
	if c.auth != "" {
		req.Header.Set("Authorization", c.auth)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return failure(ctx, rctx, err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponse))
	_ = resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return statusError(resp.StatusCode, resp.Header.Get("Retry-After"), time.Now())
}

func failure(ctx, rctx context.Context, err error) error {
	var (
		verify *tls.CertificateVerificationError
		alert  tls.AlertError
	)
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, httpx.ErrRedirect):
		return &Error{code: "redirect"}
	case rctx.Err() != nil:
		return &Error{code: "timeout"}
	case errors.As(err, &verify), errors.As(err, &alert):
		return &Error{code: "tls"}
	}
	return &Error{code: "unavailable"}
}

// statusError: a 4xx refuses the request itself, but for 408 and 429, which
// say to try again, as 5xx do (OTLP/HTTP's retryable set, plus 500)
func statusError(status int, retryAfter string, now time.Time) *Error {
	e := &Error{code: "status", status: status}
	switch {
	case status == http.StatusTooManyRequests, status == http.StatusServiceUnavailable:
		e.wait = parseRetryAfter(retryAfter, now)
	case status == http.StatusRequestTimeout, status >= 500:
	case status >= 300 && status < 400:
		e.code = "redirect"
	default:
		e.permanent = true
	}
	return e
}

// parseRetryAfter reads seconds or an HTTP date, at most maxWait; zero for none
func parseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	var d time.Duration
	if n, err := strconv.Atoi(v); err == nil {
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = t.Sub(now)
	}
	return min(max(d, 0), maxWait)
}
