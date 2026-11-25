// Package ytmusic finds songs on YouTube Music through its web client's own
// search endpoint (InnerTube), which needs no key. Queries, results and
// upstream bodies never reach an error or log.
package ytmusic

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
	defaultBaseURL = "https://music.youtube.com"
	defaultTimeout = 5 * time.Second
	searchPath     = "/youtubei/v1/search?prettyPrint=false"
	maxBodyBytes   = 4 << 20
	// MaxLimit bounds one search's results
	MaxLimit = 10
	// songsFilter is the search's "Songs" tab: tracks from the music catalog,
	// not uploaded videos
	songsFilter = "EgWKAQIIAWoMEA4QChADEAQQCRAF"
	clientName  = "WEB_REMIX"
)

type Config struct {
	BaseURL string
	// Timeout bounds one search; zero means 5 s
	Timeout time.Duration
	// Log receives provider_panic; nil uses slog default
	Log *slog.Logger
	// Now dates the client version the endpoint wants; nil is time.Now
	Now func() time.Time
}

// Client searches YouTube Music; it is a providers.Music.
type Client struct {
	log     *slog.Logger
	base    string
	timeout time.Duration
	client  *http.Client
	now     func() time.Time
}

func New(c Config, client *http.Client) *Client {
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &Client{log: c.Log, base: strings.TrimRight(c.BaseURL, "/"), timeout: c.Timeout, client: client, now: c.Now}
}

var errFailed = errors.New(providers.CodeMusicFailed)

type request struct {
	Context clientContext `json:"context"`
	Query   string        `json:"query"`
	Params  string        `json:"params"`
}

// SearchMusic answers up to limit songs (1 to MaxLimit) in the order YouTube
// Music ranks them.
func (c *Client) SearchMusic(ctx context.Context, query string, limit int) ([]providers.Track, error) {
	var out []providers.Track
	var err error
	if providers.Guard(c.log, "youtube-music", providers.CodeMusicFailed, func() { out, err = c.search(ctx, query, limit) }) || err != nil {
		return nil, errFailed
	}
	return out, nil
}

func (c *Client) search(ctx context.Context, query string, limit int) ([]providers.Track, error) {
	limit = min(max(limit, 1), MaxLimit)
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, err := c.searchPost(ctx, query, songsFilter)
	if err != nil {
		return nil, errFailed
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, errFailed
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, errFailed
	}
	tracks, ok := parseSongs(b)
	if !ok {
		return nil, errFailed
	}
	if len(tracks) > limit {
		tracks = tracks[:limit]
	}
	return tracks, nil
}

func (c *Client) post(ctx context.Context, query string) (*http.Response, error) {
	return c.searchPost(ctx, query, songsFilter)
}

func (c *Client) searchPost(ctx context.Context, query, params string) (*http.Response, error) {
	return c.call(ctx, searchPath, request{Context: c.clientContext(), Query: query, Params: params})
}

type clientContext struct {
	Client struct {
		ClientName    string `json:"clientName"`
		ClientVersion string `json:"clientVersion"`
		HL            string `json:"hl"`
	} `json:"client"`
}

func (c *Client) clientContext() clientContext {
	var cc clientContext
	cc.Client.ClientName = clientName
	// the web client's own version form; the endpoint takes any recent date
	cc.Client.ClientVersion = "1." + c.now().UTC().Format("20060102") + ".01.00"
	cc.Client.HL = "en"
	return cc
}

func (c *Client) call(ctx context.Context, path string, v any) (*http.Response, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// the endpoint answers only a request that names the music site
	req.Header.Set("Origin", defaultBaseURL)
	return c.client.Do(req)
}
