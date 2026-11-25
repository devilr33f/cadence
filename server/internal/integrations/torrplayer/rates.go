package torrplayer

import (
	"context"
	"strconv"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
)

// the owner's own list entries, from the Shikimori list torrplayer keeps linked (its /mylist): torrplayer holds the
// account and its tokens, the server only reads the entries

const (
	// the whole list is read again at most this often
	ratesTTL = 2 * time.Minute
	// a failed read (no list linked, torrplayer away) is not retried before this
	ratesRetry = 30 * time.Second
)

type rateCache struct {
	mu      sync.Mutex
	at      time.Time
	ok      bool
	byShiki map[string]catalog.UserRate
}

// Rate is the owner's entry of a title by its Shikimori id, false when the list has none or none is linked.
func (p *Provider) Rate(ctx context.Context, ids catalog.ExternalIDs) (catalog.UserRate, bool) {
	if ids.Shikimori == "" {
		return catalog.UserRate{}, false
	}
	c := &p.rates
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	ttl := ratesTTL
	if !c.ok {
		ttl = ratesRetry
	}
	if c.at.IsZero() || now.Sub(c.at) > ttl {
		c.at = now
		out, err := p.c.myList(ctx, "all", 0)
		c.ok = err == nil
		if c.ok {
			c.byShiki = make(map[string]catalog.UserRate, len(out.Items))
			for _, it := range out.Items {
				c.byShiki[strconv.Itoa(it.ShikimoriID)] = catalog.UserRate{Status: it.Status, Score: it.Score, Watched: it.Watched}
			}
		}
	}
	r, ok := c.byShiki[ids.Shikimori]
	return r, ok
}
