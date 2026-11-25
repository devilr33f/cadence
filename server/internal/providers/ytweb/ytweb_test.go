package ytweb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// the fixtures are live answers of 2026-11-16 (hl=en, gl=US), trimmed: rows cut to five, tracking ids and the
// visitor id dropped
var testNow = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type call struct {
	endpoint string
	body     map[string]any
}

// fake answers each call by route(endpoint, body); "" is a 404
type fake struct {
	t     *testing.T
	mu    sync.Mutex
	calls []call
	route func(endpoint string, body map[string]any) (int, string)
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ep := strings.TrimPrefix(r.URL.Path, "/youtubei/v1/")
	var body map[string]any
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &body)
	if r.Header.Get("Origin") != "https://www.youtube.com" || r.URL.Query().Get("prettyPrint") != "false" {
		f.t.Errorf("request without the site's origin or prettyPrint=false: %v", r.URL)
	}
	if hl := str(body, "context", "client", "hl"); hl != "en" {
		f.t.Errorf("hl %q", hl)
	}
	f.mu.Lock()
	f.calls = append(f.calls, call{ep, body})
	f.mu.Unlock()
	status, name := f.route(ep, body)
	w.WriteHeader(status)
	if name != "" {
		_, _ = w.Write(fixture(f.t, name))
	}
}

