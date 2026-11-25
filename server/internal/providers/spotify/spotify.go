// Package spotify finds songs through the Spotify Web API's search, with an
// app's client credentials: no user account, so it plays nothing itself. A
// song it finds is played from YouTube Music, matched by its artist, title
// and duration. Queries, results, keys and upstream bodies never reach an
// error or log.
package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	defaultAccountsURL = "https://accounts.spotify.com"
	defaultAPIURL      = "https://api.spotify.com"
	defaultTimeout     = 5 * time.Second
	tokenPath          = "/api/token"
	searchPath         = "/v1/search"
	maxBodyBytes       = 1 << 20
	maxTextRunes       = 200
	// MaxLimit bounds one search's results
	MaxLimit = 10
	// a token is renewed this long before Spotify says it ends
	tokenSlack = time.Minute
)

type Config struct {
	AccountsURL, APIURL string
	ClientID            string
	ClientSecret        string
	// Market is an ISO 3166-1 alpha-2 country whose catalog is searched; ""
	// is the app's own
	Market string
	// Timeout bounds one search, its token included; zero means 5 s
	Timeout time.Duration
	Log     *slog.Logger
	Now     func() time.Time
}

// Client searches Spotify; it is a providers.Music.
type Client struct {
	cfg    Config
	client *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

func New(c Config, client *http.Client) *Client {
	if c.AccountsURL == "" {
		c.AccountsURL = defaultAccountsURL
	}
	if c.APIURL == "" {
		c.APIURL = defaultAPIURL
	}
	c.AccountsURL = strings.TrimRight(c.AccountsURL, "/")
	c.APIURL = strings.TrimRight(c.APIURL, "/")
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &Client{cfg: c, client: client}
}

var errFailed = errors.New(providers.CodeMusicFailed)

// statusError is an upstream answer that is not 200, for the probe's code
type statusError int

func (e statusError) Error() string { return "spotify: status " + strconv.Itoa(int(e)) }

// SearchMusic answers up to limit tracks (1 to MaxLimit) in Spotify's order.
func (c *Client) SearchMusic(ctx context.Context, query string, limit int) ([]providers.Track, error) {
	var out []providers.Track
	var err error
	if providers.Guard(c.cfg.Log, "spotify", providers.CodeMusicFailed, func() { out, err = c.search(ctx, query, limit) }) || err != nil {
		return nil, errFailed
	}
	return out, nil
}

type searchAnswer struct {
	Tracks struct {
		Items []trackItem `json:"items"`
	} `json:"tracks"`
}

type trackItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	DurationMS int    `json:"duration_ms"`
	Artists    []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"artists"`
	Album struct {
		Name   string  `json:"name"`
		Images []image `json:"images"`
	} `json:"album"`
}

type image struct {
	URL string `json:"url"`
}

func (c *Client) search(ctx context.Context, query string, limit int) ([]providers.Track, error) {
	limit = min(max(limit, 1), MaxLimit)
	var a searchAnswer
	if err := c.get(ctx, searchPath, url.Values{"q": {query}, "type": {"track"}, "limit": {strconv.Itoa(limit)}}, &a); err != nil {
		return nil, err
	}
	out := make([]providers.Track, 0, len(a.Tracks.Items))
	for _, it := range a.Tracks.Items {
		if t, ok := track(it); ok {
			out = append(out, t)
		}
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func track(it trackItem) (providers.Track, bool) {
	if it.ID == "" || strings.TrimSpace(it.Name) == "" || (it.Type != "" && it.Type != "track") {
		return providers.Track{}, false
	}
	names := make([]string, 0, len(it.Artists))
	for _, ar := range it.Artists {
		if n := strings.TrimSpace(ar.Name); n != "" {
			names = append(names, n)
		}
	}
	return providers.Track{
		Source: providers.SourceSpotify, ID: it.ID, Title: clip(it.Name), Artist: clip(strings.Join(names, ", ")),
		WebURL: "https://open.spotify.com/track/" + url.PathEscape(it.ID),
		Album:  clip(it.Album.Name), DurationS: (it.DurationMS + 500) / 1000, ArtworkURL: largest(it.Album.Images),
	}, true
}

// largest is the first of images, which run largest first, when it is https
func largest(images []image) string {
	if len(images) > 0 && strings.HasPrefix(images[0].URL, "https://") && len(images[0].URL) <= 1024 {
		return images[0].URL
	}
	return ""
}

// get reads one Web API answer into v with the app's token; one refused
// token is dropped, so the next call fetches another
func (c *Client) get(ctx context.Context, path string, q url.Values, v any) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	if c.cfg.Market != "" {
		q.Set("market", c.cfg.Market)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIURL+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	b, err := c.do(req)
	if err != nil {
		if errors.Is(err, statusError(http.StatusUnauthorized)) {
			c.forget()
		}
		return err
	}
	return json.Unmarshal(b, v)
}

// accessToken is the app's token, fetched again once it is about to end
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.cfg.Now().Before(c.expires) {
		return c.token, nil
	}
	token, ttl, err := c.fetchToken(ctx)
	if err != nil {
		return "", err
	}
	c.token, c.expires = token, c.cfg.Now().Add(ttl-tokenSlack)
	return token, nil
}

func (c *Client) forget() {
	c.mu.Lock()
	c.token = ""
	c.mu.Unlock()
}

func (c *Client) fetchToken(ctx context.Context) (string, time.Duration, error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.AccountsURL+tokenPath, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.SetBasicAuth(c.cfg.ClientID, c.cfg.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Close = true
	b, err := c.do(req)
	if err != nil {
		return "", 0, err
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if json.Unmarshal(b, &t) != nil || t.AccessToken == "" || t.ExpiresIn <= 0 {
		return "", 0, errBadAnswer
	}
	return t.AccessToken, time.Duration(t.ExpiresIn) * time.Second, nil
}

var errBadAnswer = errors.New("spotify: bad answer")

func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, err
	}
	return b, nil
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= maxTextRunes {
		return s
	}
	return string(r[:maxTextRunes])
}
