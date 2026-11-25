// Package yandexmusic finds and streams Yandex Music songs through
// api.music.yandex.net, the API Yandex's own apps call. It needs no account to
// search, read lists and track radio; a song plays whole only with an account's
// OAuth token (a Plus subscription), given as a secret or exchanged from the
// browser's yandex.ru session cookies. The personal My Wave station uses that
// account too. Queries, results, ids, tokens, cookies and upstream bodies never
// reach an error or log.
package yandexmusic

import (
	"bytes"
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
	defaultAPIURL      = "https://api.music.yandex.net"
	defaultPassportURL = "https://mobileproxy.passport.yandex.net" //nolint:gosec // a host, no credential
	defaultOAuthURL    = "https://oauth.mobile.yandex.net"         //nolint:gosec // a host, no credential
	defaultTimeout     = 8 * time.Second
	maxBodyBytes       = 8 << 20
	maxTextRunes       = 200
	// MaxLimit bounds one search's results
	MaxLimit = 10
	// the client the get-file-info sign key belongs to: both change together
	musicClient = "YandexMusicDesktopAppWindows/5.95.0"
	userAgent   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) YandexMusic/5.95.0 Chrome/130.0 Safari/537.36"
	webBase     = "https://music.yandex.ru"
)

// MediaHosts are the hosts Yandex Music serves song files from, for the
// server's stream relay: get-file-info's strm hosts and the legacy get-mp3
// storage hosts.
var MediaHosts = []string{".strm.yandex.net", ".strm.yandex.ru", ".storage.yandex.net"}

type Config struct {
	// APIURL, PassportURL and OAuthURL are for tests; "" is Yandex's own
	APIURL, PassportURL, OAuthURL string
	// Token is an account's OAuth music token; it wins over Cookies
	Token string
	// Cookies are the yandex.ru session cookies (Session_id, sessionid2)
	// exchanged for a token when Token is ""
	Cookies map[string]string
	// Quality is hq, nq or lossless; "" is hq
	Quality string
	// Timeout bounds one call; zero means 8 s
	Timeout time.Duration
	Log     *slog.Logger
	Now     func() time.Time
}

// Client searches and streams Yandex Music: a providers.Music, Collections,
// Streamer and TrackRadio.
type Client struct {
	cfg    Config
	client *http.Client
	// testHost is a test's fake, trusted as a media and a Yandex host
	testHost string

	mu          sync.Mutex
	token       string
	exchangedAt time.Time

	accountMu sync.Mutex
	accountID int64
}

func New(c Config, client *http.Client) *Client {
	trim := func(s *string, def string) {
		if *s == "" {
			*s = def
		}
		*s = strings.TrimRight(*s, "/")
	}
	trim(&c.APIURL, defaultAPIURL)
	trim(&c.PassportURL, defaultPassportURL)
	trim(&c.OAuthURL, defaultOAuthURL)
	if c.Quality == "" {
		c.Quality = "hq"
	}
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
	errBadAnswer = errors.New("yandexmusic: bad answer")
)

// statusError is an upstream answer that is not 200, for the probe's code
type statusError int

func (e statusError) Error() string { return "yandexmusic: status " + strconv.Itoa(int(e)) }

func (c *Client) guard(f func() error) error {
	var err error
	if providers.Guard(c.cfg.Log, "yandex-music", providers.CodeMusicFailed, func() { err = f() }) {
		return errFailed
	}
	return err
}

// flexID is an id Yandex sends as a number or a string
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*f = flexID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexID(n.String())
	return nil
}

var idRe = regexp.MustCompile(`^[0-9]{1,20}$`)

type artistItem struct {
	ID    flexID `json:"id"`
	Name  string `json:"name"`
	Cover *struct {
		URI string `json:"uri"`
	} `json:"cover"`
}

type albumItem struct {
	ID         flexID       `json:"id"`
	Title      string       `json:"title"`
	Version    string       `json:"version"`
	Artists    []artistItem `json:"artists"`
	TrackCount int          `json:"trackCount"`
	CoverURI   string       `json:"coverUri"`
	Available  *bool        `json:"available"`
}

type trackItem struct {
	ID         flexID       `json:"id"`
	Title      string       `json:"title"`
	Version    string       `json:"version"`
	Artists    []artistItem `json:"artists"`
	Albums     []albumItem  `json:"albums"`
	DurationMs int          `json:"durationMs"`
	CoverURI   string       `json:"coverUri"`
	Available  *bool        `json:"available"`
}

