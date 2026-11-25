// Package streams relays a stream the server resolved to the device that
// plays it. A YouTube stream URL is bound to the address that asked for it,
// and the server may reach the internet from another address than the device
// (a VPN exit): the device fetches GET /v2/stream/{token} (and /{token}/{n}
// for the n-th upstream of a set: a screen's audio and its renditions) from
// the server, and the server fetches the stream from the same address yt-dlp
// asked from. An HLS playlist is rewritten so that every playlist and segment
// it names is fetched through the relay too, by a url sealed with the relay's
// own key. Upstream URLs never leave the server, and lines carry the device,
// codes, byte counts and timings only.
package streams

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"golang.org/x/time/rate"
)

const (
	// a stream stays fetchable this long by default, and never past its
	// upstream's own expire
	defaultTTL = 6 * time.Hour
	// DefaultTTL is defaultTTL as the server's own callers read it (the MCP
	// endpoint's link expiry)
	DefaultTTL = defaultTTL
	// a film may ask for longer, up to this
	maxTTL = 24 * time.Hour
	// at most this many tokens are kept, the oldest dropped first
	maxEntries = 64
	// at most this many upstreams in one set
	MaxUpstreams = 16
	// one fetch (a range of the track) runs at most this long
	fetchTimeout = 2 * time.Hour
	dialTimeout  = 10 * time.Second
	// a playlist is read whole to rewrite it, at most this long
	maxPlaylist = 4 << 20
	// default caps per device: fetches at once (a player opens video, audio and a prefetch) and bytes a second
	// (about 200 Mbit/s, twice a 4K stream)
	defaultPerDevice = 8
	defaultRate      = 25 << 20
)

// the default upstream hosts: YouTube's media servers, its HLS manifests among them (manifest.googlevideo.com)
var defaultHosts = []string{".googlevideo.com"}

var (
	ErrUpstream = errors.New("streams: upstream refused")
	errRedirect = errors.New("streams: redirect off the media hosts")
)

// headers passed from the device's request upstream, and back
var (
	requestHeaders  = []string{"Range", "If-Range"}
	responseHeaders = []string{"Content-Type", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"}
)

// Upstream is one url of a set. HLS marks a playlist, which the relay rewrites.
type Upstream struct {
	URL string
	HLS bool
}

// Options shape a set. TTL is how long its token lives, at most 24 h, zero for 6 h; the upstreams' own expire
// shortens it.
type Options struct {
	TTL time.Duration
	// OnFirstByte runs once, when the set's first body bytes reach the relay's
	// device-bound response. It carries no upstream text or url.
	OnFirstByte func()
}

type entry struct {
	upstreams []Upstream
	deviceID  string
	expires   time.Time
	firstByte *sync.Once
	onFirst   func()
}

type Relay struct {
	log    *slog.Logger
	now    func() time.Time
	client *http.Client
	hosts  []string
	seal   *sealer

	perDevice int
	rate      rate.Limit

	mu      sync.Mutex
	entries map[string]entry
	order   []string
	devices map[string]*deviceCap
}

// deviceCap is a device's fetches in flight and its share of bandwidth
type deviceCap struct {
	active  int
	limiter *rate.Limiter
}

type Config struct {
	Log *slog.Logger
	Now func() time.Time
	// Client fetches upstream; nil is one that dials IPv4 alone (yt-dlp
	// asks over IPv4) and follows redirects among the media hosts only
	Client *http.Client
	// Hosts are the upstream host suffixes allowed (".googlevideo.com", or a whole name "a.example"); nil is
	// YouTube's media hosts
	Hosts []string
	// PerDevice bounds a device's fetches at once (zero: 8, negative: none); one more answers 429
	PerDevice int
	// BytesPerSecond bounds a device's relayed bytes a second over all its fetches (zero: 25 MiB/s, negative: none)
	BytesPerSecond int
}

func New(c Config) *Relay {
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if len(c.Hosts) == 0 {
		c.Hosts = defaultHosts
	}
	r := &Relay{
		log: c.Log, now: c.Now, client: c.Client, hosts: c.Hosts, seal: newSealer(),
		perDevice: c.PerDevice, rate: rate.Inf,
		entries: map[string]entry{}, devices: map[string]*deviceCap{},
	}
	if r.perDevice == 0 {
		r.perDevice = defaultPerDevice
	}
	switch {
	case c.BytesPerSecond == 0:
		r.rate = rate.Limit(defaultRate)
	case c.BytesPerSecond > 0:
		r.rate = rate.Limit(c.BytesPerSecond)
	}
	if r.client == nil {
		r.client = r.upstreamClient()
	}
	return r
}

func (r *Relay) upstreamClient() *http.Client {
	d := &net.Dialer{Timeout: dialTimeout}
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return d.DialContext(ctx, "tcp4", addr)
			},
			TLSHandshakeTimeout:   dialTimeout,
			ResponseHeaderTimeout: 30 * time.Second,
			// the device asks for media as it is: no transparent gzip
			DisableCompression: true,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || !r.mediaHost(req.URL) {
				return errRedirect
			}
			return nil
		},
	}
}

