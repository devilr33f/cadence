package catalog_test

import (
	"context"
	"sync"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
)

type fakeInfo struct {
	mu     sync.Mutex
	reads  int
	warmed []catalog.ExternalIDs
}

var frierenInfo = catalog.Info{Score: 9.1, Rank: 1, Popularity: 160, Status: "released", AiredOn: "2023-09-29",
	ReleasedOn: "2024-03-22", Studios: []string{"Madhouse"}, Genres: []string{"приключения", "фэнтези"}, EpisodesTotal: 28,
	EpisodesAired: 28, Description: "Описание с Шикимори.",
	Related: []catalog.RelatedTitle{{Relation: "Продолжение", Title: "Фрирен 2", Kind: "tv", Year: 2026, Shikimori: "59978"}}}

func (f *fakeInfo) Info(_ context.Context, ids catalog.ExternalIDs) (catalog.Info, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return frierenInfo, ids.Shikimori == "52991"
}

func (f *fakeInfo) Cached(ids catalog.ExternalIDs) (catalog.Info, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return frierenInfo, ids.Shikimori == "52991" && f.reads > 0
}

func (f *fakeInfo) Warm(ids catalog.ExternalIDs) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.warmed = append(f.warmed, ids)
}

// a title page carries Shikimori's and MyAnimeList's facts over the source's (the Russian description replaces
// its own), its related titles and the owner's own list entry, which the provider reads from torrplayer's linked
// list
func TestATitlePageIsEnriched(t *testing.T) {
	info := &fakeInfo{}
	r := newRig(t, func(d *catalog.Deps) { d.Info = info })
	d, err := r.svc.Item(context.Background(), catalog.Household, groupRef(t, r), "", "")
	if err != nil {
		t.Fatal(err)
	}
	s := d.Item.Show
	if s == nil || s.Rank != 1 || s.Popularity != 160 || s.Score != 9.1 || s.AiredOn != "2023-09-29" || s.Studios[0] != "Madhouse" ||
		s.Description != "Описание с Шикимори." || len(s.Related) != 1 || s.Related[0].Title != "Фрирен 2" {
		t.Fatalf("show %+v", s)
	}
	if s.Mine == nil || *s.Mine != (catalog.UserRate{Status: "watching", Score: 9, Watched: 5}) {
		t.Fatalf("mine %+v", s.Mine)
	}
	// the item the service keeps is not changed under it
	if it, ok := r.svc.Peek(context.Background(), catalog.Household, d.Item.Ref); ok && it.Show != nil && it.Show.Rank != 0 {
		t.Fatal("the kept item was enriched in place")
	}
	// Home's items take what the cache holds, and warm their facts
	p, err := r.svc.Browse(context.Background(), catalog.Household, "", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	enriched := false
	for _, sec := range p.Sections {
		for _, it := range sec.Items {
			if it.Show != nil && it.Show.Rank == 1 {
				enriched = true
			}
		}
	}
	info.mu.Lock()
	warmed := len(info.warmed)
	info.mu.Unlock()
	if !enriched || warmed == 0 {
		t.Fatalf("home: enriched %v, warmed %d", enriched, warmed)
	}
}

// the same title on its other sources: by its Shikimori id across the provider's sources (each with its
// voiceovers and best quality), the source it plays from marked; another title of a like name is not one
func TestAlternativesFindTheSameTitleElsewhere(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	d, err := r.svc.Item(ctx, catalog.Household, groupRef(t, r), "", "")
	if err != nil || len(d.Variants) != 2 {
		t.Fatalf("detail %+v %v", d, err)
	}
	// from the anime365 variant alone, as a page opened from Home is
	var from catalog.Ref
	for _, v := range d.Variants {
		if v.Source == "anime365" {
			from = v.Ref
		}
	}
	alts, err := r.svc.Alternatives(ctx, catalog.Household, from)
	if err != nil {
		t.Fatal(err)
	}
	bySource := map[catalog.SourceID]catalog.AlternativeView{}
	for _, a := range alts.Items {
		bySource[a.Source] = a
	}
	al, cur := bySource["aniliberty"], bySource["anime365"]
	if len(alts.Items) != 2 || al.Match != "shikimori" || al.Current || !cur.Current || al.Ref == "" || al.SourceName != "AniLiberty" {
		t.Fatalf("alternatives %+v", alts.Items)
	}
	if al.Quality != 1080 || len(al.Voiceovers) < 2 || al.Voiceovers[0].Label != "AniLibria" || al.Voiceovers[0].Quality != 1080 {
		t.Fatalf("offer %+v", al)
	}
	if alts.Asked == 0 || alts.Failed == 0 {
		t.Fatalf("asked %d failed %d (animego fails)", alts.Asked, alts.Failed)
	}
}

func TestSameTitleByIdThenName(t *testing.T) {
	show := func(shiki, en string) *catalog.ShowInfo {
		return &catalog.ShowInfo{IDs: catalog.ExternalIDs{Shikimori: shiki}, Names: catalog.Names{EN: en}}
	}
	ids := catalog.ExternalIDs{Shikimori: "52299"}
	names := []string{"Поднятие уровня в одиночку", "Solo Leveling"}
	for _, c := range []struct {
		it   catalog.Item
		want string
	}{
		{catalog.Item{Title: "x", Show: show("52299", "")}, "shikimori"},
		{catalog.Item{Title: "Solo Leveling", Show: show("58567", "Solo Leveling")}, ""},
		{catalog.Item{Title: "[Erai] Solo Leveling - 01-12 [1080p]"}, "name"},
		{catalog.Item{Title: "Поднятие уровня в одиночку"}, "name"},
		{catalog.Item{Title: "Solo"}, ""},
	} {
		if got := catalog.SameTitle(ids, names, c.it); got != c.want {
			t.Errorf("%q: %q, want %q", c.it.Title, got, c.want)
		}
	}
}

// an episode's ref names its series as the catalog last handed it out: the cards and the player name the show, not
// the episode's number
func TestAnEpisodeKnowsItsSeries(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	g, err := r.svc.Item(ctx, catalog.Household, groupRef(t, r), "", "")
	if err != nil || len(g.Variants) == 0 {
		t.Fatalf("group: %v %d", err, len(g.Variants))
	}
	series, err := r.svc.Item(ctx, catalog.Household, g.Variants[0].Ref, "", "")
	if err != nil || len(series.Children) == 0 {
		t.Fatalf("series: %v %d", err, len(series.Children))
	}
	ep := series.Children[0]
	if ep.Episode == nil {
		t.Fatalf("child %+v", ep)
	}
	got, ok := r.svc.Series(ctx, catalog.Household, ep.Ref)
	if !ok || got.Title != series.Item.Title || got.Title == "" {
		t.Fatalf("series of the episode: %v %q, want %q", ok, got.Title, series.Item.Title)
	}
	if _, ok := r.svc.Series(ctx, catalog.Household, series.Item.Ref); ok {
		t.Fatal("a series has a series")
	}
}
