// Package ryd reads a YouTube video's likes and its estimated dislikes from
// Return YouTube Dislike's open API (https://returnyoutubedislike.com): one
// keyless GET per video id. Its terms ask for a clear credit (Credit) and
// limit a client to 100 calls a minute and 10 000 a day, backing off on a
// 429; this client keeps under both (60 and 5 000) and answers what it has
// rather than wait.
//
// It is off unless the youtubeVotes setting turns it on: a third party then
// learns which video ids the household looks at. Only ids leave the server,
// never a query or a title, and nothing of an answer reaches a log (rule 8).
package ryd

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Credit is the attribution the API's terms ask for, shown where its counts
// are; the wording trips gosec's credential pattern, but it is a public credit line
const Credit = "Likes and dislikes: Return YouTube Dislike (returnyoutubedislike.com)" //nolint:gosec // public credit text, not a credential

const (
	defaultBaseURL = "https://returnyoutubedislikeapi.com"
	defaultTimeout = 2 * time.Second
	maxBody        = 4 << 10
	// own limits, under the API's 100 a minute and 10 000 a day
	perMinute = 60
	perDay    = 5000
	// MaxIDs bounds one Votes call
	MaxIDs   = 8
	inFlight = 4
	cacheTTL = 30 * time.Minute
	maxCache = 4096
)

var videoIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// Votes are a video's counts. Likes and Views are YouTube's public ones;
// Dislikes is the service's estimate, never YouTube's own.
type Votes struct {
	Likes    int64
	Dislikes int64
	Views    int64
}

type Config struct {
	BaseURL string
	// Timeout bounds one call; zero is 2 s
	Timeout time.Duration
	Log     *slog.Logger
	Now     func() time.Time
}

// Client is safe for concurrent use.
type Client struct {
	cfg  Config
	http *http.Client

	mu        sync.Mutex
	cache     map[string]entry
	minute    time.Time
	minuteN   int
	day       time.Time
	dayN      int
	backoffTo time.Time
}

type entry struct {
	v   Votes
	exp time.Time
}

// New is a client over hc (httpx.Client).
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
	return &Client{cfg: c, http: hc, cache: map[string]entry{}}
}

// Votes answers the counts of up to MaxIDs videos it could read within ctx:
// cached ones at once, the rest four at a time while the limits allow. An id
// left out has no counts this time; Votes never fails.
func (c *Client) Votes(ctx context.Context, ids []string) map[string]Votes {
	out := map[string]Votes{}
	var todo []string
	seen := map[string]bool{}
	for _, id := range ids {
		if !videoIDRe.MatchString(id) || seen[id] || len(seen) >= MaxIDs {
			continue
		}
		seen[id] = true
		if v, ok := c.cached(id); ok {
			out[id] = v
			continue
		}
		todo = append(todo, id)
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, inFlight)
	for _, id := range todo {
		if !c.take() {
			break
		}
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			if v, ok := c.fetch(ctx, id); ok {
				mu.Lock()
				out[id] = v
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return out
}

func (c *Client) cached(id string) (Votes, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[id]
	if !ok || !e.exp.After(c.cfg.Now()) {
		delete(c.cache, id)
		return Votes{}, false
	}
	return e.v, true
}

// take spends one call of the limits; false while backing off or at a limit
func (c *Client) take() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.cfg.Now()
	if now.Before(c.backoffTo) {
		return false
	}
	if m := now.Truncate(time.Minute); !m.Equal(c.minute) {
		c.minute, c.minuteN = m, 0
	}
	if d := now.UTC().Truncate(24 * time.Hour); !d.Equal(c.day) {
		c.day, c.dayN = d, 0
	}
	if c.minuteN >= perMinute || c.dayN >= perDay {
		return false
	}
	c.minuteN++
	c.dayN++
	return true
}

func (c *Client) fetch(ctx context.Context, id string) (Votes, bool) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BaseURL+"/votes?videoId="+id, nil)
	if err != nil {
		return Votes{}, false
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Votes{}, false
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		c.mu.Lock()
		// the terms ask a client to back off: the rest of this minute and the next
		c.backoffTo = c.cfg.Now().Truncate(time.Minute).Add(2 * time.Minute)
		c.mu.Unlock()
		c.cfg.Log.Warn("ryd_rate_limited", "status", resp.StatusCode)
		return Votes{}, false
	case resp.StatusCode != http.StatusOK:
		// 404 is a video the service does not know, 400 an id it refuses
		if resp.StatusCode >= 500 {
			c.cfg.Log.Warn("ryd_failed", "status", resp.StatusCode)
		}
		return Votes{}, false
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Votes{}, false
	}
	var a struct {
		ID       string `json:"id"`
		Likes    int64  `json:"likes"`
		Dislikes int64  `json:"dislikes"`
		Views    int64  `json:"viewCount"`
		Deleted  bool   `json:"deleted"`
	}
	if json.Unmarshal(b, &a) != nil || a.ID != id || a.Deleted || a.Likes < 0 || a.Dislikes < 0 || a.Views < 0 {
		return Votes{}, false
	}
	v := Votes{Likes: a.Likes, Dislikes: a.Dislikes, Views: a.Views}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cache) >= maxCache {
		now := c.cfg.Now()
		for k, e := range c.cache {
			if !e.exp.After(now) || len(c.cache) >= maxCache {
				delete(c.cache, k)
			}
		}
	}
	c.cache[id] = entry{v: v, exp: c.cfg.Now().Add(cacheTTL)}
	return v, true
}
