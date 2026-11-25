// Package torrserver is the catalog provider over a TorrServer MatriX.135+
// HTTP API (build/spikes/research/torrserver-api.md): the owner's torrents
// as folders and their files, rutor and Torznab search, and LAN /play URLs
// the TV pulls itself. The server calls TorrServer at the address the TVs
// reach (its URLs are built on the request's host), or rewrites the URLs it
// hands out to the configured public base. Titles, magnets, hashes and paths
// never reach a log or an error.
package torrserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

const (
	requestTimeout = 15 * time.Second
	maxBody        = 8 << 20
)

// ErrBadURL names no part of the URL, which may carry credentials.
var ErrBadURL = errors.New("torrserver: url must be http or https, without userinfo, query or fragment")

type httpError struct{ status int }

func (e *httpError) Error() string { return "torrserver: status " + http.StatusText(e.status) }

// ClientConfig configures a Client.
type ClientConfig struct {
	BaseURL string
	// PublicURL, when set, is the base the TV reaches TorrServer by, put on
	// every URL handed out; empty is BaseURL
	PublicURL string
	User      string
	Password  string
	HTTP      *http.Client
	Timeout   time.Duration
}

// Client speaks TorrServer's API.
type Client struct {
	base, public *url.URL
	user, pass   string
	http         *http.Client
	timeout      time.Duration
}

func parseBase(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, ErrBadURL
	}
	return u, nil
}

// NewClient checks both bases.
func NewClient(c ClientConfig) (*Client, error) {
	base, err := parseBase(c.BaseURL)
	if err != nil {
		return nil, err
	}
	public := base
	if c.PublicURL != "" {
		if public, err = parseBase(c.PublicURL); err != nil {
			return nil, err
		}
	}
	if c.HTTP == nil {
		c.HTTP = httpx.Client(nil)
	}
	if c.Timeout <= 0 {
		c.Timeout = requestTimeout
	}
	return &Client{base: base, public: public, user: c.User, pass: c.Password, http: c.HTTP, timeout: c.Timeout}, nil
}

func (c *Client) endpoint(base *url.URL, path string, q url.Values) string {
	u := *base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawPath = ""
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// do sends one request; a 2xx body is read into out (when given)
func (c *Client) do(ctx context.Context, method, path string, q url.Values, in, out any) error {
	b, status, err := c.raw(ctx, method, path, q, in)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return &httpError{status: status}
	}
	if out == nil || len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

func (c *Client) raw(ctx context.Context, method, path string, q url.Values, in any) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(c.base, path, q), body)
	if err != nil {
		return nil, 0, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, context.DeadlineExceeded
		}
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return b, resp.StatusCode, err
}

var versionRe = regexp.MustCompile(`MatriX\.(\d+)`)

// version is MatriX's number, 0 when /echo names none
func (c *Client) version(ctx context.Context) (int, error) {
	b, status, err := c.raw(ctx, http.MethodGet, "/echo", nil, nil)
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK {
		return 0, &httpError{status: status}
	}
	m := versionRe.FindSubmatch(b)
	if m == nil {
		return 0, nil
	}
	n, _ := strconv.Atoi(string(m[1]))
	return n, nil
}

type fileStat struct {
	ID     int    `json:"id"`
	Path   string `json:"path"`
	Length int64  `json:"length"`
}

type torrentStatus struct {
	Title          string     `json:"title"`
	Poster         string     `json:"poster"`
	Name           string     `json:"name,omitempty"`
	Hash           string     `json:"hash,omitempty"`
	Stat           int        `json:"stat"`
	TorrentSize    int64      `json:"torrent_size,omitempty"`
	ActivePeers    int        `json:"active_peers,omitempty"`
	TotalPeers     int        `json:"total_peers,omitempty"`
	Seeders        int        `json:"connected_seeders,omitempty"`
	FileStats      []fileStat `json:"file_stats,omitempty"`
	Timestamp      int64      `json:"timestamp"`
	DurationSecond float64    `json:"duration_seconds,omitempty"`
}

