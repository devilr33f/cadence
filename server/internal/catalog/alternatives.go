package catalog

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

// alternatives: the same title on every other provider and source, found by its Shikimori or MyAnimeList id
// first, else by its names, each with what it offers (voiceovers, the best quality, the episodes it has), so the
// owner can move to another source from a title's page or by voice.

const (
	// the most alternatives listed, and read for their offers
	maxAlternatives = 12
	// each search and each offer's read
	altWait = 12 * time.Second
	// the names searched: the Russian, the English, the source's own
	maxAltQueries = 3
)

// AlternativeView is one other place a title plays.
type AlternativeView struct {
	Ref        Ref        `json:"ref"`
	Provider   ProviderID `json:"provider"`
	Source     SourceID   `json:"source,omitempty"`
	SourceName string     `json:"sourceName"`
	Title      string     `json:"title"`
	// Match says how it was found: shikimori, mal or name
	Match         string `json:"match" enum:"shikimori,mal,name"`
	EpisodesAired int    `json:"episodesAired,omitempty"`
	EpisodesTotal int    `json:"episodesTotal,omitempty"`
	// Voiceovers are its dubs and subtitles for the title's first episode, each with its best quality
	Voiceovers []AltVoiceover `json:"voiceovers" nullable:"false"`
	// Quality is the best of them, 0 when unknown
	Quality int `json:"quality,omitempty"`
	// Current marks the source the title plays from now
	Current     bool `json:"current,omitempty"`
	Unavailable bool `json:"unavailable,omitempty"`
}

// AltVoiceover is a voiceover an alternative offers.
type AltVoiceover struct {
	Label   string    `json:"label"`
	Kind    TransType `json:"kind,omitempty"`
	Quality int       `json:"quality,omitempty"`
}

// AlternativesView is a title's alternatives, the ones found by an id first.
type AlternativesView struct {
	Items []AlternativeView `json:"items" nullable:"false"`
	// Asked and Failed count the providers and sources searched
	Asked  int `json:"asked"`
	Failed int `json:"failed"`
}

type found struct {
	p     Provider
	it    Item
	match string
}

// Alternatives finds a title's other sources.
func (s *Service) Alternatives(ctx context.Context, household string, r Ref) (AlternativesView, error) {
	t, err := s.resolveRef(ctx, household, r, "")
	if err != nil {
		return AlternativesView{}, err
	}
	base, err := s.baseItem(ctx, household, t)
	if err != nil {
		return AlternativesView{}, err
	}
	out := AlternativesView{Items: []AlternativeView{}}
	var ids ExternalIDs
	if base.Show != nil {
		ids = base.Show.IDs
	}
	names := altNames(base)
	if len(names) == 0 {
		return out, nil
	}
	var mu sync.Mutex
	seen := map[string]bool{}
	var hits []found
	add := func(p Provider, it Item, match string) {
		mu.Lock()
		defer mu.Unlock()
		k := it.Key.String()
		if seen[k] {
			return
		}
		seen[k] = true
		hits = append(hits, found{p: p, it: it, match: match})
	}
	// the title's own sources first: a title group's variants, or the item itself
	if t.group != nil {
		for _, v := range s.variants(t.group) {
			add(t.p, v, matchOf(ids, v))
		}
	} else {
		add(t.p, base, matchOf(ids, base))
	}
	var wg sync.WaitGroup
	for _, c := range s.d.Providers(ctx) {
		p := c.Provider
		if !p.Capabilities().Has(CapSearch) {
			continue
		}
		for _, q := range names {
			wg.Go(func() {
				sctx, cancel := context.WithTimeout(ctx, altWait)
				defer cancel()
				_ = p.Search(sctx, SearchQuery{Query: q, Limit: 20}, func(b SearchBatch) error {
					mu.Lock()
					out.Asked++
					if b.Failure != nil {
						out.Failed++
					}
					mu.Unlock()
					for _, it := range b.Items {
						if m := sameTitle(ids, names, it); m != "" {
							add(p, it, m)
						}
					}
					return nil
				})
			})
		}
	}
	wg.Wait()
	slices.SortStableFunc(hits, func(a, b found) int { return matchRank(a.match) - matchRank(b.match) })
	if len(hits) > maxAlternatives {
		hits = hits[:maxAlternatives]
	}
	views := make([]AlternativeView, len(hits))
	names2 := s.sourceNames(ctx)
	var ow sync.WaitGroup
	for i, h := range hits {
		o, err := s.out(ctx, household, h.p, h.it)
		if err != nil {
			continue
		}
		v := AlternativeView{Ref: o.Ref, Provider: h.p.ID(), Source: h.it.Key.Source, Title: o.Title, Match: h.match,
			Current: h.it.Key == t.variant, Unavailable: h.it.Unavailable, Voiceovers: []AltVoiceover{}}
		v.SourceName = names2[sourceKey{h.p.ID(), h.it.Key.Source}]
		if v.SourceName == "" {
			v.SourceName = string(h.it.Key.Source)
			if v.SourceName == "" {
				v.SourceName = string(h.p.ID())
			}
		}
		if h.it.Show != nil {
			v.EpisodesAired, v.EpisodesTotal = h.it.Show.EpisodesAired, h.it.Show.EpisodesTotal
		}
		views[i] = v
		if h.it.Unavailable || !h.p.Capabilities().Has(CapOptions) {
			continue
		}
		ow.Go(func() {
			octx, cancel := context.WithTimeout(ctx, altWait)
			defer cancel()
			o, err := h.p.Options(octx, OptionsRequest{Key: h.it.Key})
			if err != nil {
				return
			}
			offerOf(&views[i], o)
		})
	}
	ow.Wait()
	for _, v := range views {
		if v.Ref != "" {
			out.Items = append(out.Items, v)
		}
	}
	return out, nil
}