func (f *fake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func newClient(t *testing.T, route func(string, map[string]any) (int, string)) (*Client, *fake, *bytes.Buffer) {
	t.Helper()
	f := &fake{t: t, route: route}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	var logs bytes.Buffer
	c := New(Config{BaseURL: srv.URL, Now: func() time.Time { return testNow }, Timeout: 2 * time.Second,
		Log: slog.New(slog.NewTextHandler(&logs, nil))}, srv.Client())
	return c, f, &logs
}

func TestParseCount(t *testing.T) {
	for in, want := range map[string]int64{
		"131,220,260 views": 131220260, "843K views": 843000, "3.8B plays": 3800000000, "39M subscribers": 39000000,
		"21.6K subscribers": 21600, "1.4 million views": 1400000, "No views": 0, "165 videos": 165, "1 video": 1,
		"10,100 watching": 10100, "5.9K videos": 5900, "29K": 29000,
	} {
		if got, ok := ParseCount(in); !ok || got != want {
			t.Errorf("%q: %d %v, want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "Playlist", "views"} {
		if _, ok := ParseCount(in); ok {
			t.Errorf("%q counted", in)
		}
	}
	if n, ok := countBefore("POV: FNaF 2 Movie MEME, 1.4 million views, 1 year ago - play Short", " view"); !ok || n != 1400000 {
		t.Fatalf("countBefore %d %v", n, ok)
	}
}

func TestParseAgo(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"17 hours ago": 17 * time.Hour, "Streamed 2 months ago": 60 * 24 * time.Hour, "1 year ago": 365 * 24 * time.Hour,
		"3 weeks ago": 21 * 24 * time.Hour, "1.4M views · 1y ago": 365 * 24 * time.Hour, "3mo ago": 90 * 24 * time.Hour,
		"5 minutes ago": 5 * time.Minute, "Premiered 2 days ago": 48 * time.Hour,
	} {
		if got, ok := parseAgo(in, testNow); !ok || testNow.Sub(got) != want {
			t.Errorf("%q: %v %v", in, testNow.Sub(got), ok)
		}
	}
	if _, ok := parseAgo("Nov 15, 2026", testNow); ok {
		t.Fatal("a date read as relative")
	}
}

func TestSearchParams(t *testing.T) {
	for want, o := range map[string]SearchOptions{
		"":             {},
		"EgIQAQ==":     {Type: "video"},
		"EgIQAg==":     {Type: "channel"},
		"EgIQAw==":     {Type: "playlist"},
		"EgIQCQ==":     {Type: "short"},
		"CAM=":         {Sort: "popular"},
		"EgQIAxAB":     {Uploaded: "week", Type: "video"},
		"EgQQARgC":     {Type: "video", Duration: "long"},
		"CAMSBAgCEAE=": {Sort: "popular", Uploaded: "today", Type: "video"},
		"EgJAAQ==":     {Live: true},
	} {
		if got, err := o.params(); err != nil || got != want {
			t.Errorf("%+v: %q %v, want %q", o, got, err, want)
		}
	}
	// youtube no longer sorts a search by date: refused, not sent as an ignored code
	var bad *BadOption
	if _, err := (SearchOptions{Sort: "date"}).params(); !errors.As(err, &bad) || bad.Name != "sort" {
		t.Fatalf("sort by date: %v", err)
	}
}

func TestSearchMixed(t *testing.T) {
	c, f, _ := newClient(t, func(ep string, body map[string]any) (int, string) {
		if ep == "search" && body["query"] == "markiplier fnaf" && body["params"] == nil {
			return 200, "search-plain"
		}
		return 404, ""
	})
	p, err := c.Search(context.Background(), SearchOptions{Query: "  markiplier   fnaf "})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 5 || p.Next == "" || f.count() != 1 {
		t.Fatalf("%d items, next %q, %d calls", len(p.Items), p.Next, f.count())
	}
	pl, v := p.Items[0], p.Items[1]
	if pl.Kind != KindPlaylist || pl.ID != "PL3tRBEVW0hiDL09lO0xjKEix84OY27xet" || pl.Channel != "Markiplier" ||
		pl.ChannelID != "UC7_YxT-KID8kRbqZo7MyscQ" || pl.Stats.VideoCount != 165 {
		t.Fatalf("playlist %+v", pl)
	}
	if v.Kind != KindVideo || v.ID != "iOztnsBPrAA" || v.DurationS != 1064 || v.Stats.Views != 131220260 ||
		v.ChannelID != "UC7_YxT-KID8kRbqZo7MyscQ" || !v.Stats.PublishedApprox || v.Stats.Published.Year() != 2014 ||
		v.Thumbnail != "https://i.ytimg.com/vi/iOztnsBPrAA/hqdefault.jpg" {
		t.Fatalf("video %+v", v)
	}
}

func TestSearchChannelsReadsFieldsByWhatTheySay(t *testing.T) {
	c, _, _ := newClient(t, func(_ string, body map[string]any) (int, string) {
		if body["params"] == "EgIQAg==" {
			return 200, "search-channel"
		}
		return 404, ""
	})
	p, err := c.Search(context.Background(), SearchOptions{Query: "markiplier", Type: "channel"})
	if err != nil {
		t.Fatal(err)
	}
	ch := p.Items[0]
	// subscriberCountText holds the handle and videoCountText the subscribers
	if ch.Kind != KindChannel || ch.ID != "UC7_YxT-KID8kRbqZo7MyscQ" || ch.Handle != "@markiplier" ||
		ch.Stats.Subscribers != 39000000 || ch.Thumbnail == "" {
		t.Fatalf("channel %+v", ch)
	}
}

func TestSearchShorts(t *testing.T) {
	c, _, _ := newClient(t, func(string, map[string]any) (int, string) { return 200, "search-shorts" })
	p, err := c.Search(context.Background(), SearchOptions{Query: "markiplier fnaf", Type: "short"})
	if err != nil {
		t.Fatal(err)
	}
	s := p.Items[0]
	if s.Kind != KindShort || s.ID != "uXKz3SBXZdE" || s.Stats.Views != 1400000 || !s.Stats.PublishedApprox {
		t.Fatalf("short %+v", s)
	}
}

func TestSearchRefusesABadOptionWithoutACall(t *testing.T) {
	c, f, _ := newClient(t, func(string, map[string]any) (int, string) { return 200, "search-plain" })
	var bad *BadOption
	if _, err := c.Search(context.Background(), SearchOptions{Query: "x", Uploaded: "hour"}); !errors.As(err, &bad) {
		t.Fatalf("%v", err)
	}
	if p, err := c.Search(context.Background(), SearchOptions{Query: "   "}); err != nil || len(p.Items) != 0 {
		t.Fatalf("empty query: %v %v", p, err)
	}
	if f.count() != 0 {
		t.Fatalf("%d calls", f.count())
	}
}

// the sort a channel's videos tab offers is followed by the token it carries, never one built here
func TestChannelTabSortsByItsOwnTokens(t *testing.T) {
	var root node
	_ = json.Unmarshal(fixture(t, "ch-videos"), &root)
	popular := sortMenu(root)[SortPopular].token
	if popular == "" {
		t.Fatal("no popular token in the fixture")
	}
	c, f, _ := newClient(t, func(ep string, body map[string]any) (int, string) {
		switch {
		case ep == "navigation/resolve_url" && body["url"] == "https://www.youtube.com/@markiplier":
			return 200, "resolve"
		case ep == "browse" && body["browseId"] == "UC7_YxT-KID8kRbqZo7MyscQ" && body["params"] == tabParams[TabVideos]:
			return 200, "ch-videos"
		case ep == "browse" && body["continuation"] == popular:
			return 200, "ch-videos-popular"
		}
		return 404, ""
	})
	cp, err := c.ChannelTab(context.Background(), "@markiplier", "", SortPopular)
	if err != nil {
		t.Fatal(err)
	}
	if cp.Sort != SortPopular || len(cp.Sorts) != 3 || cp.Page.Items[0].ID != "ndsaoMFz9J4" || cp.Page.Items[0].Stats.Views != 379000000 {
		t.Fatalf("popular: %+v %v %+v", cp.Sort, cp.Sorts, cp.Page.Items[0])
	}
	ch := cp.Channel
	if ch.ID != "UC7_YxT-KID8kRbqZo7MyscQ" || ch.Title != "Markiplier" || ch.Handle != "@markiplier" ||
		ch.Stats.Subscribers != 39000000 || ch.Stats.VideoCount != 5900 {
		t.Fatalf("channel %+v", ch)
	}
	// a channel's own rows name it
	if v := cp.Page.Items[0]; v.Channel != "Markiplier" || v.ChannelID != ch.ID {
		t.Fatalf("row owner %+v", v)
	}
	// the handle resolves once
	if _, err := c.ChannelTab(context.Background(), "@markiplier", TabVideos, ""); err != nil {
		t.Fatal(err)
	}
	resolves := 0
	for _, cl := range f.calls {
		if cl.endpoint == "navigation/resolve_url" {
			resolves++
		}
	}
	if resolves != 1 {
		t.Fatalf("%d resolves", resolves)
	}
}

func TestChannelTabWithoutTheSortAsked(t *testing.T) {
	c, _, _ := newClient(t, func(string, map[string]any) (int, string) { return 200, "ch-shorts" })
	cp, err := c.ChannelTab(context.Background(), "UC7_YxT-KID8kRbqZo7MyscQ", TabShorts, SortOldest)
	if err != nil {
		t.Fatal(err)
	}
	if cp.Sort == SortOldest || len(cp.Page.Items) != 5 || cp.Page.Next != "" {
		t.Fatalf("%+v", cp)
	}
}

func TestChannelPlaylistsSortByBrowse(t *testing.T) {
	c, f, _ := newClient(t, func(_ string, _ map[string]any) (int, string) { return 200, "ch-playlists" })
	cp, err := c.ChannelTab(context.Background(), "UC7_YxT-KID8kRbqZo7MyscQ", TabPlaylists, SortUpdated)
	if err != nil {
		t.Fatal(err)
	}
	last := f.calls[len(f.calls)-1].body
	if cp.Sort != SortUpdated || last["params"] != "EglwbGF5bGlzdHMYBCABMAHyBgQKAkIA" {
		t.Fatalf("sort %q, params %v", cp.Sort, last["params"])
	}
	if pl := cp.Page.Items[0]; pl.Kind != KindPlaylist || pl.Channel != "Markiplier" || pl.Stats.VideoCount != 4 {
		t.Fatalf("%+v", pl)
	}
}

func TestChannelSearch(t *testing.T) {
	c, f, _ := newClient(t, func(_ string, _ map[string]any) (int, string) { return 200, "ch-search" })
	cp, err := c.ChannelSearch(context.Background(), "UC7_YxT-KID8kRbqZo7MyscQ", "fnaf")
	if err != nil {
		t.Fatal(err)
	}
	if b := f.calls[0].body; b["query"] != "fnaf" || b["params"] != channelSearchParams {
		t.Fatalf("body %v", b)
	}
	if pl := cp.Page.Items[0]; pl.Kind != KindPlaylist || pl.Channel != "Markiplier" || pl.Stats.VideoCount != 165 {
		t.Fatalf("%+v", pl)
	}
	if v := cp.Page.Items[1]; v.ID != "zlQenSJ1lZY" || v.Stats.Views != 4446142 {
		t.Fatalf("%+v", v)
	}
}

// a playlist pages by the token beside its rows; the one a level up is a dead end
func TestPlaylistPagesByTheTokenBesideItsRows(t *testing.T) {
	var root node
	_ = json.Unmarshal(fixture(t, "pl-1"), &root)
	section := list(root, "contents", "twoColumnBrowseResultsRenderer", "tabs")[0]
	contents := list(section, "tabRenderer", "content", "sectionListRenderer", "contents")
	rows := list(contents[0], "itemSectionRenderer", "contents")
	nested := tokenOf(rows[len(rows)-1])
	sibling := tokenOf(contents[1])
	if nested == "" || sibling == "" || nested == sibling {
		t.Fatal("fixture lacks its two tokens")
	}
	c, _, _ := newClient(t, func(_ string, body map[string]any) (int, string) {
		switch {
		case body["browseId"] == "VLPL3tRBEVW0hiDL09lO0xjKEix84OY27xet":
			return 200, "pl-1"
		case body["continuation"] == nested:
			return 200, "pl-2"
		}
		return 404, ""
	})
	pp, err := c.Playlist(context.Background(), "PL3tRBEVW0hiDL09lO0xjKEix84OY27xet")
	if err != nil {
		t.Fatal(err)
	}
	pl := pp.Playlist
	if pl.Title != "Five Nights at Freddy's" || pl.Channel != "Markiplier" || pl.ChannelID != "UC7_YxT-KID8kRbqZo7MyscQ" ||
		pl.Stats.VideoCount != 165 || pl.Stats.Views != 540359694 || pl.Description == "" {
		t.Fatalf("playlist %+v", pl)
	}
	if len(pp.Page.Items) != 5 || pp.Page.Items[0].ID != "iOztnsBPrAA" {
		t.Fatalf("rows %+v", pp.Page.Items)
	}
	more, err := c.More(context.Background(), pp.Page.Next)
	if err != nil {
		t.Fatal(err)
	}
	if len(more.Items) != 5 || more.Items[0].ID != "__1uuWTpD_c" || more.Next != "" {
		t.Fatalf("page 2 %+v", more)
	}
	if _, err := c.More(context.Background(), "nope"); !errors.Is(err, ErrBadCursor) {
		t.Fatalf("unknown cursor: %v", err)
	}
}

func TestVideoReadsThePlayersCountsEvenUnplayable(t *testing.T) {
	c, f, _ := newClient(t, func(ep string, body map[string]any) (int, string) {
		if ep == "player" && body["videoId"] == "5K49xpvMYyo" {
			return 200, "player"
		}
		return 404, ""
	})
	v, err := c.Video(context.Background(), "5K49xpvMYyo")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 15, 13, 52, 41, 0, time.FixedZone("", -8*3600))
	if v.Title != "3 SCARY GAMES #117" || v.ChannelID != "UC7_YxT-KID8kRbqZo7MyscQ" || v.DurationS != 2191 ||
		v.Stats.Views != 844462 || v.Stats.Likes != 29867 || !v.Stats.Published.Equal(want) || v.Stats.PublishedApprox ||
		v.Category != "Gaming" || v.Description == "" {
		t.Fatalf("%+v", v)
	}
	// counts read once are reused
	st := c.Stats(context.Background(), []string{"5K49xpvMYyo", "bad id"})
	if st["5K49xpvMYyo"].Likes != 29867 || len(st) != 1 || f.count() != 1 {
		t.Fatalf("stats %v, %d calls", st, f.count())
	}
	if _, err := c.Video(context.Background(), "aaaaaaaaaaa"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown video: %v", err)
	}
}

func TestStatsLeavesOutWhatIsLate(t *testing.T) {
	var n atomic.Int32
	f := &fake{t: t, route: func(string, map[string]any) (int, string) { return 200, "player" }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) > 1 {
			time.Sleep(300 * time.Millisecond)
		}
		f.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL, Now: func() time.Time { return testNow }}, srv.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	// the fixture answers for 5K49xpvMYyo alone: the other id answers as not found, or late
	st := c.Stats(ctx, []string{"5K49xpvMYyo", "dQw4w9WgXcQ"})
	if _, ok := st["dQw4w9WgXcQ"]; ok {
		t.Fatalf("%v", st)
	}
}

