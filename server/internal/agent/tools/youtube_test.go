package tools_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
)

// ytCatalog answers as the catalog service does for its YouTube source: refs, counts, a channel's tabs
type ytCatalog struct {
	queries []agent.CatalogQuery
	items   []agent.CatalogItem
	asked   int
}

var (
	ytVideo = catalog.Item{Key: catalog.Key{Provider: catalog.ProviderYouTubeMusic, Source: "video", Kind: catalog.KindVideo,
		ID: "iOztnsBPrAA"}, Ref: "c:video", Provider: catalog.ProviderYouTubeMusic, Kind: catalog.KindVideo,
		Title: "WARNING: SCARIEST GAME IN YEARS", Subtitle: "Markiplier", DurationS: 1064, Playable: true,
		Artwork: catalog.Artwork{Hash: "AbCdEfGhIjKlMnOpQrStUv1"},
		Stats:   &catalog.Stats{Views: 131220260, PublishedAt: time.Date(2014, 11, 19, 0, 0, 0, 0, time.UTC), PublishedApprox: true},
		Owner:   &catalog.Owner{Ref: "c:channel", Title: "Markiplier"}}
	ytChannel = catalog.Item{Ref: "c:channel", Kind: catalog.KindChannel, Title: "Markiplier", Subtitle: "@markiplier",
		HasChildren: true, Stats: &catalog.Stats{Subscribers: 39000000, VideoCount: 5900}}
)

func (y *ytCatalog) Cached(catalog.Ref) (catalog.Item, bool) { return catalog.Item{}, false }
func (y *ytCatalog) Options(context.Context, catalog.Ref, string, catalog.Selection) (catalog.Options, error) {
	return catalog.Options{}, catalog.ErrUnsupported
}
func (y *ytCatalog) Resolve(context.Context, agent.CatalogResolve) (catalog.Playable, error) {
	return catalog.Playable{}, catalog.ErrUnsupported
}
func (y *ytCatalog) Act(context.Context, catalog.Ref, string, bool, string) ([]catalog.Action, error) {
	return nil, catalog.ErrUnsupported
}
func (y *ytCatalog) Alternatives(context.Context, catalog.Ref) (catalog.AlternativesView, error) {
	return catalog.AlternativesView{}, nil
}
func (y *ytCatalog) Search(ctx context.Context, q string, limit int) (agent.CatalogFound, error) {
	return y.SearchWith(ctx, agent.CatalogQuery{Query: q, Limit: limit})
}

func (y *ytCatalog) SearchWith(_ context.Context, q agent.CatalogQuery) (agent.CatalogFound, error) {
	y.queries = append(y.queries, q)
	return agent.CatalogFound{Items: []catalog.Item{ytChannel, ytVideo}, Asked: y.asked, Next: "c2",
		Unenforced: []string{"duration"}}, nil
}

func (y *ytCatalog) Item(_ context.Context, r agent.CatalogItem) (catalog.ItemDetail, error) {
	y.items = append(y.items, r)
	if r.Ref != "c:channel" {
		return catalog.ItemDetail{}, catalog.ErrUnknownRef
	}
	return catalog.ItemDetail{Item: ytChannel, Children: []catalog.Item{ytVideo}, Next: "n2",
		Tabs:  []catalog.Tab{{ID: "videos", Selected: true}, {ID: "shorts"}, {ID: "streams"}, {ID: "playlists"}},
		Sorts: []catalog.Sort{catalog.SortLatest, catalog.SortPopular, catalog.SortOldest}, Sort: r.Sort}, nil
}