func (r *Relay) mediaHost(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, s := range r.hosts {
		s = strings.ToLower(s)
		if strings.HasPrefix(s, ".") && strings.HasSuffix(h, s) || h == s {
			return true
		}
	}
	return false
}

// Add keeps upstream (an https url on the media hosts) for the device
// and answers the token it is fetched by: 32 random bytes, which only the
// device's play command carries.
func (r *Relay) Add(deviceID, upstream string) (string, error) {
	return r.AddSet(deviceID, []Upstream{{URL: upstream}}, Options{})
}

// AddSet keeps upstreams for the device under one token: the n-th is fetched at /stream/{token}/{n}, the first
// at /stream/{token} too.
func (r *Relay) AddSet(deviceID string, ups []Upstream, o Options) (string, error) {
	if len(ups) == 0 || len(ups) > MaxUpstreams {
		return "", ErrUpstream
	}
	now := r.now()
	ttl := o.TTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	expires := now.Add(min(ttl, maxTTL))
	for _, up := range ups {
		u, err := url.Parse(up.URL)
		if err != nil || !r.mediaHost(u) {
			return "", ErrUpstream
		}
		if e := Expiry(u); !e.IsZero() && e.Before(expires) {
			expires = e
		}
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(now)
	r.entries[token] = entry{upstreams: append([]Upstream(nil), ups...), deviceID: deviceID, expires: expires,
		firstByte: &sync.Once{}, onFirst: o.OnFirstByte}
	r.order = append(r.order, token)
	for len(r.order) > maxEntries {
		delete(r.entries, r.order[0])
		r.order = r.order[1:]
	}
	return token, nil
}

// Expires answers when the token stops working, false for one the relay does not keep
func (r *Relay) Expires(token string) (time.Time, bool) {
	e, ok := r.get(token)
	return e.expires, ok
}

// Drop forgets a token (a session replaced by another)
func (r *Relay) Drop(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, token)
}

// Expiry reads a googlevideo url's expire, in its query or its path (/expire/<unix>/), zero without one
func Expiry(u *url.URL) time.Time {
	v := u.Query().Get("expire")
	if v == "" {
		parts := strings.Split(u.Path, "/")
		for i := 0; i+1 < len(parts); i++ {
			if parts[i] == "expire" {
				v = parts[i+1]
				break
			}
		}
	}
	s, err := strconv.ParseInt(v, 10, 64)
	if err != nil || s <= 0 {
		return time.Time{}
	}
	return time.Unix(s, 0)
}

func (r *Relay) get(token string) (entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(r.now())
	e, ok := r.entries[token]
	return e, ok
}

// prune drops what expired; r.mu is held
func (r *Relay) prune(now time.Time) {
	kept := r.order[:0]
	for _, t := range r.order {
		if e, ok := r.entries[t]; ok && now.Before(e.expires) {
			kept = append(kept, t)
			continue
		}
		delete(r.entries, t)
	}
	r.order = kept
}

// acquire takes one of the device's fetch slots; false when it has none left
func (r *Relay) acquire(dev string) (*rate.Limiter, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.devices[dev]
	if c == nil {
		c = &deviceCap{limiter: rate.NewLimiter(r.rate, burst(r.rate))}
		r.devices[dev] = c
	}
	if r.perDevice > 0 && c.active >= r.perDevice {
		return nil, false
	}
	c.active++
	return c.limiter, true
}