func TestRetriesOnceThenSaysWhy(t *testing.T) {
	var n atomic.Int32
	c, _, logs := newClient(t, func(string, map[string]any) (int, string) {
		if n.Add(1) == 1 {
			return 503, ""
		}
		return 200, "search-plain"
	})
	if _, err := c.Search(context.Background(), SearchOptions{Query: "secret words"}); err != nil {
		t.Fatalf("after one 503: %v", err)
	}
	c2, f2, logs2 := newClient(t, func(string, map[string]any) (int, string) { return 429, "" })
	_, err := c2.Search(context.Background(), SearchOptions{Query: "secret words"})
	var e *Error
	if !errors.As(err, &e) || e.Code != "rate_limited" || e.Status != 429 || f2.count() != 2 {
		t.Fatalf("%v (%d calls)", err, f2.count())
	}
	// rule 8: a failure logs the operation and a code, never the query
	for _, l := range []string{logs.String(), logs2.String()} {
		if strings.Contains(l, "secret") {
			t.Fatalf("query logged: %s", l)
		}
	}
	if !strings.Contains(logs2.String(), "ytweb_failed") || !strings.Contains(logs2.String(), "op=search") {
		t.Fatalf("failure not logged: %s", logs2.String())
	}
}

func TestUnreadableAnswerFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }))
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL}, srv.Client())
	if _, err := c.Search(context.Background(), SearchOptions{Query: "x"}); !errors.Is(err, ErrFailed) {
		t.Fatalf("%v", err)
	}
}

func TestValidators(t *testing.T) {
	for _, h := range []string{"@markiplier", "@Lord.Sayo-Nara_1", "@кино"} {
		if !ValidHandle(h) {
			t.Errorf("%q refused", h)
		}
	}
	for _, h := range []string{"markiplier", "@", "@a/b", "@a b"} {
		if ValidHandle(h) {
			t.Errorf("%q taken", h)
		}
	}
	if !ValidChannelID("UC7_YxT-KID8kRbqZo7MyscQ") || ValidChannelID("UC7") || !ValidVideoID("dQw4w9WgXcQ") {
		t.Fatal("ids")
	}
	if safeImage("https://evil.example/x.jpg") != "" || safeImage("//yt3.ggpht.com/a") != "https://yt3.ggpht.com/a" {
		t.Fatal("images")
	}
}
