// Package xweb searches posts on X through its web app's own api, signed in as
// the account whose cookies the instance holds (a throwaway one: X may
// suspend an account its web api serves to a program). Queries, posts, the
// cookies and upstream bodies never reach an error or log; a failure is
// logged by its code alone.
package xweb

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"math/rand/v2"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/netscape"
)

const (
	defaultBaseURL   = "https://x.com"
	defaultAssetsURL = "https://abs.twimg.com"
	defaultTimeout   = 15 * time.Second
	// the signing keys and the query id change with x's deploys: a session is read again at most this old
	sessionTTL     = 30 * time.Minute
	maxPageBytes   = 8 << 20
	maxScriptBytes = 16 << 20
	maxBodyBytes   = 8 << 20
	userAgent      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) " +
		"Chrome/141.0.0.0 Safari/537.36"
)

var (
	errFailed = errors.New(providers.CodeXSearchFailed)
	// ErrNoAccount refuses cookies without the session's two: auth_token and ct0 (the csrf token)
	ErrNoAccount = errors.New("xweb: the cookies hold no x.com auth_token and ct0")

	ondemandIndexRe = regexp.MustCompile(`,(\d+):["']ondemand\.s["']`)
	mainScriptRe    = regexp.MustCompile(`/responsive-web/client-web/main\.([0-9a-f]+)\.js`)
	bearerRe        = regexp.MustCompile(`"Bearer (AAAAAAAAAAAAAAAAAAAAA[A-Za-z0-9%]+)"`)
	searchOpRe      = regexp.MustCompile(`queryId:"([\w-]+)",operationName:"SearchTimeline",operationType:"query",` +
		`metadata:\{featureSwitches:\[([^\]]*)\],fieldToggles:\[([^\]]*)\]`)
	quotedRe = regexp.MustCompile(`"([\w]+)"`)
)

type Config struct {
	BaseURL, AssetsURL string
	// Cookies is the cookies.txt netscape.Parse answered
	Cookies []byte
	// Save stores the jar once x renewed a cookie of it (ct0 rotates); nil keeps it in memory
	Save providers.SaveSecret
	// Timeout bounds one search, a session's reads included; zero means 15 s
	Timeout time.Duration
	// Log receives x_search_failed with its code; nil uses slog default
	Log *slog.Logger
	Now func() time.Time
}

// Client is a providers.XSearch.
type Client struct {
	cfg  Config
	http *http.Client

	// cookieMu guards the jar and what is read of it: x renews cookies in its answers
	cookieMu sync.Mutex
	jar      []byte
	cookies  map[string]string
	csrf     string

	mu      sync.Mutex
	session *session
}

// session is what one read of the web app gives a search
type session struct {
	at                     time.Time
	signer                 *signer
	bearer, queryID        string
	features, fieldToggles string
}

// failure is a search that went wrong, by its log code; reset says the session is stale
type failure struct {
	code  string
	reset bool
}

func (f *failure) Error() string { return f.code }

