// Package zvuk finds and streams Zvuk (zvuk.com, once SberZvuk) songs
// through the GraphQL API the site's own player calls. It needs no account:
// a guest token from the site plays every song whole at 128 kbps; a
// signed-in account's token, optional, plays 320 kbps and flac with a
// subscription. Queries, results, ids, tokens, urls and upstream bodies
// never reach an error or log.
package zvuk

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
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
	defaultBaseURL = "https://zvuk.com"
	defaultTimeout = 8 * time.Second
	maxBodyBytes   = 4 << 20
	maxTextRunes   = 200
	// MaxLimit bounds one search's results
	MaxLimit = 10
	// covers are asked for at this size
	artworkSize = "600x600"
)

// MediaHosts are the hosts Zvuk serves streams from, for the server's stream
// relay: its cdn's progressive mp3 and flac-in-mp4 files.
var MediaHosts = []string{".zvuk.com", ".zvq.me"}

// stream qualities, as the config's quality names them
const (
	QualityMid  = "mid"
	QualityHigh = "high"
	QualityFLAC = "flac"
)

type Config struct {
	// BaseURL is the site; tests point it at a fake
	BaseURL string
	// Token is a signed-in account's token; "" plays as a guest
	Token string
	// Quality is mid, high or flac; "" means high
	Quality string
	// Timeout bounds one call, a guest token's fetch included; zero means 8 s
	Timeout time.Duration
	Log     *slog.Logger
}

// Client searches and streams Zvuk: a providers.Music, Collections, Streamer
// and TrackRadio.
type Client struct {
	cfg    Config
	client *http.Client

	mu    sync.Mutex
	guest string
}

func New(c Config, client *http.Client) *Client {
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.Quality == "" {
		c.Quality = QualityHigh
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	return &Client{cfg: c, client: client}
}

var (
	errFailed    = errors.New(providers.CodeMusicFailed)
	errBadAnswer = errors.New("zvuk: bad answer")
	errGraphQL   = errors.New("zvuk: graphql error")
	errBlocked   = errors.New("zvuk: blocked")
	errNoToken   = errors.New("zvuk: no guest token")
)

// statusError is an upstream answer that is not 200, for the probe's code
type statusError int

func (e statusError) Error() string { return "zvuk: status " + strconv.Itoa(int(e)) }

var (
	idRe    = regexp.MustCompile(`^[0-9]{1,20}$`)
	tokenRe = regexp.MustCompile(`^[0-9A-Za-z._~+/=:-]{8,1000}$`)
)

func (c *Client) guard(f func() error) error {
	var err error
	if providers.Guard(c.cfg.Log, "zvuk", providers.CodeMusicFailed, func() { err = f() }) {
		return errFailed
	}
	return err
}

// zid is an id the API sends as a string or a number
type zid string

func (z *zid) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*z = zid(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*z = zid(n.String())
	return nil
}

type image struct {
	Src string `json:"src"`
}

type named struct {
	Title string `json:"title"`
}

type trackItem struct {
	ID       zid         `json:"id"`
	Title    string      `json:"title"`
	Duration float64     `json:"duration"`
	Artists  []named     `json:"artists"`
	Release  *releaseRef `json:"release"`
}

type releaseRef struct {
	ID    zid    `json:"id"`
	Title string `json:"title"`
	Image *image `json:"image"`
}

// SearchMusic answers up to limit songs (1 to MaxLimit) in Zvuk's order.
func (c *Client) SearchMusic(ctx context.Context, query string, limit int) ([]providers.Track, error) {
	limit = min(max(limit, 1), MaxLimit)
	var out []providers.Track
	if c.guard(func() error {
		var a searchAnswer
		if err := c.search(ctx, query, limit, "tracks", &a); err != nil {
			return err
		}
		out = tracks(a.Search.Tracks.Items, limit)
		return nil
	}) != nil {
		return nil, errFailed
	}
	return out, nil
}

type searchAnswer struct {
	Search struct {
		Tracks    struct{ Items []trackItem }  `json:"tracks"`
		Releases  struct{ Items []listItem }   `json:"releases"`
		Playlists struct{ Items []listItem }   `json:"playlists"`
		Artists   struct{ Items []artistItem } `json:"artists"`
	} `json:"search"`
}

// search runs the search operation with only kind's results asked for
func (c *Client) search(ctx context.Context, query string, limit int, kind string, a *searchAnswer) error {
	vars := map[string]any{"query": query, "limit": limit}
	for _, k := range []string{"tracks", "artists", "releases", "playlists"} {
		vars[k] = k == kind
	}
	return c.graphql(ctx, "search", searchQuery, vars, a)
}

func tracks(items []trackItem, limit int) []providers.Track {
	out := make([]providers.Track, 0, min(len(items), limit))
	for _, it := range items {
		if len(out) == limit {
			break
		}
		if t, ok := track(it); ok {
			out = append(out, t)
		}
	}
	return out
}

func track(it trackItem) (providers.Track, bool) {
	id := string(it.ID)
	if !idRe.MatchString(id) || strings.TrimSpace(it.Title) == "" {
		return providers.Track{}, false
	}
	t := providers.Track{
		Source: providers.SourceZvuk, ID: id, Title: clip(it.Title), Artist: artists(it.Artists),
		DurationS: int(math.Round(max(it.Duration, 0))), WebURL: page("track", id),
	}
	if r := it.Release; r != nil {
		t.Album = clip(r.Title)
		t.ArtworkURL = artwork(r.Image)
	}
	return t, true
}

func artists(as []named) string {
	var names []string
	for _, a := range as {
		if n := strings.TrimSpace(a.Title); n != "" {
			names = append(names, n)
		}
	}
	return clip(strings.Join(names, ", "))
}

// artwork is a cover's https url at 600x600: its {size} template filled,
// a site path made whole, else its size query set (an editorial cover comes
// without one)
func artwork(im *image) string {
	if im == nil || im.Src == "" || len(im.Src) > 1024 {
		return ""
	}
	templated := strings.Contains(im.Src, "{size}")
	src := strings.ReplaceAll(im.Src, "{size}", artworkSize)
	if strings.HasPrefix(src, "/") && !strings.HasPrefix(src, "//") {
		src = defaultBaseURL + src
	}
	u, err := url.Parse(src)
	if err != nil || u.Scheme != "https" || u.Host == "" || strings.Contains(src, "{") {
		return ""
	}
	if templated {
		return u.String()
	}
	q := u.Query()
	q.Set("size", artworkSize)
	u.RawQuery = q.Encode()
	return u.String()
}

func page(kind, id string) string {
	return defaultBaseURL + "/" + kind + "/" + id
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= maxTextRunes {
		return s
	}
	return string(r[:maxTextRunes])
}
