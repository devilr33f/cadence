// Package soundcloud finds and streams SoundCloud songs through api-v2, the
// API soundcloud.com's own player calls. It needs no account: the site's
// public client id is read from its scripts, or given as a secret; a Go+
// account's OAuth token, also optional, plays the songs SoundCloud otherwise
// cuts to a 30 s preview. Queries, results, ids, keys and upstream bodies
// never reach an error or log.
package soundcloud

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
	defaultSiteURL = "https://soundcloud.com"
	defaultAPIURL  = "https://api-v2.soundcloud.com"
	defaultTimeout = 8 * time.Second
	maxBodyBytes   = 4 << 20
	maxScriptBytes = 8 << 20
	maxTextRunes   = 200
	// MaxLimit bounds one search's results
	MaxLimit = 10
	// a client id read from the site is read again after this long, or at once when the API refuses it
	clientIDTTL = 12 * time.Hour
	userAgent   = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36"
)

// MediaHosts are the hosts SoundCloud serves streams from, for the server's
// stream relay: its HLS playlists and segments, and its progressive mp3s.
var MediaHosts = []string{".media-streaming.soundcloud.cloud", ".sndcdn.com"}

type Config struct {
	SiteURL, APIURL string
	// ClientID is a fixed client id; "" reads the site's own from its scripts
	ClientID string
	// OAuthToken is a signed-in account's token (Go+ plays whole songs); "" plays as a guest
	OAuthToken string
	// Timeout bounds one call, the client id's read included; zero means 8 s
	Timeout time.Duration
	Log     *slog.Logger
	Now     func() time.Time
}

// Client searches and streams SoundCloud: a providers.Music, Collections,
// Streamer and TrackRadio.
type Client struct {
	cfg    Config
	client *http.Client

	mu       sync.Mutex
	clientID string
	readAt   time.Time
}

func New(c Config, client *http.Client) *Client {
	if c.SiteURL == "" {
		c.SiteURL = defaultSiteURL
	}
	if c.APIURL == "" {
		c.APIURL = defaultAPIURL
	}
	c.SiteURL = strings.TrimRight(c.SiteURL, "/")
	c.APIURL = strings.TrimRight(c.APIURL, "/")
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &Client{cfg: c, client: client}
}

var (
	errFailed    = errors.New(providers.CodeMusicFailed)
	errBadAnswer = errors.New("soundcloud: bad answer")
	errNoID      = errors.New("soundcloud: no client id")
)

// statusError is an upstream answer that is not 200, for the probe's code
type statusError int

func (e statusError) Error() string { return "soundcloud: status " + strconv.Itoa(int(e)) }

func (c *Client) guard(f func() error) error {
	var err error
	if providers.Guard(c.cfg.Log, "soundcloud", providers.CodeMusicFailed, func() { err = f() }) {
		return errFailed
	}
	return err
}

// SearchMusic answers up to limit songs (1 to MaxLimit) in SoundCloud's order.
func (c *Client) SearchMusic(ctx context.Context, query string, limit int) ([]providers.Track, error) {
	var out []providers.Track
	if c.guard(func() error {
		var err error
		out, err = c.searchTracks(ctx, query, limit)
		return err
	}) != nil {
		return nil, errFailed
	}
	return out, nil
}

func (c *Client) searchTracks(ctx context.Context, query string, limit int) ([]providers.Track, error) {
	limit = min(max(limit, 1), MaxLimit)
	var a struct {
		Collection []trackItem `json:"collection"`
	}
	// a few more than asked: songs SoundCloud blocks where the server is are left out
	if err := c.get(ctx, "/search/tracks", url.Values{"q": {query}, "limit": {strconv.Itoa(limit + 5)}}, &a); err != nil {
		return nil, err
	}
	return tracks(a.Collection, limit), nil
}

type trackItem struct {
	ID           int64  `json:"id"`
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	Duration     int    `json:"duration"`
	FullDuration int    `json:"full_duration"`
	Policy       string `json:"policy"`
	Streamable   *bool  `json:"streamable"`
	Permalink    string `json:"permalink_url"`
	Artwork      string `json:"artwork_url"`
	PublisherMD  *struct {
		Artist     string `json:"artist"`
		AlbumTitle string `json:"album_title"`
	} `json:"publisher_metadata"`
	User struct {
		Username string `json:"username"`
		Avatar   string `json:"avatar_url"`
	} `json:"user"`
	TrackAuthorization string `json:"track_authorization"`
	Media              struct {
		Transcodings []transcoding `json:"transcodings"`
	} `json:"media"`
}

type transcoding struct {
	URL     string `json:"url"`
	Preset  string `json:"preset"`
	Snipped bool   `json:"snipped"`
	Format  struct {
		Protocol string `json:"protocol"`
		MimeType string `json:"mime_type"`
	} `json:"format"`
}

