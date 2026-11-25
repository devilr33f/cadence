// Package images is the server's image proxy: posters, artwork, avatars and
// thumbnails the catalog and cards name reach a device only as an id the
// proxy issued (cards' img:<id>, the catalog's artwork hash), never as a
// third-party URL. An id is an HMAC of its URL under a key of this process,
// so a device can fetch only what the server handed out; ids, URLs and
// images live in memory alone (D11). A public URL is fetched under media's
// cover guard (https on 443, checked addresses, the environment's proxy, a
// deadline and a size cap); a provider's own LAN URL (torrplayer's signed
// /image, a TorrServer poster) with a plain client, the same cap and no
// redirect. A screen's decoder takes png and jpeg alone, so a webp or a gif
// is converted for a caller whose Accept header does not take it (pure Go,
// kept beside the original). URLs and ids never reach a log.
package images

import (
	"container/list"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	maxBytes     = 5 << 20
	cacheBytes   = 96 << 20
	maxEntries   = 50_000
	localTimeout = 5 * time.Second
	failFor      = 30 * time.Second
)

var types = []string{"image/jpeg", "image/png", "image/webp", "image/gif"}

// IDPattern is an id's shape, the part after cards' "img:"
var IDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{24}$`)

var (
	// ErrUnknown is an id this process did not issue
	ErrUnknown = errors.New("images: unknown id")
	// ErrUnavailable is an image that could not be fetched now
	ErrUnavailable = errors.New("images: unavailable")
)

// Public fetches a hostile https url (media.Artwork.Image).
type Public func(rawURL string) (contentType string, data []byte, err error)

// Config configures a Proxy.
type Config struct {
	Public Public
	// Local fetches providers' own urls; nil is a plain client without
	// redirects
	Local *http.Client
	Now   func() time.Time
}

type entry struct {
	url   string
	local bool
	// load reads an image the server holds itself (RefLoaded); nil for a url
	load func() (string, []byte, error)
	el   *list.Element
}

type cached struct {
	id          string
	contentType string
	data        []byte
}

// Proxy issues ids and serves their images.
type Proxy struct {
	key    []byte
	public Public
	local  *http.Client
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]*entry
	order   *list.List
	cache   map[string]*list.Element
	lru     *list.List
	size    int
	failed  map[string]time.Time
	flight  singleflight.Group
	// slots bounds the conversions decoding at once (a decoded poster is
	// its pixels in memory)
	slots chan struct{}
}

// New makes a proxy with a fresh key: ids end with the process.
func New(c Config) *Proxy {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	if c.Local == nil {
		c.Local = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &Proxy{key: key, public: c.Public, local: c.Local, now: c.Now, entries: map[string]*entry{},
		order: list.New(), cache: map[string]*list.Element{}, lru: list.New(), failed: map[string]time.Time{},
		slots: make(chan struct{}, convertSlots)}
}

// Ref issues the id of rawURL ("" for no usable url); local marks a url of a
// configured provider's own host.
func (p *Proxy) Ref(rawURL string, local bool) string {
	u, err := url.Parse(rawURL)
	if rawURL == "" || err != nil || (u.Scheme != "https" && (!local || u.Scheme != "http")) || u.Host == "" || u.User != nil {
		return ""
	}
	mac := hmac.New(sha256.New, p.key)
	if local {
		mac.Write([]byte{1})
	} else {
		mac.Write([]byte{0})
	}
	mac.Write([]byte(rawURL))
	id := base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:18])
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.entries[id]; ok {
		p.order.MoveToBack(e.el)
		return id
	}
	p.add(id, &entry{url: rawURL, local: local})
	return id
}

// RefLoaded issues the id of an image the server holds itself, named by key (a device's uploaded cover: its device
// and hash), which load reads when the id is fetched; "" for an empty key. key never leaves the process.
func (p *Proxy) RefLoaded(key string, load func() (string, []byte, error)) string {
	if key == "" || load == nil {
		return ""
	}
	mac := hmac.New(sha256.New, p.key)
	mac.Write([]byte{2})
	mac.Write([]byte(key))
	id := base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:18])
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.entries[id]; ok {
		p.order.MoveToBack(e.el)
		return id
	}
	p.add(id, &entry{load: load})
	return id
}

// add issues an id; callers hold mu
func (p *Proxy) add(id string, e *entry) {
	e.el = p.order.PushBack(id)
	p.entries[id] = e
	for p.order.Len() > maxEntries {
		old := p.order.Remove(p.order.Front()).(string)
		delete(p.entries, old)
	}
}

// Get answers an issued id's image as it was fetched.
func (p *Proxy) Get(_ context.Context, id string) (string, []byte, error) {
	p.mu.Lock()
	e, ok := p.entries[id]
	p.mu.Unlock()
	if !ok {
		return "", nil, ErrUnknown
	}
	return p.load(id, func() (string, []byte, error) {
		if e.load != nil {
			return e.load()
		}
		if e.local {
			return p.fetchLocal(e.url)
		}
		if p.public != nil {
			return p.public(e.url)
		}
		return "", nil, ErrUnavailable
	})
}

// GetAccepting answers an issued id's image in a type accept (an Accept
// header) takes: as fetched when it does, else a webp or a gif's first frame
// converted to png (when it has transparency and png is taken) or jpeg.
// ErrNotAcceptable when no type fits.
func (p *Proxy) GetAccepting(ctx context.Context, id, accept string) (string, []byte, error) {
	typ, data, err := p.Get(ctx, id)
	if err != nil {
		return "", nil, err
	}
	a := parseAccept(accept)
	if a.takes(typ) {
		return typ, data, nil
	}
	key, ok := a.convertKey(id)
	if !convertible(typ) || !ok {
		return "", nil, ErrNotAcceptable
	}
	return p.load(key, func() (string, []byte, error) {
		p.slots <- struct{}{}
		defer func() { <-p.slots }()
		return convert(typ, data, a)
	})
}

// load answers key's cached image, else makes it once (callers of one key
// share the work); a failure is remembered for failFor
func (p *Proxy) load(key string, build func() (string, []byte, error)) (string, []byte, error) {
	p.mu.Lock()
	var hit *cached
	if el, in := p.cache[key]; in {
		p.lru.MoveToFront(el)
		hit = el.Value.(*cached)
	}
	until, failed := p.failed[key]
	p.mu.Unlock()
	switch {
	case hit != nil:
		return hit.contentType, hit.data, nil
	case failed && p.now().Before(until):
		return "", nil, ErrUnavailable
	}
	v, err, _ := p.flight.Do(key, func() (any, error) {
		var c cached
		var err error
		c.contentType, c.data, err = build()
		p.mu.Lock()
		defer p.mu.Unlock()
		if errors.Is(err, ErrNotAcceptable) {
			return nil, err
		}
		if err != nil {
			p.failed[key] = p.now().Add(failFor)
			return nil, ErrUnavailable
		}
		delete(p.failed, key)
		c.id = key
		p.put(&c)
		return &c, nil
	})
	if err != nil {
		return "", nil, err
	}
	c := v.(*cached)
	return c.contentType, c.data, nil
}

func (p *Proxy) put(c *cached) {
	if el, ok := p.cache[c.id]; ok {
		p.size -= len(el.Value.(*cached).data)
		p.lru.Remove(el)
	}
	p.cache[c.id] = p.lru.PushFront(c)
	p.size += len(c.data)
	for p.size > cacheBytes && p.lru.Len() > 0 {
		old := p.lru.Remove(p.lru.Back()).(*cached)
		delete(p.cache, old.id)
		p.size -= len(old.data)
	}
	for k, t := range p.failed {
		if p.now().After(t) {
			delete(p.failed, k)
		}
	}
}

// fetchLocal reads a provider's own image on its own deadline: other callers
// may wait on it
func (p *Proxy) fetchLocal(rawURL string) (string, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), localTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil, err
	}
	resp, err := p.local.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	typ, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	enc := resp.Header.Get("Content-Encoding")
	if resp.StatusCode != http.StatusOK || err != nil || !slices.Contains(types, typ) || (enc != "" && enc != "identity") ||
		resp.ContentLength > maxBytes {
		return "", nil, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil || len(data) > maxBytes {
		return "", nil, ErrUnavailable
	}
	return typ, data, nil
}
