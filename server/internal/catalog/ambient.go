package catalog

import (
	"context"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/ambient"
)

// backdropTTL is how long Home's artwork is reused for backdrops
const backdropTTL = 10 * time.Minute

// Backdrops is ambient's artwork: the posters of what Home shows (continue
// watching, the owner's lists, the torrent library), as the image proxy's
// urls, without captions (titles are third-party and the backdrop needs
// none). It reads Home at most every ten minutes.
type Backdrops struct {
	s *Service

	mu   sync.Mutex
	urls []string
	at   time.Time
}

var _ ambient.Artwork = (*Backdrops)(nil)

// Backdrops answers the service as ambient's Artwork.
func (s *Service) Backdrops() *Backdrops { return &Backdrops{s: s} }

func (b *Backdrops) Backdrops(ctx context.Context, _ string, limit int) ([]ambient.Image, error) {
	if b.s.d.ImageURL == nil {
		return nil, nil
	}
	b.mu.Lock()
	fresh := b.at.After(b.s.d.Now().Add(-backdropTTL))
	urls := b.urls
	b.mu.Unlock()
	if !fresh {
		pg, err := b.s.Browse(ctx, Household, "", "", nil, 0)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		urls = nil
		for _, sec := range pg.Sections {
			for _, it := range sec.Items {
				if h := it.Artwork.Hash; h != "" && !seen[h] {
					seen[h] = true
					urls = append(urls, b.s.d.ImageURL(h))
				}
			}
		}
		b.mu.Lock()
		b.urls, b.at = urls, b.s.d.Now()
		b.mu.Unlock()
	}
	if limit > 0 && len(urls) > limit {
		urls = urls[:limit]
	}
	out := make([]ambient.Image, 0, len(urls))
	for _, u := range urls {
		out = append(out, ambient.Image{URL: u})
	}
	return out, nil
}
