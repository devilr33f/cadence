package media

import (
	"container/list"
	"crypto/sha256"
	"sync"
	"time"
)

const (
	artCacheEntries = 32
	artCacheBytes   = 32 << 20
)

// artKey is an artwork url's SHA-256, its artworkHash.
type artKey = [sha256.Size]byte

type artImage struct {
	contentType string
	data        []byte
}

type artEntry struct {
	key artKey
	img artImage
}

// artCache keeps images by their url's SHA-256 in memory only (D11), the least
// recently used dropped first once it holds more images or bytes than allowed.
type artCache struct {
	maxEntries, maxBytes int

	mu    sync.Mutex
	bytes int
	order *list.List // of *artEntry, the most recently used first
	byKey map[artKey]*list.Element
}

func newArtCache() *artCache { return newArtCacheOf(artCacheEntries, artCacheBytes) }

func newArtCacheOf(entries, bytes int) *artCache {
	return &artCache{maxEntries: entries, maxBytes: bytes, order: list.New(), byKey: map[artKey]*list.Element{}}
}

func (c *artCache) get(k artKey) (artImage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byKey[k]
	if !ok {
		return artImage{}, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*artEntry).img, true
}

func (c *artCache) put(k artKey, img artImage) {
	if len(img.data) > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.byKey[k]; ok {
		e := el.Value.(*artEntry)
		c.bytes += len(img.data) - len(e.img.data)
		e.img = img
		c.order.MoveToFront(el)
	} else {
		c.byKey[k] = c.order.PushFront(&artEntry{key: k, img: img})
		c.bytes += len(img.data)
	}
	for c.order.Len() > c.maxEntries || c.bytes > c.maxBytes {
		e := c.order.Remove(c.order.Back()).(*artEntry)
		delete(c.byKey, e.key)
		c.bytes -= len(e.img.data)
	}
}

const (
	artFailFor     = 30 * time.Second
	artFailEntries = 64
)

type artFailure struct {
	err   error
	until time.Time
}

// failMemo keeps a failed fetch's answer for a while by its url's SHA-256, so a
// host that hangs or refuses is not asked again on every view.
type failMemo struct {
	mu    sync.Mutex
	until map[artKey]artFailure
}

func newFailMemo() *failMemo { return &failMemo{until: map[artKey]artFailure{}} }

// get is the failure kept for k, nil for none.
func (m *failMemo) get(k artKey, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.until[k]; ok && now.Before(f.until) {
		return f.err
	}
	return nil
}

// put drops the expired failures, then the earliest to expire while full.
func (m *failMemo) put(k artKey, err error, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, f := range m.until {
		if !now.Before(f.until) {
			delete(m.until, key)
		}
	}
	if _, ok := m.until[k]; !ok && len(m.until) >= artFailEntries {
		var first artKey
		var at time.Time
		for key, f := range m.until {
			if at.IsZero() || f.until.Before(at) {
				first, at = key, f.until
			}
		}
		delete(m.until, first)
	}
	m.until[k] = artFailure{err: err, until: now.Add(artFailFor)}
}
