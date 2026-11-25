package catalog

import (
	"context"
	"time"
)

// TitleInfo answers a title's public facts by its Shikimori and MyAnimeList ids (titleinfo.Service: Shikimori's
// Russian text, MyAnimeList's rank), cached server-side: titles change rarely.
type TitleInfo interface {
	// Info reads them, from the cache when it holds them; ok false when nothing is known
	Info(ctx context.Context, ids ExternalIDs) (Info, bool)
	// Cached answers from the cache alone
	Cached(ids ExternalIDs) (Info, bool)
	// Warm reads them in the background, at the pace the sources allow
	Warm(ids ExternalIDs)
}

// Info is a title's facts from Shikimori and MyAnimeList. Every text is third-party.
type Info struct {
	Score         float64
	Rank          int
	Popularity    int
	Status        string // announced, ongoing, released
	AiredOn       string
	ReleasedOn    string
	Studios       []string
	Genres        []string
	EpisodesTotal int
	EpisodesAired int
	// Description is Shikimori's Russian one, its markup dropped
	Description string
	Related     []RelatedTitle
}

// UserRates is a provider that sees the owner's linked anime list (torrplayer's Shikimori list): the entry of a
// title, false when the list has none or is not linked.
type UserRates interface {
	Rate(ctx context.Context, ids ExternalIDs) (UserRate, bool)
}

const (
	// a title page waits this long for facts it has not read before
	infoWait = 4 * time.Second
	// Home warms the facts of this many of a section's first items, for the hero
	warmItems = 6
)

// enrich lays a title's facts over its item on a title page: the provider's text stays where the facts have none,
// Shikimori's Russian description replaces the source's. fetch false reads the cache alone.
func (s *Service) enrich(ctx context.Context, p Provider, it *Item, fetch bool) {
	if it.Show == nil || (it.Show.IDs.Shikimori == "" && it.Show.IDs.MAL == "") {
		return
	}
	show := *it.Show
	if s.d.Info != nil {
		var info Info
		var ok bool
		if fetch {
			ictx, cancel := context.WithTimeout(ctx, infoWait)
			info, ok = s.d.Info.Info(ictx, show.IDs)
			cancel()
		} else {
			info, ok = s.d.Info.Cached(show.IDs)
		}
		if ok {
			applyInfo(&show, info)
		}
	}
	if r, ok := p.(UserRates); ok && fetch {
		rctx, cancel := context.WithTimeout(ctx, infoWait)
		if rate, ok := r.Rate(rctx, show.IDs); ok {
			show.Mine = &rate
		}
		cancel()
	}
	it.Show = &show
}

func applyInfo(show *ShowInfo, info Info) {
	if info.Score > 0 {
		show.Score = info.Score
	}
	show.Rank, show.Popularity = info.Rank, info.Popularity
	if info.Status != "" {
		show.Status = info.Status
	}
	show.AiredOn, show.ReleasedOn = info.AiredOn, info.ReleasedOn
	if len(info.Studios) > 0 {
		show.Studios = info.Studios
	}
	if len(info.Genres) > 0 {
		show.Genres = info.Genres
	}
	show.EpisodesTotal = max(show.EpisodesTotal, info.EpisodesTotal)
	show.EpisodesAired = max(show.EpisodesAired, info.EpisodesAired)
	if info.Description != "" {
		show.Description = info.Description
	}
	if show.Year == 0 && len(info.AiredOn) >= 4 {
		for _, c := range info.AiredOn[:4] {
			show.Year = show.Year*10 + int(c-'0')
		}
	}
	show.Related = info.Related
}

// enrichCached lays what the cache holds over a page's items, and warms the first ones' facts for the next read
func (s *Service) enrichCached(p Provider, items []Item) {
	if s.d.Info == nil {
		return
	}
	for i := range items {
		s.enrich(context.Background(), p, &items[i], false)
		if i < warmItems && items[i].Show != nil {
			s.d.Info.Warm(items[i].Show.IDs)
		}
	}
}