func tracks(items []trackItem, limit int) []providers.Track {
	out := make([]providers.Track, 0, min(len(items), limit))
	for _, it := range items {
		if t, ok := track(it); ok {
			out = append(out, t)
		}
		if len(out) == limit {
			break
		}
	}
	return out
}

func track(it trackItem) (providers.Track, bool) {
	if it.ID <= 0 || strings.TrimSpace(it.Title) == "" || (it.Kind != "" && it.Kind != "track") ||
		it.Policy == "BLOCK" || (it.Streamable != nil && !*it.Streamable) {
		return providers.Track{}, false
	}
	artist, album := it.User.Username, ""
	if md := it.PublisherMD; md != nil {
		if strings.TrimSpace(md.Artist) != "" {
			artist = md.Artist
		}
		album = md.AlbumTitle
	}
	dur := it.FullDuration
	if dur <= 0 {
		dur = it.Duration
	}
	return providers.Track{
		Source: providers.SourceSoundCloud, ID: strconv.FormatInt(it.ID, 10), Title: clip(it.Title), Artist: clip(artist),
		Album: clip(album), DurationS: (dur + 500) / 1000, ArtworkURL: artwork(it.Artwork, it.User.Avatar),
		WebURL: page(it.Permalink),
	}, true
}

// artwork is the song's cover, else its uploader's picture, at 500x500
func artwork(urls ...string) string {
	for _, u := range urls {
		if strings.HasPrefix(u, "https://") && len(u) <= 1024 && !strings.Contains(u, "default_avatar") {
			return strings.Replace(u, "-large.", "-t500x500.", 1)
		}
	}
	return ""
}

func page(u string) string {
	if strings.HasPrefix(u, "https://soundcloud.com/") && len(u) <= 1024 {
		return u
	}
	return ""
}

var idRe = regexp.MustCompile(`^[0-9]{1,20}$`)

// StreamTrack answers a stream of a song this provider found: its
// transcodings are read again (their authorization ages), then the whole
// song's progressive mp3, else its HLS AAC, else its HLS mp3. A song
// SoundCloud cuts to a preview for this account is ErrSignInRequired, one it
// blocks ErrUnplayable.
func (c *Client) StreamTrack(ctx context.Context, t providers.Track) (providers.Stream, error) {
	if t.Source != providers.SourceSoundCloud || !idRe.MatchString(t.ID) {
		return providers.Stream{}, providers.ErrUnplayable
	}
	var out providers.Stream
	err := c.guard(func() error {
		var err error
		out, err = c.stream(ctx, t.ID)
		return err
	})
	switch {
	case err == nil:
		return out, nil
	case errors.Is(err, providers.ErrUnplayable), errors.Is(err, providers.ErrSignInRequired):
		return providers.Stream{}, err
	case errors.Is(err, statusError(http.StatusNotFound)), errors.Is(err, statusError(http.StatusForbidden)):
		return providers.Stream{}, providers.ErrUnplayable
	}
	return providers.Stream{}, errors.New(providers.CodePlayerFailed)
}

func (c *Client) stream(ctx context.Context, id string) (providers.Stream, error) {
	var it trackItem
	if err := c.get(ctx, "/tracks/"+id, url.Values{}, &it); err != nil {
		return providers.Stream{}, err
	}
	if it.Policy == "BLOCK" || (it.Streamable != nil && !*it.Streamable) {
		return providers.Stream{}, providers.ErrUnplayable
	}
	tc, ok := pick(it.Media.Transcodings)
	if !ok {
		if len(it.Media.Transcodings) > 0 {
			return providers.Stream{}, providers.ErrSignInRequired
		}
		return providers.Stream{}, providers.ErrUnplayable
	}
	// a transcoding is fetched from the api alone, with the client id
	path, ok := strings.CutPrefix(tc.URL, c.cfg.APIURL+"/")
	if !ok || strings.ContainsAny(path, "?#") {
		return providers.Stream{}, errBadAnswer
	}
	q := url.Values{}
	if it.TrackAuthorization != "" {
		q.Set("track_authorization", it.TrackAuthorization)
	}
	var a struct {
		URL string `json:"url"`
	}
	if err := c.get(ctx, "/"+path, q, &a); err != nil {
		return providers.Stream{}, err
	}
	if !strings.HasPrefix(a.URL, "https://") {
		return providers.Stream{}, errBadAnswer
	}
	dur := it.FullDuration
	if dur <= 0 {
		dur = it.Duration
	}
	return providers.Stream{URL: a.URL, HLS: tc.Format.Protocol == "hls", DurationS: (dur + 500) / 1000}, nil
}