func (r *Relay) release(dev string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.devices[dev]; c != nil {
		c.active--
		if c.active <= 0 {
			delete(r.devices, dev)
		}
	}
}

// a quarter second of bytes, at least one read's worth
func burst(l rate.Limit) int {
	if l == rate.Inf {
		return 0
	}
	return max(int(l)/4, readChunk)
}

// Mount serves GET /stream/{token}, /stream/{token}/{n} and an HLS playlist's
// /stream/{token}/h/{sealed} on the device API: the upstream's answer (200,
// 206 for a range, 416) with its body; 404 for a token or index it does not
// keep, 429 past the device's fetches at once, 502 when the upstream does not
// answer.
func (r *Relay) Mount(rt fiber.Router) {
	rt.Get("/stream/:token", func(c fiber.Ctx) error { return r.serveIndex(c, "0") })
	rt.Get("/stream/:token/:n", func(c fiber.Ctx) error { return r.serveIndex(c, c.Params("n")) })
	rt.Get("/stream/:token/h/:sealed", r.serveChild)
}

func (r *Relay) serveIndex(c fiber.Ctx, n string) error {
	token := c.Params("token")
	e, ok := r.get(token)
	if !ok {
		return c.SendStatus(fiber.StatusNotFound)
	}
	i, err := strconv.Atoi(n)
	if err != nil || i < 0 || i >= len(e.upstreams) || strconv.Itoa(i) != n {
		return c.SendStatus(fiber.StatusNotFound)
	}
	up := e.upstreams[i]
	return r.fetch(c, token, e, up.URL, up.HLS, i)
}

func (r *Relay) serveChild(c fiber.Ctx) error {
	token := c.Params("token")
	e, ok := r.get(token)
	if !ok || !hasHLS(e) {
		return c.SendStatus(fiber.StatusNotFound)
	}
	raw, ok := r.seal.open(token, c.Params("sealed"))
	if !ok {
		return c.SendStatus(fiber.StatusNotFound)
	}
	u, err := url.Parse(raw)
	if err != nil || !r.mediaHost(u) {
		return c.SendStatus(fiber.StatusNotFound)
	}
	return r.fetch(c, token, e, raw, false, -1)
}

func hasHLS(e entry) bool {
	for _, u := range e.upstreams {
		if u.HLS {
			return true
		}
	}
	return false
}

func (e entry) notifyFirst() {
	if e.onFirst != nil {
		e.firstByte.Do(e.onFirst)
	}
}

