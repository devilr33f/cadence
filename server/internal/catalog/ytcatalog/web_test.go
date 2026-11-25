package ytcatalog_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/catalog/ytcatalog"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytweb"
)

const chID = "UC7_YxT-KID8kRbqZo7MyscQ"

var now = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

// web is a fake YouTube: what each call was asked and what it answers
type web struct {
	searched  []ytweb.SearchOptions
	more      []string
	tab       ytweb.Tab
	sort      ytweb.Sort
	query     string
	failed    error
	playlists map[string][]ytweb.Item // a playlist's rows, 100 a page
	exact     map[string]providers.Stats
	statsAsk  []string
}

func video(id string, ago time.Duration, views int64) ytweb.Item {
	return ytweb.Item{Kind: ytweb.KindVideo, ID: id, Title: "video " + id, Channel: "Markiplier", ChannelID: chID,
		DurationS: 600, Stats: providers.Stats{Views: views, Published: now.Add(-ago), PublishedApprox: true}}
}

func (w *web) Search(_ context.Context, o ytweb.SearchOptions) (ytweb.Page, error) {
	w.searched = append(w.searched, o)
	if w.failed != nil {
		return ytweb.Page{}, w.failed
	}
	return ytweb.Page{Items: []ytweb.Item{
		{Kind: ytweb.KindChannel, ID: chID, Title: "Markiplier", Handle: "@markiplier", Stats: providers.Stats{Subscribers: 39000000}},
		video("iOztnsBPrAA", 12*365*24*time.Hour, 131220260),
		{Kind: ytweb.KindPlaylist, ID: "PLfnaf", Title: "Five Nights at Freddy's", Channel: "Markiplier", ChannelID: chID,
			Stats: providers.Stats{VideoCount: 165}},
	}, Next: "page2"}, nil
}

func (w *web) More(_ context.Context, next string) (ytweb.Page, error) {
	w.more = append(w.more, next)
	var id string
	var page int
	if _, err := fmt.Sscanf(next, "%s %d", &id, &page); err == nil {
		rows := w.playlists[id]
		end := min(len(rows), (page+1)*100)
		p := ytweb.Page{Items: rows[page*100 : end]}
		if end < len(rows) {
			p.Next = fmt.Sprintf("%s %d", id, page+1)
		}
		return p, nil
	}
	return ytweb.Page{Items: []ytweb.Item{video("k9jfDVoM1GU", time.Hour, 5)}}, nil
}

func (w *web) ChannelTab(_ context.Context, id string, tab ytweb.Tab, sort ytweb.Sort) (ytweb.ChannelPage, error) {
	w.tab, w.sort = tab, sort
	got := sort
	if got == "" {
		got = ytweb.SortLatest
	}
	return ytweb.ChannelPage{Channel: ytweb.Channel{ID: id, Title: "Markiplier", Handle: "@markiplier",
		Stats: providers.Stats{Subscribers: 39000000, VideoCount: 5900}},
		Page: ytweb.Page{Items: []ytweb.Item{video("ndsaoMFz9J4", 11*365*24*time.Hour, 379000000)}, Next: "tab2"},
		Sort: got, Sorts: []ytweb.Sort{ytweb.SortLatest, ytweb.SortPopular, ytweb.SortOldest}}, nil
}

func (w *web) ChannelSearch(_ context.Context, id, query string) (ytweb.ChannelPage, error) {
	w.query = query
	return ytweb.ChannelPage{Channel: ytweb.Channel{ID: id, Title: "Markiplier"}, Page: ytweb.Page{Items: []ytweb.Item{
		{Kind: ytweb.KindPlaylist, ID: "PLfnaf", Title: "Five Nights at Freddy's", Stats: providers.Stats{VideoCount: 165}}}}}, nil
}

func (w *web) Playlist(_ context.Context, id string) (ytweb.PlaylistPage, error) {
	rows := w.playlists[id]
	p := ytweb.Page{Items: rows[:min(100, len(rows))]}
	if len(rows) > 100 {
		p.Next = id + " 1"
	}
	return ytweb.PlaylistPage{Playlist: ytweb.Playlist{ID: id, Title: "Five Nights at Freddy's", Channel: "Markiplier",
		ChannelID: chID, Stats: providers.Stats{VideoCount: len(rows)}}, Page: p}, nil
}

func (w *web) Video(_ context.Context, id string) (ytweb.Video, error) {
	if id != "iOztnsBPrAA" {
		return ytweb.Video{}, ytweb.ErrNotFound
	}
	v := video(id, 0, 131220298)
	v.Stats = providers.Stats{Views: 131220298, Likes: 1755316, Published: time.Date(2014, 8, 12, 18, 23, 51, 0, time.UTC)}
	return ytweb.Video{Item: v, Description: "Quite possibly the SCARIEST game"}, nil
}

func (w *web) Stats(_ context.Context, ids []string) map[string]providers.Stats {
	w.statsAsk = append(w.statsAsk, ids...)
	out := map[string]providers.Stats{}
	for _, id := range ids {
		if s, ok := w.exact[id]; ok {
			out[id] = s
		}
	}
	return out
}