// SearchMusic answers up to limit songs (1 to MaxLimit) in Yandex's order.
func (c *Client) SearchMusic(ctx context.Context, query string, limit int) ([]providers.Track, error) {
	limit = min(max(limit, 1), MaxLimit)
	var out []providers.Track
	if c.guard(func() error {
		var r struct {
			Tracks struct {
				Results []trackItem `json:"results"`
			} `json:"tracks"`
		}
		if err := c.get(ctx, "/search", searchQuery(query, "track"), false, &r); err != nil {
			return err
		}
		out = tracks(r.Tracks.Results, limit)
		return nil
	}) != nil {
		return nil, errFailed
	}
	return out, nil
}

func searchQuery(query, kind string) url.Values {
	return url.Values{"text": {query}, "type": {kind}, "page": {"0"}, "nocorrect": {"false"}}
}

func tracks(items []trackItem, limit int) []providers.Track {
	out := make([]providers.Track, 0, min(len(items), limit))
	seen := map[string]bool{}
	for _, it := range items {
		if t, ok := track(it); ok && !seen[t.ID] {
			seen[t.ID] = true
			out = append(out, t)
		}
		if len(out) == limit {
			break
		}
	}
	return out
}

func track(it trackItem) (providers.Track, bool) {
	id := string(it.ID)
	if !idRe.MatchString(id) || strings.TrimSpace(it.Title) == "" || (it.Available != nil && !*it.Available) {
		return providers.Track{}, false
	}
	t := providers.Track{
		Source: providers.SourceYandex, ID: id, Title: clip(titled(it.Title, it.Version)), Artist: clip(names(it.Artists)),
		DurationS: (max(it.DurationMs, 0) + 500) / 1000, ArtworkURL: cover(it.CoverURI), WebURL: webBase + "/track/" + id,
	}
	if len(it.Albums) > 0 {
		a := it.Albums[0]
		t.Album = clip(a.Title)
		if t.ArtworkURL == "" {
			t.ArtworkURL = cover(a.CoverURI)
		}
		if idRe.MatchString(string(a.ID)) {
			t.WebURL = webBase + "/album/" + string(a.ID) + "/track/" + id
		}
	}
	return t, true
}

func titled(title, version string) string {
	title, version = strings.TrimSpace(title), strings.TrimSpace(version)
	if version == "" {
		return title
	}
	return title + " (" + version + ")"
}

func names(as []artistItem) string {
	var n []string
	for _, a := range as {
		if s := strings.TrimSpace(a.Name); s != "" {
			n = append(n, s)
		}
	}
	return strings.Join(n, ", ")
}

var coverRe = regexp.MustCompile(`^[a-z0-9.-]+\.yandex\.(net|ru)/[^\s"<>]*%%[^\s"<>]*$`)

// cover is a cover uri ("avatars.yandex.net/.../%%") at 400x400
func cover(uri string) string {
	if len(uri) > 1024 || !coverRe.MatchString(uri) {
		return ""
	}
	return "https://" + strings.Replace(uri, "%%", "400x400", 1)
}

// get reads one API answer's result into v; auth sends the account's token,
// read again once when the API refuses an exchanged one
func (c *Client) get(ctx context.Context, path string, q url.Values, auth bool, v any) error {
	return c.call(ctx, http.MethodGet, path, q, nil, auth, v)
}

func (c *Client) call(ctx context.Context, method, path string, q, form url.Values, auth bool, v any) error {
	var body []byte
	contentType := ""
	if form != nil {
		body = []byte(form.Encode())
		contentType = "application/x-www-form-urlencoded"
	}
	return c.callBody(ctx, method, path, q, body, contentType, auth, v)
}

// postJSON posts one JSON body with the account's token.
func (c *Client) postJSON(ctx context.Context, path string, q url.Values, body, v any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return errBadAnswer
	}
	return c.callBody(ctx, http.MethodPost, path, q, raw, "application/json", true, v)
}

func (c *Client) callBody(ctx context.Context, method, path string, q url.Values, body []byte, contentType string, auth bool, v any) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	for attempt := 0; ; attempt++ {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		u := c.cfg.APIURL + path
		if len(q) > 0 {
			u += "?" + q.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, method, u, reader)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Accept-Language", "ru")
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("X-Yandex-Music-Client", musicClient)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		exchanged := false
		if auth {
			tok, ex, err := c.accessToken(ctx, attempt > 0)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "OAuth "+tok)
			exchanged = ex
		}
		b, err := c.do(req)
		if errors.Is(err, statusError(http.StatusUnauthorized)) && exchanged && attempt == 0 {
			continue
		}
		if err != nil {
			return err
		}
		var env struct {
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(b, &env) != nil || len(env.Result) == 0 || json.Unmarshal(env.Result, v) != nil {
			return errBadAnswer
		}
		return nil
	}
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= maxTextRunes {
		return s
	}
	return string(r[:maxTextRunes])
}