// fetch relays one upstream url; playlist marks an HLS playlist, read whole and rewritten (as is any answer whose
// type names one, in a set with HLS); index is the set's, -1 for a playlist's child
func (r *Relay) fetch(c fiber.Ctx, token string, e entry, upstream string, playlist bool, index int) error {
	sctx, sp := startRelaySpan(c, e.deviceID, index, playlist)
	limiter, ok := r.acquire(e.deviceID)
	if !ok {
		sp.end(fiber.StatusTooManyRequests)
		r.log.Warn("stream_relay_failed", "device_id", e.deviceID, "code", "busy")
		return c.SendStatus(fiber.StatusTooManyRequests)
	}
	// the slot is freed when the handler returns, or, for a body sent after it, when that body closes
	streaming := false
	defer func() {
		if !streaming {
			r.release(e.deviceID)
			sp.end(c.Response().StatusCode())
		}
	}()
	ctx, cancel := context.WithTimeout(sctx, fetchTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream, nil)
	if err != nil {
		cancel()
		return c.SendStatus(fiber.StatusBadGateway)
	}
	if !playlist {
		for _, h := range requestHeaders {
			if v := c.Get(h); v != "" {
				req.Header.Set(h, v)
			}
		}
	}
	start := r.now()
	resp, err := r.client.Do(req)
	if err != nil {
		cancel()
		r.log.Warn("stream_relay_failed", "device_id", e.deviceID, "index", index, "code", "unreachable")
		return c.SendStatus(fiber.StatusBadGateway)
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent, http.StatusRequestedRangeNotSatisfiable:
	default:
		_ = resp.Body.Close()
		cancel()
		r.log.Warn("stream_relay_failed", "device_id", e.deviceID, "index", index, "code", "status", "status", resp.StatusCode)
		return c.SendStatus(fiber.StatusBadGateway)
	}
	if playlist || hasHLS(e) && resp.StatusCode == http.StatusOK && isPlaylistType(resp.Header.Get("Content-Type")) {
		defer cancel()
		defer func() { _ = resp.Body.Close() }()
		return r.sendPlaylist(c, token, e, upstream, resp, index, start)
	}
	for _, h := range responseHeaders {
		if v := resp.Header.Get(h); v != "" {
			c.Set(h, v)
		}
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Status(resp.StatusCode)
	r.log.Info("stream_relay", "device_id", e.deviceID, "index", index, "status", resp.StatusCode,
		"ranged", c.Get("Range") != "", "ms", r.now().Sub(start).Milliseconds())
	// the body is sent after the handler returns: its Close, when the send
	// ends or the device leaves, ends the upstream fetch and frees the slot
	streaming = true
	body := &closer{ReadCloser: resp.Body, limiter: limiter, ctx: ctx, first: e.notifyFirst, span: sp,
		done: func() { cancel(); r.release(e.deviceID); sp.end(resp.StatusCode) }}
	size := -1
	if resp.ContentLength >= 0 {
		size = int(resp.ContentLength)
	}
	return c.SendStream(body, size)
}

func isPlaylistType(t string) bool {
	t = strings.ToLower(t)
	return strings.Contains(t, "mpegurl")
}

func (r *Relay) sendPlaylist(c fiber.Ctx, token string, e entry, upstream string, resp *http.Response, index int, start time.Time) error {
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxPlaylist+1))
	if err != nil || len(b) > maxPlaylist {
		r.log.Warn("stream_relay_failed", "device_id", e.deviceID, "index", index, "code", "playlist")
		return c.SendStatus(fiber.StatusBadGateway)
	}
	base, _ := url.Parse(upstream)
	prefix := c.Path()
	if i := strings.Index(prefix, "/stream/"); i >= 0 {
		prefix = prefix[:i]
	}
	out, n, ok := rewritePlaylist(string(b), base, func(u *url.URL) (string, bool) {
		if !r.mediaHost(u) {
			return "", false
		}
		s, err := r.seal.seal(token, u.String())
		if err != nil {
			return "", false
		}
		return prefix + "/stream/" + token + "/h/" + s, true
	})
	if !ok {
		r.log.Warn("stream_relay_failed", "device_id", e.deviceID, "index", index, "code", "playlist_host")
		return c.SendStatus(fiber.StatusBadGateway)
	}
	c.Set(fiber.HeaderContentType, "application/vnd.apple.mpegurl")
	c.Set(fiber.HeaderCacheControl, "no-store")
	r.log.Info("stream_relay", "device_id", e.deviceID, "index", index, "status", http.StatusOK, "playlist", true,
		"uris", n, "ms", r.now().Sub(start).Milliseconds())
	e.notifyFirst()
	return c.Status(http.StatusOK).SendString(out)
}

// readChunk bounds one read of a throttled body, so a wait is short
const readChunk = 32 << 10

type closer struct {
	io.ReadCloser
	done    func()
	once    sync.Once
	limiter *rate.Limiter
	ctx     context.Context
	first   func()
	span    *relaySpan
}

func (c *closer) Read(p []byte) (int, error) {
	if c.limiter != nil && c.limiter.Limit() != rate.Inf && len(p) > readChunk {
		p = p[:readChunk]
	}
	n, err := c.ReadCloser.Read(p)
	if n > 0 && c.limiter != nil && c.limiter.Limit() != rate.Inf {
		if werr := c.limiter.WaitN(c.ctx, n); werr != nil && err == nil {
			err = werr
		}
	}
	if n > 0 && c.first != nil {
		c.first()
	}
	c.span.moved(n, err != nil)
	return n, err
}

func (c *closer) Close() error {
	err := c.ReadCloser.Close()
	c.once.Do(c.done)
	return err
}
