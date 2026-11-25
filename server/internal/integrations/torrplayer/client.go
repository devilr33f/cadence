// Package torrplayer is the catalog provider over torrplayer's control API
// (/api/v1, a bearer token from its pairing): anime and series from its
// sources (AniLiberty, anime365, … Nyaa, Torznab), their episodes, the
// voiceovers and torrents of an episode, and its proxied play URLs. The
// server calls torrplayer at an address the TVs reach too (its LAN address),
// since torrplayer builds the play URLs it answers from the host it was
// asked by. Titles, queries, ids and URLs never reach a log or an error.
package torrplayer

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

	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

const (
	requestTimeout = 15 * time.Second
	// resolve adds a torrent and waits for its metadata: torrplayer's own wait
	resolveTimeout = 100 * time.Second
	maxBody        = 4 << 20
)

// ErrBadURL names no part of the URL, which may carry credentials.
var ErrBadURL = errors.New("torrplayer: base url must be http or https, without userinfo, query or fragment")

// httpError is an answer that is not a success, by status and torrplayer's
// own code; never its message
type httpError struct {
	status int
	code   string
}

func (e *httpError) Error() string { return "torrplayer: status " + http.StatusText(e.status) }

// ClientConfig configures a Client.
type ClientConfig struct {
	BaseURL string
	Token   string
	// HTTP nil is httpx.Client(nil)
	HTTP *http.Client
	// Timeout bounds one request; zero is 15 s (resolve has its own)
	Timeout time.Duration
}

// Client speaks the control API.
type Client struct {
	base    *url.URL
	token   string
	http    *http.Client
	timeout time.Duration
}

// NewClient checks the base url: http or https, a host, no userinfo, query
// or fragment; a path prefix is kept.
func NewClient(c ClientConfig) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(c.BaseURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, ErrBadURL
	}
	if c.HTTP == nil {
		c.HTTP = httpx.Client(nil)
	}
	if c.Timeout <= 0 {
		c.Timeout = requestTimeout
	}
	return &Client{base: u, token: c.Token, http: c.HTTP, timeout: c.Timeout}, nil
}

// Host is the base url's host, the one torrplayer builds its URLs on.
func (c *Client) Host() string { return c.base.Host }

func (c *Client) endpoint(path string, q url.Values) string {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1" + path
	u.RawPath = ""
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// pathEscape escapes one path segment (a provider or title id)
func pathEscape(s string) string { return url.PathEscape(s) }

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, q, nil, out, c.timeout)
}

func (c *Client) send(ctx context.Context, method, path string, in, out any) error {
	return c.do(ctx, method, path, nil, in, out, c.timeout)
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, in, out any, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path, q), body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return context.DeadlineExceeded
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var eb errorBody
		_ = json.Unmarshal(b, &eb)
		return &httpError{status: resp.StatusCode, code: eb.Error.Code}
	}
	if out == nil || len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

func (c *Client) status(ctx context.Context) (statusOut, error) {
	var out statusOut
	return out, c.get(ctx, "/status", nil, &out)
}

func (c *Client) search(ctx context.Context, q url.Values) (searchOut, error) {
	var out searchOut
	return out, c.get(ctx, "/search", q, &out)
}

func (c *Client) episodes(ctx context.Context, source, title string) (episodesOut, error) {
	var out episodesOut
	return out, c.get(ctx, "/providers/"+pathEscape(source)+"/titles/"+pathEscape(title)+"/episodes", nil, &out)
}

func (c *Client) sources(ctx context.Context, source, title, episode string) (sourcesOut, error) {
	var out sourcesOut
	q := url.Values{}
	if episode != "" {
		q.Set("episode", episode)
	}
	return out, c.get(ctx, "/providers/"+pathEscape(source)+"/titles/"+pathEscape(title)+"/sources", q, &out)
}

func (c *Client) resolve(ctx context.Context, in resolveIn) (mediaOut, error) {
	var out mediaOut
	return out, c.do(ctx, http.MethodPost, "/resolve", nil, in, &out, resolveTimeout)
}

// skips reads one episode's opening/ending ranges: the provider's own
// markers first, then torrplayer's resolver (manual overrides, AniSkip by
// MAL id). An empty answer is "nobody knows", not a failure.
func (c *Client) skips(ctx context.Context, source, title, episode string, durationS int) (skipsOut, error) {
	var out skipsOut
	q := url.Values{"episode": {episode}}
	if durationS > 0 {
		q.Set("duration", strconv.Itoa(durationS))
	}
	return out, c.get(ctx, "/providers/"+pathEscape(source)+"/titles/"+pathEscape(title)+"/skips", q, &out)
}

func (c *Client) progress(ctx context.Context, source, title string) (progressOut, error) {
	var out progressOut
	return out, c.get(ctx, "/progress", url.Values{"provider": {source}, "title": {title}}, &out)
}

func titlePath(source, title, rest string) string {
	return "/progress/" + pathEscape(source) + "/" + pathEscape(title) + rest
}

func (c *Client) setPrefs(ctx context.Context, source, title string, in titlePrefsIn) error {
	return c.send(ctx, http.MethodPut, titlePath(source, title, "/prefs"), in, nil)
}

func (c *Client) report(ctx context.Context, source, title string, in progressReportIn) error {
	return c.send(ctx, http.MethodPost, titlePath(source, title, "/report"), in, nil)
}

func (c *Client) setWatched(ctx context.Context, source, title string, in watchedIn) error {
	return c.send(ctx, http.MethodPut, titlePath(source, title, "/watched"), in, nil)
}

func (c *Client) watching(ctx context.Context, limit int) (watchingOut, error) {
	var out watchingOut
	q := url.Values{"unfinished": {"true"}}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return out, c.get(ctx, "/watching", q, &out)
}

// schedule reads the airing feed: ongoing titles, each linked to the release
// a provider plays it from. It answers the search shape, with the same
// available flag a browse carries.
func (c *Client) schedule(ctx context.Context, page, limit int) (searchOut, error) {
	var out searchOut
	q := url.Values{}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return out, c.get(ctx, "/schedule", q, &out)
}

func (c *Client) myList(ctx context.Context, status string, limit int) (myListOut, error) {
	var out myListOut
	q := url.Values{}
	if status != "" {
		q.Set("status", status)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return out, c.get(ctx, "/mylist", q, &out)
}

func (c *Client) myListResolve(ctx context.Context, shikimori string) (myListResolveOut, error) {
	var out myListResolveOut
	return out, c.get(ctx, "/mylist/"+pathEscape(shikimori)+"/resolve", nil, &out)
}

func (c *Client) myListSet(ctx context.Context, shikimori, status string) error {
	return c.send(ctx, http.MethodPatch, "/mylist/"+pathEscape(shikimori), myListSetIn{Status: status}, nil)
}

func (c *Client) subscriptions(ctx context.Context) (subscriptionsOut, error) {
	var out subscriptionsOut
	return out, c.get(ctx, "/subscriptions", nil, &out)
}

func (c *Client) subscribe(ctx context.Context, source, title string) error {
	return c.send(ctx, http.MethodPost, "/subscriptions", subscribeIn{Provider: source, TitleID: title}, nil)
}

func (c *Client) unsubscribe(ctx context.Context, source, title string) error {
	return c.send(ctx, http.MethodDelete, "/subscriptions/"+pathEscape(source)+"/"+pathEscape(title), nil, nil)
}