// baseItem is the title a ref names, as its page shows it
func (s *Service) baseItem(ctx context.Context, household string, t target) (Item, error) {
	if t.group != nil {
		return s.groupItem(t.group), nil
	}
	if it, ok := s.kept(household, t.key); ok && it.Show != nil {
		return it, nil
	}
	if err := s.need(t.p, CapItem); err != nil {
		return Item{}, err
	}
	ictx, cancel := context.WithTimeout(ctx, altWait)
	defer cancel()
	d, err := t.p.Item(ictx, ItemRequest{Key: t.variant, Limit: 1})
	if err != nil {
		return Item{}, err
	}
	return d.Item, nil
}

func (s *Service) sourceNames(ctx context.Context) map[sourceKey]string {
	out := map[sourceKey]string{}
	for _, c := range s.d.Providers(ctx) {
		sctx, cancel := context.WithTimeout(ctx, sourceWait)
		srcs, _ := c.Provider.Sources(sctx)
		cancel()
		for _, src := range srcs {
			out[sourceKey{c.Provider.ID(), src.ID}] = src.Name
		}
	}
	return out
}

// offerOf reads an alternative's voiceovers and qualities from its options
func offerOf(v *AlternativeView, o Options) {
	for _, g := range o.Groups {
		for _, ch := range g.Choices {
			if ch.Unavailable {
				continue
			}
			switch {
			case g.Kind == GroupVoiceover:
				av := AltVoiceover{Label: ch.Label}
				if ch.Stream != nil {
					av.Kind, av.Quality = ch.Stream.TransType, ch.Stream.Quality
				}
				if len(v.Voiceovers) < 8 {
					v.Voiceovers = append(v.Voiceovers, av)
				}
				v.Quality = max(v.Quality, av.Quality)
			case g.Kind == GroupTorrent && ch.Torrent != nil:
				v.Quality = max(v.Quality, ch.Torrent.Quality)
			case g.Kind == GroupQuality:
				if q := atoiPrefix(ch.ID); q > 0 {
					v.Quality = max(v.Quality, q)
				}
			}
		}
	}
}

func atoiPrefix(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// altNames are the names a title is searched by, distinct, its Russian one first
func altNames(it Item) []string {
	var out []string
	add := func(n string) {
		if len(out) == maxAltQueries || nameKey(n) == "" {
			return
		}
		for _, o := range out {
			if nameKey(o) == nameKey(n) {
				return
			}
		}
		out = append(out, strings.TrimSpace(n))
	}
	if it.Show != nil {
		add(it.Show.Names.RU)
		add(it.Show.Names.EN)
	}
	add(it.Title)
	return out
}

// sameTitle says how a found item is the title: by an id the two share, else, when either has no id, by a name
// alike; "" when it is another title
func sameTitle(ids ExternalIDs, names []string, it Item) string {
	if m := matchOf(ids, it); m != "name" {
		return m
	}
	var its ExternalIDs
	if it.Show != nil {
		its = it.Show.IDs
	}
	// two known ids that differ are two titles, whatever their names
	if (ids.Shikimori != "" && its.Shikimori != "") || (ids.MAL != "" && its.MAL != "") {
		return ""
	}
	cand := []string{it.Title}
	if it.Show != nil {
		cand = append(cand, it.Show.Names.RU, it.Show.Names.EN)
		cand = append(cand, it.Show.Names.Alt...)
	}
	for _, n := range names {
		for _, c := range cand {
			if alike(n, c) {
				return "name"
			}
		}
	}
	return ""
}

func matchOf(ids ExternalIDs, it Item) string {
	if it.Show != nil {
		if ids.Shikimori != "" && it.Show.IDs.Shikimori == ids.Shikimori {
			return "shikimori"
		}
		if ids.MAL != "" && it.Show.IDs.MAL == ids.MAL {
			return "mal"
		}
	}
	return "name"
}

func matchRank(m string) int {
	switch m {
	case "shikimori":
		return 0
	case "mal":
		return 1
	}
	return 2
}

// alike is one name, case and punctuation aside, or a torrent's name that holds the title's whole (at least 6
// letters: "Solo Leveling [1080p]")
func alike(title, name string) bool {
	a, b := nameKey(title), nameKey(name)
	if a == "" || b == "" {
		return false
	}
	return a == b || (len([]rune(a)) >= 6 && strings.Contains(b, a))
}

func nameKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.ReplaceAll(s, "ё", "е")) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