func New(c Config, client *http.Client) (*Client, error) {
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	if c.AssetsURL == "" {
		c.AssetsURL = defaultAssetsURL
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	c.BaseURL, c.AssetsURL = strings.TrimRight(c.BaseURL, "/"), strings.TrimRight(c.AssetsURL, "/")
	cookies := netscape.For(c.Cookies, "x.com")
	if cookies["auth_token"] == "" || cookies["ct0"] == "" {
		return nil, ErrNoAccount
	}
	return &Client{cfg: c, http: client, jar: c.Cookies, cookies: cookies, csrf: cookies["ct0"]}, nil
}

// SearchPosts answers up to limit posts (1 to 20) for an X search query, its
// operators included; latest asks for the newest first instead of the top.
// A stale session (x deployed, the signing keys changed) is read again once.
func (c *Client) SearchPosts(ctx context.Context, query string, latest bool, limit int) ([]providers.Post, error) {
	var out []providers.Post
	var err error
	if providers.Guard(c.cfg.Log, "x-web", providers.CodeXSearchFailed, func() { out, err = c.search(ctx, query, latest, limit) }) {
		return nil, errFailed
	}
	if err != nil {
		code := "failed"
		var f *failure
		if errors.As(err, &f) {
			code = f.code
		}
		c.cfg.Log.Warn("x_search_failed", "provider", "x-web", "code", code)
		return nil, errFailed
	}
	return out, nil
}

func (c *Client) search(ctx context.Context, query string, latest bool, limit int) ([]providers.Post, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	limit = min(max(limit, 1), MaxLimit)
	for attempt := 0; ; attempt++ {
		s, err := c.current(ctx)
		if err != nil {
			return nil, err
		}
		posts, err := c.searchOnce(ctx, s, query, latest, limit)
		var f *failure
		if errors.As(err, &f) && f.reset {
			c.drop(s)
			if attempt == 0 {
				continue
			}
		}
		return posts, err
	}
}

func (c *Client) current(ctx context.Context) (*session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil && c.cfg.Now().Sub(c.session.at) < sessionTTL {
		return c.session, nil
	}
	s, err := c.load(ctx)
	if err != nil {
		return nil, err
	}
	c.session = s
	return s, nil
}

func (c *Client) drop(s *session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == s {
		c.session = nil
	}
}

// load reads the signed-in home page, then the scripts it names: the signing parts, the search's query id and
// features, and the web app's bearer
func (c *Client) load(ctx context.Context) (*session, error) {
	home, err := c.get(ctx, c.cfg.BaseURL+"/home", maxPageBytes, true)
	if err != nil {
		return nil, err
	}
	m := ondemandIndexRe.FindStringSubmatch(home)
	if m == nil {
		return nil, &failure{code: "no_signer"}
	}
	hash := regexp.MustCompile(`,` + m[1] + `:"([0-9a-f]+)"`).FindStringSubmatch(home)
	main := mainScriptRe.FindStringSubmatch(home)
	if hash == nil || main == nil {
		return nil, &failure{code: "no_signer"}
	}
	ondemand, err := c.get(ctx, c.cfg.AssetsURL+"/responsive-web/client-web/ondemand.s."+hash[1]+"a.js", maxScriptBytes, false)
	if err != nil {
		return nil, err
	}
	sig, err := newSigner(home, ondemand)
	if err != nil {
		return nil, &failure{code: "no_signer"}
	}
	script, err := c.get(ctx, c.cfg.AssetsURL+"/responsive-web/client-web/main."+main[1]+".js", maxScriptBytes, false)
	if err != nil {
		return nil, err
	}
	op := searchOpRe.FindStringSubmatch(script)
	bearer := bearerRe.FindStringSubmatch(script)
	if op == nil || bearer == nil {
		return nil, &failure{code: "no_operation"}
	}
	features := map[string]bool{}
	for _, f := range quotedRe.FindAllStringSubmatch(op[2], -1) {
		// the value the page's own feature switches give it; one it does not list is off
		features[f[1]] = regexp.MustCompile(`"` + regexp.QuoteMeta(f[1]) + `":\{"value":true`).MatchString(home)
	}
	toggles := map[string]bool{}
	for _, f := range quotedRe.FindAllStringSubmatch(op[3], -1) {
		toggles[f[1]] = false
	}
	// json.Marshal writes a map's keys sorted, so a request's bytes do not change between sessions
	fj, _ := json.Marshal(features)
	tj, _ := json.Marshal(toggles)
	return &session{at: c.cfg.Now(), signer: sig, bearer: bearer[1], queryID: op[1], features: string(fj), fieldToggles: string(tj)}, nil
}

func (c *Client) cookieHeader() string {
	c.cookieMu.Lock()
	defer c.cookieMu.Unlock()
	names := slices.Sorted(maps.Keys(c.cookies))
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n + "=" + c.cookies[n]
	}
	return strings.Join(parts, "; ")
}

// get reads a page or script; the home page alone carries the cookies, and a redirect from it means they no longer
// sign in
func (c *Client) get(ctx context.Context, url string, limit int64, signedIn bool) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", &failure{code: "request"}
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if signedIn {
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Cookie", c.cookieHeader())
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if signedIn && isRedirect(err) {
			return "", &failure{code: "signed_out"}
		}
		return "", sendFailure(ctx)
	}
	defer func() { _ = resp.Body.Close() }()
	if signedIn {
		c.absorb(resp)
	}
	if resp.StatusCode != http.StatusOK {
		return "", &failure{code: statusCode("page_status_", resp.StatusCode)}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return "", sendFailure(ctx)
	}
	return string(b), nil
}

func sendFailure(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &failure{code: "timeout"}
	}
	return &failure{code: "send"}
}

func statusCode(prefix string, n int) string { return prefix + strconv.Itoa(n) }

func randomByte() byte { return byte(rand.IntN(256)) } //nolint:gosec // the xor byte is no secret
