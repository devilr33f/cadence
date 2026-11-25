// Package ytweb reads YouTube (www.youtube.com) through its web client's own
// API (InnerTube), which needs no key: search with its filters, a channel's
// tabs in each of their sorts, search inside a channel, playlists and a
// video's counts. yt-dlp still resolves what plays; this reads what exists.
//
// The request shapes follow what Yattee Server (MIT) and YouTube.js (MIT) do,
// checked against live answers on 2026-11-16 (testdata): search params are
// the SearchFilter protobuf (relevance or popularity only: YouTube no longer
// sorts a search by date), a channel's sorts are the continuation tokens its
// tab offers (never ones built here), a playlist pages by the token beside
// its rows, and the player's microformat carries a video's exact views,
// likes and publish time even when it will not play to this client.
//
// Privacy (rule 8): queries, ids, titles and answers never reach a log or an
// error; a failure is an Error with a code and an HTTP status.
package ytweb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	defaultBaseURL = "https://www.youtube.com"
	defaultTimeout = 8 * time.Second
	maxBodyBytes   = 8 << 20
	userAgent      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) " +
		"Chrome/131.0.0.0 Safari/537.36"
	retryWait = 500 * time.Millisecond
)

// Error is a failed call by code: failed, not_found, rate_limited, timeout,
// bad_cursor. Status is YouTube's HTTP answer when it gave one.
type Error struct {
	Code   string
	Status int
}

func (e *Error) Error() string        { return "ytweb: " + e.Code }
func (e *Error) ErrorCode() string    { return e.Code }
func (e *Error) HTTPStatusCode() int  { return e.Status }
func (e *Error) Is(target error) bool { t, ok := target.(*Error); return ok && t.Code == e.Code }

var (
	ErrFailed      = &Error{Code: "failed"}
	ErrNotFound    = &Error{Code: "not_found"}
	ErrRateLimited = &Error{Code: "rate_limited"}
	ErrTimeout     = &Error{Code: "timeout"}
	ErrBadCursor   = &Error{Code: "bad_cursor"}
)

type Config struct {
	BaseURL string
	// Timeout bounds one call to YouTube; zero is 8 s
	Timeout time.Duration
	// Region is the gl YouTube ranks for ("US", "RU"); "" leaves it to YouTube
	Region string
	// Log receives provider_panic and ytweb_failed; nil is slog's default
	Log *slog.Logger
	// Now dates the client version and relative times; nil is time.Now
	Now func() time.Time
}

// Client reads YouTube; it is safe for concurrent use.
type Client struct {
	cfg     Config
	http    *http.Client
	cursors *cursors
	handles *ttlMap[string]
	stats   *ttlMap[providers.Stats]
}

// New is a client over hc (httpx.Client: no redirects off the host).
func New(c Config, hc *http.Client) *Client {
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &Client{cfg: c, http: hc, cursors: newCursors(c.Now), handles: newTTLMap[string](c.Now, 24*time.Hour, 1024),
		stats: newTTLMap[providers.Stats](c.Now, 30*time.Minute, 4096)}
}

func (c *Client) now() time.Time { return c.cfg.Now() }

// guard runs fn and turns a panic into ErrFailed, logged by op alone
func (c *Client) guard(op string, fn func() error) error {
	var err error
	if providers.Guard(c.cfg.Log, "youtube-web", "youtube_failed", func() { err = fn() }) {
		return ErrFailed
	}
	if err == nil {
		return nil
	}
	var bad *BadOption
	if errors.As(err, &bad) {
		return bad
	}
	var e *Error
	if !errors.As(err, &e) {
		e = ErrFailed
	}
	if e.Code != ErrNotFound.Code && e.Code != ErrBadCursor.Code {
		c.cfg.Log.Warn("ytweb_failed", "op", op, "code", e.Code, "status", e.Status)
	}
	return e
}

type clientContext struct {
	Client struct {
		ClientName    string `json:"clientName"`
		ClientVersion string `json:"clientVersion"`
		HL            string `json:"hl"`
		GL            string `json:"gl,omitempty"`
	} `json:"client"`
}

func (c *Client) context() clientContext {
	var cc clientContext
	cc.Client.ClientName = "WEB"
	// the web client's own version form; the endpoint takes any recent date
	cc.Client.ClientVersion = "2." + c.now().UTC().AddDate(0, 0, -2).Format("20060102") + ".00.00"
	// counts and times are parsed in English
	cc.Client.HL = "en"
	cc.Client.GL = c.cfg.Region
	return cc
}

// post calls one endpoint ("search", "browse", "player",
// "navigation/resolve_url") and answers its decoded body; a 429 or a 5xx is
// tried once more
func (c *Client) post(ctx context.Context, endpoint string, body map[string]any) (node, error) {
	body["context"] = c.context()
	b, err := json.Marshal(body)
	if err != nil {
		return nil, ErrFailed
	}
	for attempt := 0; ; attempt++ {
		out, status, err := c.once(ctx, endpoint, b)
		retry := status == http.StatusTooManyRequests || status >= 500
		if err == nil || !retry || attempt > 0 {
			return out, err
		}
		select {
		case <-ctx.Done():
			return nil, ErrTimeout
		case <-time.After(retryWait):
		}
	}
}

func (c *Client) once(ctx context.Context, endpoint string, body []byte) (node, int, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/youtubei/v1/"+endpoint+"?prettyPrint=false",
		bytes.NewReader(body))
	if err != nil {
		return nil, 0, ErrFailed
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("User-Agent", userAgent)
	// the endpoint answers only a request that names the site
	req.Header.Set("Origin", defaultBaseURL)
	req.Header.Set("Referer", defaultBaseURL+"/")
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, 0, ErrTimeout
		}
		return nil, 0, ErrFailed
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, resp.StatusCode, &Error{Code: "not_found", Status: resp.StatusCode}
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, resp.StatusCode, &Error{Code: "rate_limited", Status: resp.StatusCode}
	case resp.StatusCode != http.StatusOK:
		return nil, resp.StatusCode, &Error{Code: "failed", Status: resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, 0, ErrTimeout
		}
		return nil, resp.StatusCode, ErrFailed
	}
	var out node
	if json.Unmarshal(raw, &out) != nil || out == nil {
		return nil, resp.StatusCode, ErrFailed
	}
	return out, resp.StatusCode, nil
}
