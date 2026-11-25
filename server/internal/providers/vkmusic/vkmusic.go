// Package vkmusic finds and streams VK songs through api.vk.com's audio
// methods, with an access token of VK's official Android client (VK refuses
// audio to any other client's token, and to a call whose user agent is not
// the one the token was issued with). Queries, results, ids, access keys,
// urls, the token and upstream bodies never reach an error or log.
package vkmusic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	defaultAPIURL = "https://api.vk.com/method"
	apiVersion    = "5.131"
	// DefaultUserAgent is the official Android client's, which the owner's
	// token is issued with unless its config names another
	DefaultUserAgent = "VKAndroidApp/5.52-4543 (Android 5.1.1; SDK 22; x86_64; unknown Android SDK built for x86_64; en; 320x240)"
	defaultTimeout   = 8 * time.Second
	// vk answers error 6 past a few calls a second
	defaultInterval = 350 * time.Millisecond
	maxBodyBytes    = 4 << 20
	maxTextRunes    = 200
	// MaxLimit bounds one search's results
	MaxLimit = 10
)

// MediaHosts are the hosts VK serves songs from, for the server's stream
// relay: its HLS playlists, keys and segments, and its older mp3s.
var MediaHosts = []string{".vkuseraudio.net", ".vkuseraudio.com", ".vk-cdn.net"}

type Config struct {
	APIURL    string
	Token     string
	UserAgent string
	// Timeout bounds one call; zero means 8 s
	Timeout time.Duration
	// Interval is the least time between two calls; zero means 350 ms,
	// negative none
	Interval time.Duration
	Log      *slog.Logger
}

// Client searches and streams VK: a providers.Music, Collections, Streamer
// and TrackRadio.
type Client struct {
	cfg    Config
	client *http.Client

	mu   sync.Mutex
	next time.Time
}

