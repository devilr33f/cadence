package torrplayer_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/catalog/catalogtest"
	"git.shino.boo/devilreef/cadence/server/internal/images"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/torrplayer"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/torrplayer/torrplayertest"
)

func rig(t *testing.T) (*torrplayer.Provider, *torrplayertest.Fake, string) {
	t.Helper()
	fake := torrplayertest.New(torrplayertest.Frieren())
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	c, err := torrplayer.NewClient(torrplayer.ClientConfig{BaseURL: srv.URL, Token: "fake-token"})
	if err != nil {
		t.Fatal(err)
	}
	return torrplayer.New(c), fake, srv.URL
}

var (
	frieren  = catalog.Key{Provider: catalog.ProviderTorrplayer, Source: "aniliberty", Kind: catalog.KindSeries, ID: "t-frieren"}
	episode5 = catalog.Key{Provider: catalog.ProviderTorrplayer, Source: "aniliberty", Kind: catalog.KindEpisode, ID: "t-frieren", Episode: "5"}
)

func TestContract(t *testing.T) {
	p, _, _ := rig(t)
	catalogtest.Contract(t, p, catalogtest.Case{Query: "фрирен", Item: frieren, Play: frieren, Episode: "5"})
}

func TestSearchStreamsPerSourceWithFailures(t *testing.T) {
	p, _, _ := rig(t)
	var mu sync.Mutex
	got := map[catalog.SourceID]catalog.SearchBatch{}
	err := p.Search(context.Background(), catalog.SearchQuery{Query: "фрирен"}, func(b catalog.SearchBatch) error {
		mu.Lock()
		defer mu.Unlock()
		got[b.Source] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("batches %d", len(got))
	}
	if f := got["animego"].Failure; f == nil || f.Code != catalog.FailFailed {
		t.Fatalf("animego %+v", got["animego"])
	}
	al, a365 := got["aniliberty"].Items, got["anime365"].Items
	if len(al) != 1 || len(a365) != 2 {
		t.Fatalf("items %d %d", len(al), len(a365))
	}
	k1, _ := catalog.GroupKey(al[0])
	k2, _ := catalog.GroupKey(a365[0])
	if k1 == "" || k1 != k2 {
		t.Fatalf("group keys %q %q", k1, k2)
	}
	if !strings.HasPrefix(al[0].Artwork.URL, "http://") || !p.OwnsArtwork(al[0].Artwork.URL) {
		t.Fatal("poster is not torrplayer's own")
	}
}

func TestSearchUnauthorizedIsAuthFailure(t *testing.T) {
	fake := torrplayertest.New(torrplayertest.Frieren())
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c, _ := torrplayer.NewClient(torrplayer.ClientConfig{BaseURL: srv.URL, Token: "wrong"})
	err := torrplayer.New(c).Search(context.Background(), catalog.SearchQuery{Query: "x"}, func(catalog.SearchBatch) error { return nil })
	var f *catalog.Failure
	if !errors.As(err, &f) || f.Code != catalog.FailAuth {
		t.Fatalf("err %v", err)
	}
}

func TestItemEpisodesAndContinue(t *testing.T) {
	p, fake, _ := rig(t)
	ctx := context.Background()
	_ = p.Search(ctx, catalog.SearchQuery{Query: "фрирен"}, func(catalog.SearchBatch) error { return nil })
	if err := p.Report(ctx, catalog.ProgressReport{Key: frieren, Episode: "1", PositionS: 1400, Watched: true}); err != nil {
		t.Fatal(err)
	}
	if err := p.Report(ctx, catalog.ProgressReport{Key: frieren, Episode: "5", PositionS: 300}); err != nil {
		t.Fatal(err)
	}
	d, err := p.Item(ctx, catalog.ItemRequest{Key: frieren})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Children) != 3 || d.Children[2].Episode.Label != "5.5" || d.Children[2].Episode.Type != "recap" {
		t.Fatalf("children %+v", d.Children)
	}
	if d.Continue == nil || d.Continue.Episode != "5" || d.Continue.PositionS != 300 {
		t.Fatalf("continue %+v", d.Continue)
	}
	if !d.Children[0].Progress.Watched || d.Item.Progress.Count != 1 {
		t.Fatal("progress")
	}
	if len(d.Panels) != 1 || len(d.Actions) != 2 || !d.Actions[0].Enabled {
		t.Fatalf("panels %v actions %+v", d.Panels, d.Actions)
	}
	if len(fake.Reports()) != 2 || fake.Reports()[0].State != "ended" {
		t.Fatalf("reports %+v", fake.Reports())
	}
	page, err := p.Panel(ctx, catalog.PanelRequest{Key: frieren, Panel: "description"})
	if err != nil || page.Text == "" || len(page.Facts) == 0 {
		t.Fatalf("panel %+v %v", page, err)
	}
}

