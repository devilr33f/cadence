package openmeteo

import (
	"sync"
	"time"
)

const (
	cacheTTL = 24 * time.Hour
	cacheMax = 256
)

type entry struct {
	p       Place
	expires time.Time
}

// geoCache is bounded: when full it drops expired entries, then the soonest to expire.
type geoCache struct {
	mu sync.Mutex
	m  map[string]entry
}

func newGeoCache() *geoCache { return &geoCache{m: map[string]entry{}} }

func (c *geoCache) get(key string, now time.Time) (Place, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || !now.Before(e.expires) {
		delete(c.m, key)
		return Place{}, false
	}
	return e.p, true
}

func (c *geoCache) put(key string, p Place, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[key]; !ok && len(c.m) >= cacheMax {
		var oldest string
		var oldestAt time.Time
		for k, e := range c.m {
			if !now.Before(e.expires) {
				delete(c.m, k)
			} else if oldest == "" || e.expires.Before(oldestAt) {
				oldest, oldestAt = k, e.expires
			}
		}
		if len(c.m) >= cacheMax {
			delete(c.m, oldest)
		}
	}
	c.m[key] = entry{p: p, expires: now.Add(cacheTTL)}
}

func (c *geoCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}