func ytTool(t *testing.T, y *ytCatalog, votes tools.VotesFunc, name string) agent.Tool {
	t.Helper()
	for _, tl := range tools.YouTube(tools.ScreenDeps{Catalog: y, Votes: votes}) {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("no %s", name)
	return agent.Tool{}
}

func TestYouTubeSearchAsksTheYouTubeSourceWithItsOptions(t *testing.T) {
	y := &ytCatalog{asked: 1}
	tool := ytTool(t, y, nil, "youtube_search")
	if tool.Effects != agent.Read || tool.Output != agent.Untrusted || tool.Reach != nil {
		t.Fatalf("policy %+v", tool)
	}
	out, terr := call(t, tool, "kitchen", `{"query":"markiplier","type":"channel","sort":"popular","uploaded":"week","duration":"long"}`)
	if terr != nil {
		t.Fatal(terr)
	}
	q := y.queries[0]
	if q.Query != "markiplier" || !slices.Equal(q.Kinds, []catalog.Kind{catalog.KindChannel}) || q.Sort != catalog.SortPopular ||
		q.Filters[catalog.FilterUploaded] != "week" || q.Filters[catalog.FilterDuration] != "long" ||
		!slices.Equal(q.Sources, []catalog.SourceID{"video"}) || q.Limit != 6 {
		t.Fatalf("query %+v", q)
	}
	var got struct {
		Items []struct {
			Ref, Kind, Title, Subtitle, Published, Artwork string
			Channel                                        *struct{ Ref, Title string }
			Views, Subscribers                             int64
			VideoCount                                     int
			PublishedApprox                                bool
		}
		Next       string
		Unenforced []string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	ch, v := got.Items[0], got.Items[1]
	if ch.Ref != "c:channel" || ch.Subscribers != 39000000 || ch.VideoCount != 5900 || ch.Subtitle != "@markiplier" {
		t.Fatalf("channel %+v", ch)
	}
	// the channel is named once, with its ref, not again as the subtitle
	if v.Views != 131220260 || v.Channel == nil || v.Channel.Ref != "c:channel" || v.Subtitle != "" ||
		v.Published != "2014-11-19" || !v.PublishedApprox {
		t.Fatalf("video %+v", v)
	}
	// the item's art reaches the dashboard's cards as the proxy's id alone
	if v.Artwork != "AbCdEfGhIjKlMnOpQrStUv1" {
		t.Fatalf("video artwork %q", v.Artwork)
	}
	if got.Next != "c2" || !slices.Equal(got.Unenforced, []string{"duration"}) {
		t.Fatalf("%+v", got)
	}
	// a search by date is not offered: youtube cannot do it
	if _, terr := call(t, tool, "kitchen", `{"query":"x","sort":"date"}`); terr == nil {
		t.Fatal("sort date taken")
	}
}

func TestYouTubeSearchWithoutTheSource(t *testing.T) {
	tool := ytTool(t, &ytCatalog{asked: 0}, nil, "youtube_search")
	if _, terr := call(t, tool, "kitchen", `{"query":"x"}`); terr == nil || terr.Code != "unavailable" {
		t.Fatalf("%+v", terr)
	}
}

func TestYouTubeBrowseAChannelsTab(t *testing.T) {
	y := &ytCatalog{asked: 1}
	tool := ytTool(t, y, nil, "youtube_browse")
	out, terr := call(t, tool, "kitchen", `{"ref":"c:channel","tab":"live","sort":"popular","limit":3}`)
	if terr != nil {
		t.Fatal(terr)
	}
	if r := y.items[0]; r.Tab != "streams" || r.Sort != catalog.SortPopular || r.Limit != 3 {
		t.Fatalf("asked %+v", r)
	}
	var got struct {
		Item     struct{ Title string }
		Tabs     []string
		Sorts    []string
		Sort     string
		Children []struct{ Ref string }
		Next     string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Item.Title != "Markiplier" || !slices.Equal(got.Tabs, []string{"videos", "shorts", "live", "playlists"}) ||
		got.Sort != "popular" || len(got.Sorts) != 3 || got.Children[0].Ref != "c:video" || got.Next != "n2" {
		t.Fatalf("%+v", got)
	}
	if _, terr := call(t, tool, "kitchen", `{"ref":"c:made-up"}`); terr == nil {
		t.Fatal("a made-up ref answered")
	}
}

// votes come only while the setting lets VotesFunc answer, and carry their credit
func TestYouTubeVotesAreAddedWithTheirCredit(t *testing.T) {
	y := &ytCatalog{asked: 1}
	var asked []string
	votes := func(_ context.Context, _ string, ids []string) map[string]tools.Votes {
		asked = ids
		return map[string]tools.Votes{"iOztnsBPrAA": {Likes: 1755316, Dislikes: 40000}}
	}
	out, terr := call(t, ytTool(t, y, votes, "youtube_search"), "kitchen", `{"query":"fnaf"}`)
	if terr != nil {
		t.Fatal(terr)
	}
	if !slices.Equal(asked, []string{"iOztnsBPrAA"}) {
		t.Fatalf("asked %v", asked)
	}
	if !strings.Contains(string(out), `"likes":1755316`) || !strings.Contains(string(out), `"dislikesEstimated":40000`) ||
		!strings.Contains(string(out), "returnyoutubedislike.com") {
		t.Fatalf("%s", out)
	}
	off := func(context.Context, string, []string) map[string]tools.Votes { return nil }
	out, _ = call(t, ytTool(t, y, off, "youtube_search"), "kitchen", `{"query":"fnaf"}`)
	if strings.Contains(string(out), "dislikes") || strings.Contains(string(out), "returnyoutubedislike") {
		t.Fatalf("votes while off: %s", out)
	}
}

func TestCatalogSearchTakesAKind(t *testing.T) {
	y := &ytCatalog{asked: 1}
	tool := screenTool(tools.ScreenDeps{Catalog: y}, "catalog_search")
	out, terr := call(t, tool, "tv", `{"query":"markiplier","kind":"channel"}`)
	if terr != nil {
		t.Fatal(terr)
	}
	if q := y.queries[0]; !slices.Equal(q.Kinds, []catalog.Kind{catalog.KindChannel}) {
		t.Fatalf("%+v", q)
	}
	if !strings.Contains(string(out), `"subscribers":39000000`) || !strings.Contains(string(out), `"channel":{"ref":"c:channel"`) {
		t.Fatalf("%s", out)
	}
}

func TestCatalogItemTakesATabASortAndAQuery(t *testing.T) {
	y := &ytCatalog{asked: 1}
	tool := screenTool(tools.ScreenDeps{Catalog: y}, "catalog_item")
	out, terr := call(t, tool, "tv", `{"ref":"c:channel","tab":"playlists","sort":"latest","query":"fnaf"}`)
	if terr != nil {
		t.Fatal(terr)
	}
	if r := y.items[0]; r.Tab != "playlists" || r.Sort != catalog.SortLatest || r.Query != "fnaf" {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(string(out), `"tabs":["videos","shorts","streams","playlists"]`) || !strings.Contains(string(out), `"views":131220260`) {
		t.Fatalf("%s", out)
	}
}

// a search's items reach the turn's screens as tiles (VideoFound), the videos the model then never reads out
func TestYouTubeSearchShowsItsItemsOnTheScreens(t *testing.T) {
	y := &ytCatalog{asked: 1}
	var shown [][]catalog.Item
	var tool agent.Tool
	for _, tl := range tools.YouTube(tools.ScreenDeps{Catalog: y,
		VideoFound: func(_ agent.Call, items []catalog.Item) { shown = append(shown, items) }}) {
		if tl.Name == "youtube_search" {
			tool = tl
		}
	}
	if _, e := call(t, tool, "kitchen", `{"query":"markiplier"}`); e != nil {
		t.Fatalf("%+v", e)
	}
	if len(shown) != 1 || len(shown[0]) != 2 || shown[0][0].Ref != "c:channel" {
		t.Fatalf("shown %+v", shown)
	}
}

// a browse's opened item and its children reach the screens too: a channel's videos as pressable tiles
func TestYouTubeBrowseShowsItsChildrenOnTheScreens(t *testing.T) {
	y := &ytCatalog{asked: 1}
	var shown [][]catalog.Item
	var tool agent.Tool
	for _, tl := range tools.YouTube(tools.ScreenDeps{Catalog: y,
		VideoFound: func(_ agent.Call, items []catalog.Item) { shown = append(shown, items) }}) {
		if tl.Name == "youtube_browse" {
			tool = tl
		}
	}
	if _, e := call(t, tool, "kitchen", `{"ref":"c:channel"}`); e != nil {
		t.Fatalf("%+v", e)
	}
	if len(shown) != 1 || len(shown[0]) != 2 || shown[0][0].Ref != "c:channel" || shown[0][1].Ref != "c:video" {
		t.Fatalf("shown %+v", shown)
	}
}