type torrentsReq struct {
	Action   string `json:"action"`
	Link     string `json:"link,omitempty"`
	Hash     string `json:"hash,omitempty"`
	Title    string `json:"title,omitempty"`
	Poster   string `json:"poster,omitempty"`
	SaveToDB bool   `json:"save_to_db,omitempty"`
}

func (c *Client) list(ctx context.Context) ([]torrentStatus, error) {
	var out []torrentStatus
	return out, c.do(ctx, http.MethodPost, "/torrents", nil, torrentsReq{Action: "list"}, &out)
}

func (c *Client) get(ctx context.Context, hash string) (torrentStatus, error) {
	var out torrentStatus
	return out, c.do(ctx, http.MethodPost, "/torrents", nil, torrentsReq{Action: "get", Hash: hash}, &out)
}

func (c *Client) add(ctx context.Context, link, title, poster string) (torrentStatus, error) {
	var out torrentStatus
	return out, c.do(ctx, http.MethodPost, "/torrents", nil,
		torrentsReq{Action: "add", Link: link, Title: title, Poster: poster, SaveToDB: true}, &out)
}

// details is a search result; TorrServer sends Go's field names
type details struct {
	Title   string
	Name    string
	Size    string
	Tracker string
	Link    string
	Year    int
	Peer    int
	Seed    int
	Magnet  string
	Hash    string
}

// errSearchOff is a search TorrServer has switched off (400 with [])
var errSearchOff = errors.New("torrserver: search off")

func (c *Client) search(ctx context.Context, path, query string) ([]details, error) {
	b, status, err := c.raw(ctx, http.MethodGet, path, url.Values{"query": {query}}, nil)
	switch {
	case err != nil:
		return nil, err
	case status == http.StatusBadRequest && strings.TrimSpace(string(b)) == "[]":
		return nil, errSearchOff
	case status != http.StatusOK:
		return nil, &httpError{status: status}
	}
	var out []details
	return out, json.Unmarshal(b, &out)
}

type subTrack struct {
	Index  int    `json:"index"`
	Name   string `json:"name"`
	Title  string `json:"title"`
	Lang   string `json:"lang"`
	Format string `json:"format"`
	URL    string `json:"url"`
}

func (c *Client) subs(ctx context.Context, hash string, id int) ([]subTrack, error) {
	var out []subTrack
	return out, c.do(ctx, http.MethodGet, "/subs/"+url.PathEscape(hash)+"/"+strconv.Itoa(id), nil, nil, &out)
}

type viewedReq struct {
	Action    string  `json:"action"`
	Hash      string  `json:"hash"`
	FileIndex int     `json:"file_index"`
	TimeCode  float64 `json:"timecode"`
}

type viewed struct {
	Hash      string  `json:"hash"`
	FileIndex int     `json:"file_index"`
	TimeCode  float64 `json:"timecode"`
}

func (c *Client) viewed(ctx context.Context, hash string) ([]viewed, error) {
	var out []viewed
	return out, c.do(ctx, http.MethodPost, "/viewed", nil, viewedReq{Action: "list", Hash: hash}, &out)
}

func (c *Client) setViewed(ctx context.Context, hash string, index int, at float64) error {
	return c.do(ctx, http.MethodPost, "/viewed", nil, viewedReq{Action: "set", Hash: hash, FileIndex: index, TimeCode: at}, nil)
}

// playURL is a file's stream at the public base: no credentials, which the
// known-torrent exemption makes needless
func (c *Client) playURL(hash string, id int) string {
	return c.endpoint(c.public, "/play/"+url.PathEscape(hash)+"/"+strconv.Itoa(id), nil)
}

// publicURL puts a relative or base-hosted URL TorrServer answered on the
// public base
func (c *Client) publicURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.Host == "" || u.Host == c.base.Host {
		u.Scheme, u.Host = c.public.Scheme, c.public.Host
		if !strings.HasPrefix(u.Path, "/") {
			u.Path = "/" + u.Path
		}
		if p := strings.TrimRight(c.public.Path, "/"); p != "" && !strings.HasPrefix(u.Path, p+"/") {
			u.Path = p + u.Path
		}
	}
	return u.String()
}