func search(t *testing.T, p *ytcatalog.Provider, q catalog.SearchQuery) catalog.SearchBatch {
	t.Helper()
	q.Sources = []catalog.SourceID{ytcatalog.SourceVideo}
	var out catalog.SearchBatch
	if err := p.Search(context.Background(), q, func(b catalog.SearchBatch) error { out = b; return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestVideoSourceSearchesYouTubeItself(t *testing.T) {
	w := &web{}
	p := ytcatalog.New(ytcatalog.Config{Music: music{}, Player: player{}, Web: w})
	b := search(t, p, catalog.SearchQuery{Query: "markiplier", Kinds: []catalog.Kind{catalog.KindChannel},
		Sort: catalog.SortPopular, Filters: map[string]string{catalog.FilterUploaded: "week", "genre": "horror"}})
	if o := w.searched[0]; o.Type != "channel" || o.Sort != "popular" || o.Uploaded != "week" || o.Query != "markiplier" {
		t.Fatalf("asked %+v", o)
	}
	if !slices.Equal(b.Unenforced, []string{"genre"}) || b.Next != "page2" {
		t.Fatalf("unenforced %v, next %q", b.Unenforced, b.Next)
	}
	// only the kind asked for comes back
	if len(b.Items) != 1 || b.Items[0].Kind != catalog.KindChannel || b.Items[0].Stats.Subscribers != 39000000 ||
		b.Items[0].Subtitle != "@markiplier" || !b.Items[0].HasChildren {
		t.Fatalf("items %+v", b.Items)
	}
	all := search(t, p, catalog.SearchQuery{Query: "markiplier fnaf"})
	if len(all.Items) != 3 {
		t.Fatalf("%d items", len(all.Items))
	}
	v := all.Items[1]
	if v.Kind != catalog.KindVideo || !v.Playable || v.Stats.Views != 131220260 || !v.Stats.PublishedApprox ||
		v.Owner == nil || v.Owner.Title != "Markiplier" || v.Owner.Key.Kind != catalog.KindChannel || v.Owner.Key.ID != chID {
		t.Fatalf("video %+v", v)
	}
	if pl := all.Items[2]; pl.Kind != catalog.KindPlaylist || pl.Source != ytcatalog.SourceVideo || pl.Stats.VideoCount != 165 {
		t.Fatalf("playlist %+v", pl)
	}
	// a page further is the batch's next
	more := search(t, p, catalog.SearchQuery{Cursor: "page2"})
	if len(more.Items) != 1 || !slices.Equal(w.more, []string{"page2"}) {
		t.Fatalf("more %+v %v", more.Items, w.more)
	}
}

func TestVideoSourceFallsBackToThePlayersSearch(t *testing.T) {
	w := &web{failed: ytweb.ErrRateLimited}
	p := ytcatalog.New(ytcatalog.Config{Music: music{}, Player: player{}, Web: w})
	b := search(t, p, catalog.SearchQuery{Query: "x", Sort: catalog.SortPopular})
	if b.Failure != nil || len(b.Items) != 1 || b.Items[0].Key.ID != "ccccccccccc" || !slices.Equal(b.Unenforced, []string{"sort"}) {
		t.Fatalf("%+v", b)
	}
	// channels are YouTube's alone: no fallback finds them
	b = search(t, p, catalog.SearchQuery{Query: "x", Kinds: []catalog.Kind{catalog.KindChannel}})
	if b.Failure == nil {
		t.Fatalf("%+v", b)
	}
}

func TestAChannelsTabsAndSorts(t *testing.T) {
	w := &web{}
	p := ytcatalog.New(ytcatalog.Config{Music: music{}, Player: player{}, Web: w})
	ch := catalog.Key{Provider: catalog.ProviderYouTubeMusic, Source: ytcatalog.SourceVideo, Kind: catalog.KindChannel, ID: chID}
	d, err := p.Item(context.Background(), catalog.ItemRequest{Key: ch, Sort: catalog.SortPopular})
	if err != nil {
		t.Fatal(err)
	}
	if w.tab != ytweb.TabVideos || w.sort != ytweb.SortPopular || d.Sort != catalog.SortPopular || len(d.Sorts) != 3 {
		t.Fatalf("tab %q sort %q, answered %q %v", w.tab, w.sort, d.Sort, d.Sorts)
	}
	if len(d.Tabs) != 4 || !d.Tabs[0].Selected || d.Item.Title != "Markiplier" || d.Item.Stats.Subscribers != 39000000 ||
		d.Item.Stats.VideoCount != 5900 || d.Next != "tab2" {
		t.Fatalf("%+v", d)
	}
	if c := d.Children[0]; c.Key.ID != "ndsaoMFz9J4" || c.Stats.Views != 379000000 {
		t.Fatalf("child %+v", c)
	}
	if _, err := p.Item(context.Background(), catalog.ItemRequest{Key: ch, Tab: "playlists"}); err != nil || w.tab != ytweb.TabPlaylists {
		t.Fatalf("%v %q", err, w.tab)
	}
	if _, err := p.Item(context.Background(), catalog.ItemRequest{Key: ch, Tab: "community"}); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("unknown tab: %v", err)
	}
	d, err = p.Item(context.Background(), catalog.ItemRequest{Key: ch, Query: "fnaf"})
	if err != nil || w.query != "fnaf" || d.Children[0].Kind != catalog.KindPlaylist {
		t.Fatalf("search inside: %+v %v", d, err)
	}
}

// "the latest video of a playlist": its pages read, the rows tied on their relative time settled by their exact one
func TestAPlaylistsLatest(t *testing.T) {
	var rows []ytweb.Item
	for i := range 230 {
		rows = append(rows, video(fmt.Sprintf("old%08d", i), time.Duration(230-i)*24*time.Hour+365*24*time.Hour, int64(i)))
	}
	// the last three rows all say "2 weeks ago"; the exact times put b first
	for _, id := range []string{"aaaaaaaaaaa", "bbbbbbbbbbb", "ccccccccccc"} {
		rows = append(rows, video(id, 14*24*time.Hour, 10))
	}
	w := &web{playlists: map[string][]ytweb.Item{"PLfnaf": rows}, exact: map[string]providers.Stats{
		"aaaaaaaaaaa": {Published: now.Add(-15 * 24 * time.Hour), Likes: 1},
		"bbbbbbbbbbb": {Published: now.Add(-13 * 24 * time.Hour), Likes: 2},
		"ccccccccccc": {Published: now.Add(-14 * 24 * time.Hour), Likes: 3},
	}}
	p := ytcatalog.New(ytcatalog.Config{Music: music{}, Player: player{}, Web: w})
	pl := catalog.Key{Provider: catalog.ProviderYouTubeMusic, Source: ytcatalog.SourceVideo, Kind: catalog.KindPlaylist, ID: "PLfnaf"}
	d, err := p.Item(context.Background(), catalog.ItemRequest{Key: pl, Sort: catalog.SortLatest, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Children) != 3 || d.Children[0].Key.ID != "bbbbbbbbbbb" || d.Children[1].Key.ID != "ccccccccccc" ||
		d.Children[0].Stats.PublishedApprox || d.Children[0].Stats.Likes != 2 {
		t.Fatalf("%+v", d.Children)
	}
	if d.Sort != catalog.SortLatest || d.Partial || d.Next != "" || len(w.statsAsk) != 3 {
		t.Fatalf("sort %q partial %v next %q, asked %v", d.Sort, d.Partial, d.Next, w.statsAsk)
	}
	// in its own order, a page at a time
	d, err = p.Item(context.Background(), catalog.ItemRequest{Key: pl})
	if err != nil || len(d.Children) != 100 || d.Next == "" || d.Item.Owner == nil || d.Item.Owner.Key.ID != chID {
		t.Fatalf("%d rows, next %q, %v", len(d.Children), d.Next, err)
	}
	// a list past five pages is read in part
	long := slices.Repeat(rows[:100], 6)
	for i := range long {
		long[i].ID = fmt.Sprintf("x%010d", i)
	}
	w.playlists["PLlong"] = long
	d, err = p.Item(context.Background(), catalog.ItemRequest{Key: catalog.Key{Provider: pl.Provider, Source: pl.Source,
		Kind: pl.Kind, ID: "PLlong"}, Sort: catalog.SortOldest})
	if err != nil || !d.Partial || len(d.Children) != 500 {
		t.Fatalf("partial %v, %d rows, %v", d.Partial, len(d.Children), err)
	}
	// a title filter
	d, _ = p.Item(context.Background(), catalog.ItemRequest{Key: pl, Query: "VIDEO BBB"})
	if len(d.Children) != 0 {
		t.Fatalf("filtered the first page alone: %d", len(d.Children))
	}
}

func TestAVideosOwnCounts(t *testing.T) {
	w := &web{}
	p := ytcatalog.New(ytcatalog.Config{Music: music{}, Player: player{}, Web: w})
	b := search(t, p, catalog.SearchQuery{Query: "markiplier fnaf"})
	v := b.Items[1].Key
	d, err := p.Item(context.Background(), catalog.ItemRequest{Key: v})
	if err != nil {
		t.Fatal(err)
	}
	if d.Item.Stats.Likes != 1755316 || d.Item.Stats.Views != 131220298 || d.Item.Stats.PublishedApprox ||
		d.Item.Title != "video iOztnsBPrAA" || len(d.Panels) != 1 {
		t.Fatalf("%+v", d.Item)
	}
	pg, err := p.Panel(context.Background(), catalog.PanelRequest{Key: v, Panel: "description"})
	if err != nil || pg.Text == "" {
		t.Fatalf("%+v %v", pg, err)
	}
	// a video YouTube does not know, and the listing never showed
	if _, err := p.Item(context.Background(), catalog.ItemRequest{Key: catalog.Key{Provider: v.Provider, Source: v.Source,
		Kind: v.Kind, ID: "zzzzzzzzzzz"}}); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("%v", err)
	}
}