// pick is the transcoding of the whole song the device plays best; ok is
// false when every one is a preview or none is one it plays
func pick(tcs []transcoding) (transcoding, bool) {
	rank := func(tc transcoding) int {
		mime := tc.Format.MimeType
		switch {
		case tc.Snipped || tc.URL == "":
			return 0
		case tc.Format.Protocol == "progressive" && strings.HasPrefix(mime, "audio/mpeg"):
			return 4
		case tc.Format.Protocol == "hls" && strings.HasPrefix(mime, "audio/mp4") && strings.Contains(tc.Preset, "160"):
			return 3
		case tc.Format.Protocol == "hls" && strings.HasPrefix(mime, "audio/mp4"):
			return 2
		case tc.Format.Protocol == "hls" && strings.HasPrefix(mime, "audio/mpeg") && !strings.Contains(mime, "url"):
			return 1
		}
		return 0
	}
	best, score := transcoding{}, 0
	for _, tc := range tcs {
		if r := rank(tc); r > score {
			best, score = tc, r
		}
	}
	return best, score > 0
}

// TrackRadio answers up to limit songs SoundCloud relates to the seed.
func (c *Client) TrackRadio(ctx context.Context, seed providers.Track, limit int) ([]providers.Track, error) {
	if seed.Source != providers.SourceSoundCloud || !idRe.MatchString(seed.ID) || limit < 1 {
		return nil, errFailed
	}
	var out []providers.Track
	if c.guard(func() error {
		var a struct {
			Collection []trackItem `json:"collection"`
		}
		if err := c.get(ctx, "/tracks/"+seed.ID+"/related", url.Values{"limit": {strconv.Itoa(min(limit, 50))}}, &a); err != nil {
			return err
		}
		out = tracks(a.Collection, limit)
		return nil
	}) != nil {
		return nil, errFailed
	}
	return out, nil
}

// get reads one api-v2 answer into v with the client id; an id the API
// refuses (401 or 403 without a path's own reason) is read again once
func (c *Client) get(ctx context.Context, path string, q url.Values, v any) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	for attempt := 0; ; attempt++ {
		id, err := c.id(ctx, attempt > 0)
		if err != nil {
			return err
		}
		q.Set("client_id", id)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIURL+path+"?"+q.Encode(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent)
		if c.cfg.OAuthToken != "" {
			req.Header.Set("Authorization", "OAuth "+c.cfg.OAuthToken)
		}
		b, err := c.do(req, maxBodyBytes)
		if errors.Is(err, statusError(http.StatusUnauthorized)) && attempt == 0 && c.cfg.ClientID == "" {
			continue
		}
		if err != nil {
			return err
		}
		if json.Unmarshal(b, v) != nil {
			return errBadAnswer
		}
		return nil
	}
}

// id is the client id: the configured one, else the site's, read again when
// stale or refused
func (c *Client) id(ctx context.Context, refresh bool) (string, error) {
	if c.cfg.ClientID != "" {
		return c.cfg.ClientID, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !refresh && c.clientID != "" && c.cfg.Now().Sub(c.readAt) < clientIDTTL {
		return c.clientID, nil
	}
	id, err := c.readClientID(ctx)
	if err != nil {
		return "", err
	}
	c.clientID, c.readAt = id, c.cfg.Now()
	return id, nil
}

var (
	scriptRe   = regexp.MustCompile(`<script[^>]+src="(https://a-v2\.sndcdn\.com/assets/[^"]+\.js)"`)
	clientIDRe = regexp.MustCompile(`client_id\s*[:=]\s*"([0-9A-Za-z]{32})"`)
)

// readClientID finds the client id in the site's scripts, the last ones
// first (the app's own bundle comes after its libraries)
func (c *Client) readClientID(ctx context.Context) (string, error) {
	page, err := c.fetch(ctx, c.cfg.SiteURL+"/", maxBodyBytes)
	if err != nil {
		return "", err
	}
	scripts := scriptRe.FindAllStringSubmatch(string(page), -1)
	if c.cfg.SiteURL != defaultSiteURL {
		// a test's site names its scripts on its own host
		scripts = regexp.MustCompile(`<script[^>]+src="(`+regexp.QuoteMeta(c.cfg.SiteURL)+`/[^"]+\.js)"`).FindAllStringSubmatch(string(page), -1)
	}
	for i := len(scripts) - 1; i >= 0 && i >= len(scripts)-8; i-- {
		js, err := c.fetch(ctx, scripts[i][1], maxScriptBytes)
		if err != nil {
			continue
		}
		if m := clientIDRe.FindSubmatch(js); m != nil {
			return string(m[1]), nil
		}
	}
	return "", errNoID
}

func (c *Client) fetch(ctx context.Context, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	return c.do(req, limit)
}

func (c *Client) do(req *http.Request, limit int64) ([]byte, error) {
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= maxTextRunes {
		return s
	}
	return string(r[:maxTextRunes])
}
