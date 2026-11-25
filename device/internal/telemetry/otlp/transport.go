// Package otlp posts OTLP/HTTP protobuf requests to the server's ingest
// routes: gzip-compressed, with the device token, over verified TLS.
package otlp

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
)

const (
	// MaxRequestBytes bounds a request body once compressed.
	MaxRequestBytes = 1 << 20

	// past the server's own budget for a request (5 s for the token, 5 s for a
	// decompression slot, 10 s to store): one given up sooner may be stored
	// all the same, and stored again on the retry
	requestTimeout    = 30 * time.Second
	handshakeTimeout  = 15 * time.Second
	defaultRetryAfter = 5 * time.Second
	maxRetryAfter     = 60 * time.Second
	// a retry-after of 0 or a past date would retry in a loop
	minRetryAfter = time.Second
	// an answer's body is read this far so the connection can be reused
	maxAnswer = 64 << 10
)

var (
	ErrTooLarge = errors.New("otlp: request over 1 MiB compressed")
	errScheme   = errors.New("otlp: server url must be http:// or https://")
	errEncode   = errors.New("otlp: encode failed")
	// the transport's own errors quote the url and the tls cause
	errPost = errors.New("otlp: post failed")
)

// Client posts to <serverUrl><path> with the device token as bearer. TLS is
// verified against the given roots (the system pool when nil), at least 1.2;
// redirects are refused.
type Client struct {
	base, token string
	http        *http.Client
	timeout     time.Duration
	now         func() time.Time
}

func NewClient(serverURL, token string, roots *x509.CertPool) (*Client, error) {
	base := strings.TrimRight(serverURL, "/")
	if !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://") {
		return nil, errScheme
	}
	return &Client{
		base: base, token: token, timeout: requestTimeout, now: time.Now,
		http: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig:     &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
				MaxIdleConns:        2,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: handshakeTimeout,
			},
			// a redirect could carry the bearer to another host
			CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") },
		},
	}, nil
}

// Post encodes msg and sends it as PostEncoded does.
func (c *Client) Post(ctx context.Context, path string, msg proto.Message) (status int, retryAfter time.Duration, err error) {
	b, err := proto.Marshal(msg)
	if err != nil {
		return 0, 0, errEncode
	}
	return c.PostEncoded(ctx, path, b)
}

// PostEncoded compresses body and posts it, within 30 s. It returns the
// status, and for 429 and 503 how long to wait: the Retry-After in seconds or
// as a date, 1 to 60 s, 5 s without one. A body over MaxRequestBytes once
// compressed is not sent: ErrTooLarge. Errors quote nothing of the request.
func (c *Client) PostEncoded(ctx context.Context, path string, body []byte) (status int, retryAfter time.Duration, err error) {
	z, err := compress(body)
	if err != nil {
		return 0, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(z))
	if err != nil {
		return 0, 0, errPost
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, 0, errPost
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswer))
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
		return resp.StatusCode, c.retryAfter(resp.Header.Get("Retry-After")), nil
	}
	return resp.StatusCode, 0, nil
}

// Put sends body as is (an image is compressed already) to path with its content type, within 30 s, and returns the
// status. Errors quote nothing of the request.
func (c *Client) Put(ctx context.Context, path, contentType string, body []byte) (status int, err error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, errPost
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, errPost
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswer))
	return resp.StatusCode, nil
}

// Get streams path's answer with the device's bearer token; the caller closes the body and owns
// its deadline through ctx. The device-update executor fetches its plan and files this way.
func (c *Client) Get(ctx context.Context, path string) (io.ReadCloser, error) {
	return c.GetWith(ctx, path, nil)
}

// GetWith is Get with header's fields sent too (the cover a screen asks for names its item).
func (c *Client) GetWith(ctx context.Context, path string, header http.Header) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, errPost
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errPost
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswer))
		_ = resp.Body.Close()
		return nil, errPost
	}
	return resp.Body, nil
}

func (c *Client) retryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	var d time.Duration
	if n, err := strconv.ParseUint(v, 10, 64); err == nil || errors.Is(err, strconv.ErrRange) {
		// any count of seconds over the cap is the cap
		d = time.Duration(min(n, uint64(maxRetryAfter/time.Second))) * time.Second
	} else if at, err := http.ParseTime(v); err == nil {
		d = at.Sub(c.now())
	} else {
		return defaultRetryAfter
	}
	return min(max(d, minRetryAfter), maxRetryAfter)
}

// capped refuses to grow past MaxRequestBytes, so compressing stops there.
type capped struct{ bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if c.Len()+len(p) > MaxRequestBytes {
		return 0, ErrTooLarge
	}
	return c.Buffer.Write(p)
}

// a gzip writer holds some hundred KiB of state: one per post is worth keeping
var writers = sync.Pool{New: func() any { return gzip.NewWriter(nil) }}

func compress(b []byte) ([]byte, error) {
	var out capped
	zw := writers.Get().(*gzip.Writer)
	defer func() {
		// the pooled writer must not keep the request's buffer alive
		zw.Reset(io.Discard)
		writers.Put(zw)
	}()
	zw.Reset(&out)
	_, err := zw.Write(b)
	if err == nil {
		err = zw.Close()
	}
	switch {
	case errors.Is(err, ErrTooLarge):
		return nil, ErrTooLarge
	case err != nil:
		return nil, errEncode
	}
	return out.Bytes(), nil
}