func TestOptionsAndResolveRememberTheVoiceover(t *testing.T) {
	p, fake, base := rig(t)
	ctx := context.Background()
	o, err := p.Options(ctx, catalog.OptionsRequest{Key: frieren, Episode: "5"})
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Groups) != 3 || o.Groups[0].Kind != catalog.GroupVoiceover || o.Groups[0].Hidden != 2 ||
		o.Groups[2].Kind != catalog.GroupTorrent {
		t.Fatalf("groups %+v", o.Groups)
	}
	if o.Selected[catalog.GroupVoiceover] != "AniLibria" || o.Selected[catalog.GroupQuality] != "1080" {
		t.Fatalf("selected %v", o.Selected)
	}
	if !o.Groups[2].Choices[0].Unavailable || o.Groups[2].Choices[0].Reason != catalog.FailNoSeeders {
		t.Fatal("a torrent without seeders is playable")
	}
	sel := catalog.Selection{catalog.GroupVoiceover: "Crunchyroll"}
	var stages []catalog.Stage
	pl, err := p.Resolve(ctx, catalog.ResolveRequest{Key: frieren, Episode: "5", Selection: sel, From: catalog.FromStart,
		OnStage: func(s catalog.Stage) { stages = append(stages, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if pl.Kind != catalog.PlayStream || !strings.HasPrefix(pl.Stream.URL, base+"/resolve/") ||
		len(pl.Stream.Subtitles) != 1 || len(pl.Stream.Skips) != 1 || pl.Stream.Skips[0].StartMS != 90000 {
		t.Fatalf("playable %+v", pl.Stream)
	}
	if pl.Selected[catalog.GroupQuality] != "720" || len(stages) != 2 {
		t.Fatalf("selected %v stages %v", pl.Selected, stages)
	}
	if err := p.Remember(ctx, frieren, pl.Selected); err != nil {
		t.Fatal(err)
	}
	if fake.Prefs("aniliberty", "t-frieren")["translator"] != "Crunchyroll" {
		t.Fatalf("prefs %v", fake.Prefs("aniliberty", "t-frieren"))
	}
	// next episode, same voiceover: nothing chosen, the prefs pick it
	pl, err = p.Resolve(ctx, catalog.ResolveRequest{Key: episode5, From: catalog.FromResume})
	if err != nil || pl.Selected[catalog.GroupVoiceover] != "Crunchyroll" {
		t.Fatalf("remembered %v %v", pl.Selected, err)
	}
	if _, err := p.Resolve(ctx, catalog.ResolveRequest{Key: frieren, Episode: "5",
		Selection: catalog.Selection{catalog.GroupTorrent: "t-dead"}}); !errors.Is(err, catalog.ErrUnplayable) {
		t.Fatalf("dead torrent %v", err)
	}
	pl, err = p.Resolve(ctx, catalog.ResolveRequest{Key: frieren, Episode: "5",
		Selection: catalog.Selection{catalog.GroupTorrent: "t-live"}})
	if err != nil || pl.Stream.InfoHash == "" || !strings.Contains(pl.Stream.URL, "/play/") {
		t.Fatalf("torrent %+v %v", pl.Stream, err)
	}
}

func TestActsAndBrowse(t *testing.T) {
	p, fake, _ := rig(t)
	ctx := context.Background()
	_ = p.Search(ctx, catalog.SearchQuery{Query: "фрирен"}, func(catalog.SearchBatch) error { return nil })
	acts, err := p.Act(ctx, catalog.ActRequest{Key: frieren, Action: "subscribe", On: true})
	if err != nil || !acts[1].Toggled {
		t.Fatalf("subscribe %+v %v", acts, err)
	}
	if _, err := p.Act(ctx, catalog.ActRequest{Key: frieren, Action: "my_list", Value: "completed"}); err != nil {
		t.Fatal(err)
	}
	if fake.MyListStatus(52991) != "completed" {
		t.Fatal("my list not set")
	}
	if _, err := p.Act(ctx, catalog.ActRequest{Key: frieren, Action: "my_list", Value: "bogus"}); !errors.Is(err, catalog.ErrRefused) {
		t.Fatalf("bogus status %v", err)
	}
	if _, err := p.Act(ctx, catalog.ActRequest{Key: episode5, Action: "watched", On: true}); err != nil {
		t.Fatal(err)
	}
	page, err := p.Browse(ctx, catalog.BrowseRequest{Path: catalog.FeedRoot})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, s := range page.Sections {
		ids = append(ids, s.ID)
	}
	// the entry is completed now, not watching: an empty section is left out
	if strings.Join(ids, ",") != "continue,subscriptions" {
		t.Fatalf("sections %v", ids)
	}
	page, _ = p.Browse(ctx, catalog.BrowseRequest{Path: catalog.FeedMyList, Filters: map[string]string{"status": "completed"}})
	entry := page.Sections[0].Items[0]
	d, err := p.Item(ctx, catalog.ItemRequest{Key: entry.Key})
	if err != nil || d.Item.Source != "aniliberty" {
		t.Fatalf("my list entry %+v %v", d.Item, err)
	}
}

// the airing feed: a linked row opens by its provider ids exactly the way a
// search row does, an unlinked one by its Shikimori id, and the row carries
// what the home card shows — year, kind, score, the ongoing badge, a poster
// on torrplayer's own proxy
func TestScheduleFeed(t *testing.T) {
	lib := torrplayertest.Frieren()
	lib.Schedule = []torrplayertest.Title{
		{Source: "aniliberty", ID: "t-frieren", NameRu: "Провожающая в последний путь Фрирен",
			NameEn: "Frieren: Beyond Journey's End", Year: 2026, Kind: "tv", Shikimori: 52991,
			Status: "ongoing", Score: 8.9},
		{Source: "", ID: "z-999", NameRu: "То, чего никто не размещает", Year: 2026, Kind: "tv",
			Shikimori: 99999, Status: "ongoing", Score: 7.4},
	}
	fake := torrplayertest.New(lib)
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c, err := torrplayer.NewClient(torrplayer.ClientConfig{BaseURL: srv.URL, Token: "fake-token"})
	if err != nil {
		t.Fatal(err)
	}
	p := torrplayer.New(c)
	ctx := context.Background()

	page, err := p.Browse(ctx, catalog.BrowseRequest{Path: catalog.FeedNewEpisodes})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections) != 1 {
		t.Fatalf("sections %+v", page.Sections)
	}
	sec := page.Sections[0]
	if sec.ID != catalog.FeedNewEpisodes || sec.Title != "Сейчас на экранах" || len(sec.Items) != 2 {
		t.Fatalf("section %+v", sec)
	}
	linked, unlinked := sec.Items[0], sec.Items[1]
	if linked.Key != (catalog.Key{Provider: catalog.ProviderTorrplayer, Source: "aniliberty",
		Kind: catalog.KindSeries, ID: "t-frieren"}) {
		t.Fatalf("linked key %+v", linked.Key)
	}
	if linked.Title != "Провожающая в последний путь Фрирен" || linked.Subtitle != "Frieren: Beyond Journey's End" ||
		linked.Show.Year != 2026 || linked.Show.Format != "tv" || linked.Show.Score != 8.9 ||
		linked.Show.Status != "ongoing" {
		t.Fatalf("linked show %+v", linked.Show)
	}
	if linked.Badges[0].Kind != catalog.BadgeOngoing || linked.Unavailable {
		t.Fatalf("linked badges %+v unavailable %v", linked.Badges, linked.Unavailable)
	}
	if !strings.HasPrefix(linked.Artwork.URL, srv.URL+"/api/v1/image?") {
		t.Fatalf("poster %s", linked.Artwork.URL)
	}
	// a schedule row opens like any other: the ids are the provider's own
	if d, err := p.Item(ctx, catalog.ItemRequest{Key: linked.Key}); err != nil || len(d.Children) == 0 {
		t.Fatalf("item %+v %v", d, err)
	}
	if unlinked.Key != (catalog.Key{Provider: catalog.ProviderTorrplayer, Source: "shikimori",
		Kind: catalog.KindSeries, ID: "99999"}) || !unlinked.Unavailable {
		t.Fatalf("unlinked %+v", unlinked)
	}

	// the limit rides along: one row back
	page, err = p.Browse(ctx, catalog.BrowseRequest{Path: catalog.FeedNewEpisodes, Limit: 1})
	if err != nil || len(page.Sections[0].Items) != 1 {
		t.Fatalf("limited %+v %v", page, err)
	}

	// and the row joins the home feeds, after continue watching
	if err := p.Report(ctx, catalog.ProgressReport{Key: frieren, Episode: "1", PositionS: 1400}); err != nil {
		t.Fatal(err)
	}
	root, err := p.Browse(ctx, catalog.BrowseRequest{Path: catalog.FeedRoot})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, s := range root.Sections {
		ids = append(ids, s.ID)
	}
	if strings.Join(ids, ",") != "continue,new_episodes,my_list" {
		t.Fatalf("root sections %v", ids)
	}
}

// a torrplayer without the schedule endpoint answers 404: the row is simply
// absent, and nothing else about the feeds changes
func TestScheduleFeedAbsentWithoutTheEndpoint(t *testing.T) {
	p, _, _ := rig(t)
	ctx := context.Background()
	if _, err := p.Browse(ctx, catalog.BrowseRequest{Path: catalog.FeedNewEpisodes}); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("err %v", err)
	}
	root, err := p.Browse(ctx, catalog.BrowseRequest{Path: catalog.FeedRoot})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range root.Sections {
		if s.ID == catalog.FeedNewEpisodes {
			t.Fatalf("sections %+v", root.Sections)
		}
	}
}

// AniSkip-style ranges beyond the provider's own markers: the movie's
// resolve carries none, so the skips endpoint's answer becomes the
// stream's — the ranges the tv player turns into its «Пропустить
// опенинг/эндинг» pill — while Frieren's own marker rides with resolve
// itself and is never re-asked.
func TestResolveMergesSkips(t *testing.T) {
	lib := torrplayertest.Frieren()
	lib.Skips = map[string][]torrplayertest.SkipSeg{
		"t-77:1": {
			{Kind: "opening", Start: 60, Stop: 150},
			{Kind: "ending", Start: 5100, Stop: 5280},
		},
	}
	fake := torrplayertest.New(lib)
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c, err := torrplayer.NewClient(torrplayer.ClientConfig{BaseURL: srv.URL, Token: "fake-token"})
	if err != nil {
		t.Fatal(err)
	}
	p := torrplayer.New(c)
	ctx := context.Background()

	movie := catalog.Key{Provider: catalog.ProviderTorrplayer, Source: "anime365", Kind: catalog.KindMovie, ID: "t-77"}
	pl, err := p.Resolve(ctx, catalog.ResolveRequest{Key: movie, Episode: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pl.Stream.Skips) != 2 ||
		pl.Stream.Skips[0].Kind != catalog.SkipOpening || pl.Stream.Skips[0].StartMS != 60000 || pl.Stream.Skips[0].EndMS != 150000 ||
		pl.Stream.Skips[1].Kind != catalog.SkipEnding || pl.Stream.Skips[1].StartMS != 5100000 || pl.Stream.Skips[1].EndMS != 5280000 {
		t.Fatalf("skips %+v", pl.Stream.Skips)
	}

	before := fake.SkipsCalls()
	pl, err = p.Resolve(ctx, catalog.ResolveRequest{Key: frieren, Episode: "5"})
	if err != nil || len(pl.Stream.Skips) != 1 || pl.Stream.Skips[0].StartMS != 90000 {
		t.Fatalf("skips %+v %v", pl.Stream.Skips, err)
	}
	if fake.SkipsCalls() != before {
		t.Fatal("a resolve that carries the provider's own markers must not re-ask for ranges")
	}
}

// a torrplayer without the endpoint, and one that simply knows nothing,
// both still play: the stream is handed over without ranges, never refused
func TestResolveWithoutSkipsStillPlays(t *testing.T) {
	p, fake, _ := rig(t)
	ctx := context.Background()
	movie := catalog.Key{Provider: catalog.ProviderTorrplayer, Source: "anime365", Kind: catalog.KindMovie, ID: "t-77"}
	pl, err := p.Resolve(ctx, catalog.ResolveRequest{Key: movie, Episode: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if pl.Stream.URL == "" || len(pl.Stream.Skips) != 0 {
		t.Fatalf("playable %+v", pl.Stream)
	}
	if fake.SkipsCalls() != 1 {
		t.Fatalf("skips calls %d", fake.SkipsCalls())
	}

	// the older torrplayer: the endpoint answers 404, playback is the same
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/skips") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fake.ServeHTTP(w, r)
	}))
	defer old.Close()
	c, err := torrplayer.NewClient(torrplayer.ClientConfig{BaseURL: old.URL, Token: "fake-token"})
	if err != nil {
		t.Fatal(err)
	}
	if pl, err := torrplayer.New(c).Resolve(ctx, catalog.ResolveRequest{Key: movie, Episode: "1"}); err != nil || pl.Stream.URL == "" ||
		len(pl.Stream.Skips) != 0 {
		t.Fatalf("older torrplayer: %+v %v", pl.Stream, err)
	}
}

// torrplayer with its [http] and [control] ports split signs its artwork on
// the stream server's port, which serves no /api/v1/image: every poster is
// moved onto the configured control base, so the image proxy issues it an
// id (a local url) and fetches it there
func TestPostersOnASplitStreamPortAreFetchedFromTheControlAPI(t *testing.T) {
	stream := httptest.NewServer(http.NotFoundHandler())
	defer stream.Close()
	lib := torrplayertest.Frieren()
	lib.StreamOrigin = stream.URL
	lib.MyList = map[int]string{52991: "watching"}
	fake := torrplayertest.New(lib)
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c, err := torrplayer.NewClient(torrplayer.ClientConfig{BaseURL: srv.URL, Token: "fake-token"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := torrplayer.New(c).Report(ctx, catalog.ProgressReport{Key: frieren, Episode: "1", PositionS: 300}); err != nil {
		t.Fatal(err)
	}
	// a fresh provider has seen no search: its items carry what each feed answered
	// (continue watching draws Shikimori's cover instead: TestContinueTakesTheShikimoriCover)
	p := torrplayer.New(c)
	var posters []string
	for _, path := range []string{catalog.FeedMyList} {
		page, err := p.Browse(ctx, catalog.BrowseRequest{Path: path})
		if err != nil || len(page.Sections) == 0 || len(page.Sections[0].Items) == 0 {
			t.Fatalf("%s: %+v %v", path, page, err)
		}
		posters = append(posters, page.Sections[0].Items[0].Artwork.URL)
	}
	d, err := torrplayer.New(c).Item(ctx, catalog.ItemRequest{Key: frieren})
	if err != nil {
		t.Fatal(err)
	}
	posters = append(posters, d.Item.Artwork.URL)
	_ = p.Search(ctx, catalog.SearchQuery{Query: "фрирен"}, func(b catalog.SearchBatch) error {
		for _, it := range b.Items {
			posters = append(posters, it.Artwork.URL)
		}
		return nil
	})
	proxy := images.New(images.Config{})
	for i, poster := range posters {
		if !strings.HasPrefix(poster, srv.URL+"/api/v1/image?") || !p.OwnsArtwork(poster) {
			t.Fatalf("poster %d is not on the control api", i)
		}
		id := proxy.Ref(poster, p.OwnsArtwork(poster))
		if typ, data, err := proxy.Get(ctx, id); err != nil || typ != "image/png" || len(data) == 0 {
			t.Fatalf("poster %d: %q %v", i, typ, err)
		}
	}
	if len(posters) < 3 {
		t.Fatalf("posters %d", len(posters))
	}
}

// continue watching carries Shikimori's 2:3 cover (torrplayer's own feed draws
// it in place of the provider's, whose aspect varies) as a poster, with the
// watch progress beside it
func TestContinueTakesTheShikimoriCover(t *testing.T) {
	p, _, _ := rig(t)
	ctx := context.Background()
	if _, err := p.Act(ctx, catalog.ActRequest{Key: episode5, Action: "watched", On: true}); err != nil {
		t.Fatal(err)
	}
	page, err := p.Browse(ctx, catalog.BrowseRequest{Path: catalog.FeedContinue})
	if err != nil || len(page.Sections) == 0 || len(page.Sections[0].Items) == 0 {
		t.Fatalf("continue %+v %v", page, err)
	}
	it := page.Sections[0].Items[0]
	if it.Artwork.URL != "https://shikimori.one/system/animes/original/52991.jpg" || it.Artwork.Aspect != "poster" {
		t.Fatalf("artwork %+v", it.Artwork)
	}
	if it.Progress == nil || it.Progress.Count != 1 {
		t.Fatalf("progress %+v", it.Progress)
	}
}

// the owner's list entry of a title comes from the list torrplayer keeps linked, read once for a while; a title
// not on it, or with no Shikimori id, has none
func TestRateReadsTheLinkedList(t *testing.T) {
	p, _, _ := rig(t)
	r, ok := p.Rate(context.Background(), catalog.ExternalIDs{Shikimori: "52991"})
	if !ok || r != (catalog.UserRate{Status: "watching", Score: 9, Watched: 5}) {
		t.Fatalf("rate %+v %v", r, ok)
	}
	if _, ok := p.Rate(context.Background(), catalog.ExternalIDs{Shikimori: "1"}); ok {
		t.Fatal("a title off the list")
	}
	if _, ok := p.Rate(context.Background(), catalog.ExternalIDs{MAL: "52991"}); ok {
		t.Fatal("no shikimori id")
	}
}
