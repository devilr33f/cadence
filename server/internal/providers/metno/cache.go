package metno

import (
	"sync"
	"time"
)

const forecastCacheMax = 64

type cachedForecast struct {
	body         []byte
	expires      time.Time
	lastModified string
}

type forecastCache struct {
	mu sync.Mutex
	m  map[string]cachedForecast
}

func newForecastCache() *forecastCache { return &forecastCache{m: map[string]cachedForecast{}} }

func (c *forecastCache) fresh(key string, now time.Time) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || e.expires.IsZero() || !now.Before(e.expires) {
		return nil, false
	}
	return e.body, true
}

func (c *forecastCache) entry(key string) (cachedForecast, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	return e, ok
}

// put is bounded: when full it drops expired entries, then the soonest to expire.
func (c *forecastCache) put(key string, entry cachedForecast) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[key]; !ok && len(c.m) >= forecastCacheMax {
		now := time.Now()
		oldest := ""
		var oldestAt time.Time
		for k, e := range c.m {
			if !e.expires.IsZero() && !now.Before(e.expires) {
				delete(c.m, k)
			} else if oldest == "" || e.expires.Before(oldestAt) {
				oldest, oldestAt = k, e.expires
			}
		}
		if len(c.m) >= forecastCacheMax {
			delete(c.m, oldest)
		}
	}
	c.m[key] = entry
}