func New(c Config, client *http.Client) *Client {
	if c.APIURL == "" {
		c.APIURL = defaultAPIURL
	}
	c.APIURL = strings.TrimRight(c.APIURL, "/")
	if c.UserAgent == "" {
		c.UserAgent = DefaultUserAgent
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.Interval == 0 {
		c.Interval = defaultInterval
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	return &Client{cfg: c, client: client}
}

var (
	errFailed    = errors.New(providers.CodeMusicFailed)
	errBadAnswer = errors.New("vkmusic: bad answer")
	errRequest   = errors.New("vkmusic: request failed")
)

// statusError is an HTTP answer that is not 200, for the probe's code
type statusError int

func (e statusError) Error() string { return "vkmusic: status " + strconv.Itoa(int(e)) }

// apiError is vk's own error code; its message is never kept
type apiError int

func (e apiError) Error() string { return "vkmusic: error " + strconv.Itoa(int(e)) }

// signIn is a code of a token vk refuses, or one without audio access
func (e apiError) signIn() bool {
	switch e {
	case 5, 15, 25, 200, 201, 203, 1117:
		return true
	}
	return false
}

func (e apiError) rateLimited() bool { return e == 6 || e == 9 || e == 29 }

func (c *Client) guard(f func() error) error {
	var err error
	if providers.Guard(c.cfg.Log, "vk-music", providers.CodeMusicFailed, func() { err = f() }) {
		return errFailed
	}
	return err
}

// call runs one api method with the token, the version and the user agent,
// and reads its response into v
func (c *Client) call(ctx context.Context, method string, q url.Values, v any) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	if err := c.pace(ctx); err != nil {
		return err
	}
	q.Set("access_token", c.cfg.Token)
	q.Set("v", apiVersion)
	q.Set("lang", "ru")
	q.Set("https", "1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIURL+"/"+method+"?"+q.Encode(), nil)
	if err != nil {
		return errRequest
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		// a url.Error quotes the url, and with it the token
		var ue *url.Error
		if errors.As(err, &ue) {
			return ue.Err
		}
		return errRequest
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return statusError(resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return errRequest
	}
	var a struct {
		Response json.RawMessage `json:"response"`
		Error    *struct {
			Code int `json:"error_code"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &a) != nil {
		return errBadAnswer
	}
	if a.Error != nil {
		return apiError(a.Error.Code)
	}
	if len(a.Response) == 0 || json.Unmarshal(a.Response, v) != nil {
		return errBadAnswer
	}
	return nil
}

// pace keeps calls at least Interval apart
func (c *Client) pace(ctx context.Context) error {
	if c.cfg.Interval < 0 {
		return nil
	}
	c.mu.Lock()
	now := time.Now()
	at := c.next
	if at.Before(now) {
		at = now
	}
	c.next = at.Add(c.cfg.Interval)
	c.mu.Unlock()
	if d := at.Sub(now); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	return nil
}

type audioItem struct {
	ID                int64  `json:"id"`
	OwnerID           int64  `json:"owner_id"`
	AccessKey         string `json:"access_key"`
	Artist            string `json:"artist"`
	Title             string `json:"title"`
	Duration          int    `json:"duration"`
	URL               string `json:"url"`
	ContentRestricted int    `json:"content_restricted"`
	Album             *struct {
		Title string `json:"title"`
		Thumb photo  `json:"thumb"`
	} `json:"album"`
}

type photo struct {
	P300  string `json:"photo_300"`
	P600  string `json:"photo_600"`
	P1200 string `json:"photo_1200"`
}

// best is the 600 px picture, else a larger or a smaller one
func (p photo) best() string {
	for _, u := range []string{p.P600, p.P1200, p.P300} {
		if strings.HasPrefix(u, "https://") && len(u) <= 1024 {
			return u
		}
	}
	return ""
}

type itemsAnswer struct {
	Items []audioItem `json:"items"`
}

// SearchMusic answers up to limit songs (1 to MaxLimit) in VK's order.
func (c *Client) SearchMusic(ctx context.Context, query string, limit int) ([]providers.Track, error) {
	limit = min(max(limit, 1), MaxLimit)
	var out []providers.Track
	if c.guard(func() error {
		var a itemsAnswer
		// a few more than asked: songs vk blocks are left out
		q := url.Values{"q": {query}, "count": {strconv.Itoa(limit + 5)}, "sort": {"0"}, "autocomplete": {"1"}}
		if err := c.call(ctx, "audio.search", q, &a); err != nil {
			return err
		}
		out = tracks(a.Items, limit, "")
		return nil
	}) != nil {
		return nil, errFailed
	}
	return out, nil
}

// tracks are the playable items, up to limit, without the one whose base id
// is skip
func tracks(items []audioItem, limit int, skip string) []providers.Track {
	out := make([]providers.Track, 0, min(len(items), limit))
	for _, it := range items {
		if len(out) == limit {
			break
		}
		if t, ok := track(it); ok && (skip == "" || baseID(t.ID) != skip) {
			out = append(out, t)
		}
	}
	return out
}

func track(it audioItem) (providers.Track, bool) {
	// an empty url is a song vk blocks for this account or where the server is
	if it.ID <= 0 || it.OwnerID == 0 || strings.TrimSpace(it.Title) == "" || it.URL == "" || it.ContentRestricted != 0 {
		return providers.Track{}, false
	}
	base := strconv.FormatInt(it.OwnerID, 10) + "_" + strconv.FormatInt(it.ID, 10)
	id := base
	if it.AccessKey != "" {
		id += "_" + it.AccessKey
	}
	if !idRe.MatchString(id) {
		return providers.Track{}, false
	}
	t := providers.Track{
		Source: providers.SourceVK, ID: id, Title: clip(it.Title), Artist: clip(it.Artist),
		DurationS: max(it.Duration, 0), WebURL: "https://vk.com/audio" + base,
	}
	if it.Album != nil {
		t.Album, t.ArtworkURL = clip(it.Album.Title), it.Album.Thumb.best()
	}
	return t, true
}

// idRe is a song's or a list's id: owner, id, then its access key if any
var idRe = regexp.MustCompile(`^-?[0-9]{1,20}_[0-9]{1,20}(_[0-9A-Za-z]{1,64})?$`)

// baseID is an id without its access key
func baseID(id string) string {
	p := strings.SplitN(id, "_", 3)
	if len(p) < 2 {
		return id
	}
	return p[0] + "_" + p[1]
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= maxTextRunes {
		return s
	}
	return string(r[:maxTextRunes])
}
