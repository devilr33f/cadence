package ytmusic_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytmusic"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type innerReq struct {
	Path                                              string
	Query, Params, BrowseID, Continuation, PlaylistID string
	VideoID                                           *string
}

// innerTube answers each request with the body route picks, and keeps them
type innerTube struct {
	mu   sync.Mutex
	reqs []innerReq
}

func (f *innerTube) serve(route func(innerReq) []byte) (*ytmusic.Client, func()) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var q innerReq
		_ = json.Unmarshal(b, &q)
		q.Path = r.URL.Path
		f.mu.Lock()
		f.reqs = append(f.reqs, q)
		f.mu.Unlock()
		body := route(q)
		if body == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, probetest.Leak)
			return
		}
		_, _ = w.Write(body)
	}))
	return ytmusic.New(ytmusic.Config{BaseURL: srv.URL}, probetest.Client(srv)), srv.Close
}

func searchFixture(t *testing.T, kind, file string) []providers.Collection {
	t.Helper()
	body := read(t, file)
	var f innerTube
	c, done := f.serve(func(innerReq) []byte { return body })
	defer done()
	got, err := c.SearchCollections(context.Background(), kind, "linkin park", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.reqs) != 1 || f.reqs[0].Path != "/youtubei/v1/search" || f.reqs[0].Params == "" || f.reqs[0].Query != "linkin park" {
		t.Fatalf("requests %+v", f.reqs)
	}
	return got
}

// the fixtures are trimmed live answers of the albums, playlists and
// artists tabs
func TestSearchAlbums(t *testing.T) {
	got := searchFixture(t, providers.KindAlbum, "albums.json")
	if len(got) != 3 {
		t.Fatalf("%d albums", len(got))
	}
	first := got[0]
	if !strings.HasSuffix(first.ArtworkURL, "=w544-h544") {
		t.Fatalf("artwork %q", first.ArtworkURL)
	}
	first.ArtworkURL = ""
	want := providers.Collection{
		Source: "youtube", Kind: "album", ID: "MPREb_qMlbe7gLeuH", Title: "Meteora", Owner: "Linkin Park",
		WebURL: "https://music.youtube.com/browse/MPREb_qMlbe7gLeuH",
	}
	if first != want {
		t.Fatalf("first %+v", first)
	}
	// another tab's rows are not albums
	if len(searchFixture(t, providers.KindArtist, "albums.json")) != 0 {
		t.Fatal("album rows read as artists")
	}
}

func TestSearchPlaylists(t *testing.T) {
	got := searchFixture(t, providers.KindPlaylist, "playlists.json")
	if len(got) != 3 {
		t.Fatalf("%d playlists", len(got))
	}
	if got[0].ID != "VLPL9LkJszkF_Z6bJ82689htd2wch-HVbzCO" || got[0].Title != "Linkin Park best 50 songs" ||
		got[0].Owner != "𝐂𝐫𝐨𝐰𝐳^" || got[0].WebURL != "https://music.youtube.com/playlist?list=PL9LkJszkF_Z6bJ82689htd2wch-HVbzCO" {
		t.Fatalf("first %+v", got[0])
	}
	if got[2].Owner != "Linkin Park" {
		t.Fatalf("third %+v", got[2])
	}
	if got[0].Stats.Views != 88000000 || got[1].Stats.Views != 5900000 || got[2].Stats.Views != 3000000 {
		t.Fatalf("views %+v %+v %+v", got[0].Stats, got[1].Stats, got[2].Stats)
	}
}

func TestSearchArtistsKeepsTheirRadio(t *testing.T) {
	got := searchFixture(t, providers.KindArtist, "artists.json")
	if len(got) != 2 {
		t.Fatalf("%d artists", len(got))
	}
	if got[0].ID != "UCxgN32UVVztKAQd2HkXzBtw" || got[0].Title != "Linkin Park" || got[0].Radio != "RDEMww6ZEHgLhQ-8eu_x7Z-FJw" ||
		got[0].Owner != "" || got[0].WebURL != "https://music.youtube.com/channel/UCxgN32UVVztKAQd2HkXzBtw" {
		t.Fatalf("first %+v", got[0])
	}
	if got[0].Stats.MonthlyAudience != 176000000 || got[1].Stats.Subscribers != 49200 {
		t.Fatalf("counts %+v %+v", got[0].Stats, got[1].Stats)
	}
}

func TestAlbumSongsTakeTheAlbumsNames(t *testing.T) {
	body := read(t, "album.json")
	var f innerTube
	c, done := f.serve(func(innerReq) []byte { return body })
	defer done()
	album := providers.Collection{Source: "youtube", Kind: "album", ID: "MPREb_qMlbe7gLeuH", Title: "Meteora", Owner: "Linkin Park", ArtworkURL: "https://lh3.googleusercontent.com/a=w544-h544"}
	got, err := c.CollectionSongs(context.Background(), album, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.reqs) != 1 || f.reqs[0].Path != "/youtubei/v1/browse" || f.reqs[0].BrowseID != album.ID {
		t.Fatalf("requests %+v", f.reqs)
	}
	// the page's carousels (other versions, more by the artist) are not its songs
	if len(got) != 4 {
		t.Fatalf("%d songs", len(got))
	}
	want := providers.Track{
		Source: "youtube", ID: "oWfGOVWrueo", VideoID: "oWfGOVWrueo", Title: "Don't Stay", Artist: "Linkin Park", Album: "Meteora",
		DurationS: 188, ArtworkURL: album.ArtworkURL, WebURL: "https://music.youtube.com/watch?v=oWfGOVWrueo",
		Stats: providers.Stats{Plays: 167000000},
	}
	if got[1] != want {
		t.Fatalf("second %+v", got[1])
	}
}

func TestPlaylistSongsFollowItsPages(t *testing.T) {
	first, more := read(t, "playlist.json"), read(t, "playlist-more.json")
	var f innerTube
	c, done := f.serve(func(q innerReq) []byte {
		if q.Continuation != "" {
			return more
		}
		return first
	})
	defer done()
	list := providers.Collection{Source: "youtube", Kind: "playlist", ID: "VLPL0GvsLQil0MmYC96KEs_7dTNsLm1PS6JX"}
	got, err := c.CollectionSongs(context.Background(), list, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.reqs) != 2 || f.reqs[0].BrowseID != list.ID || f.reqs[1].BrowseID != "" || !strings.HasPrefix(f.reqs[1].Continuation, "4qmF") {
		t.Fatalf("requests %+v", f.reqs)
	}
	if len(got) != 5 {
		t.Fatalf("%d songs", len(got))
	}
	if got[0].Title != "Bohemian Rhapsody" || got[0].Artist != "Queen" || got[0].DurationS != 360 {
		t.Fatalf("first %+v", got[0])
	}
	if got[3].Title != "Livin' On A Prayer" || got[4].Artist != "thedarkknightofsteel" {
		t.Fatalf("next page %+v %+v", got[3], got[4])
	}
	// the limit ends it before the next page
	f.reqs = nil
	got, err = c.CollectionSongs(context.Background(), list, 2)
	if err != nil || len(got) != 2 || len(f.reqs) != 1 {
		t.Fatalf("%v, %d songs, %d requests", err, len(got), len(f.reqs))
	}
}

func TestArtistSongsAreItsRadio(t *testing.T) {
	body := read(t, "artist-radio.json")
	var f innerTube
	c, done := f.serve(func(innerReq) []byte { return body })
	defer done()
	artist := providers.Collection{Source: "youtube", Kind: "artist", ID: "UCxgN32UVVztKAQd2HkXzBtw", Title: "Linkin Park", Radio: "RDEMww6ZEHgLhQ-8eu_x7Z-FJw"}
	got, err := c.CollectionSongs(context.Background(), artist, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.reqs) != 1 || f.reqs[0].Path != "/youtubei/v1/next" || f.reqs[0].PlaylistID != artist.Radio || f.reqs[0].VideoID != nil {
		t.Fatalf("requests %+v", f.reqs)
	}
	if len(got) != 3 || got[0].Title != "Easier to Run" || got[0].Artist != "Linkin Park" {
		t.Fatalf("songs %+v", got)
	}
}

func TestCollectionsRefuseWhatTheyDidNotFind(t *testing.T) {
	var f innerTube
	c, done := f.serve(func(innerReq) []byte { return nil })
	defer done()
	for _, col := range []providers.Collection{
		{Source: "youtube", Kind: "artist", ID: "UCxgN32UVVztKAQd2HkXzBtw"}, // no radio
		{Source: "youtube", Kind: "album", ID: "../../evil"},
		{Source: "spotify", Kind: "album", ID: "MPREb_qMlbe7gLeuH"},
		{Source: "youtube", Kind: "song", ID: "MPREb_qMlbe7gLeuH"},
	} {
		if _, err := c.CollectionSongs(context.Background(), col, 10); err == nil || err.Error() != providers.CodeMusicFailed {
			t.Fatalf("%+v: %v", col, err)
		}
	}
	if len(f.reqs) != 0 {
		t.Fatalf("requests %+v", f.reqs)
	}
	// an upstream failure is the code alone
	_, err := c.CollectionSongs(context.Background(), providers.Collection{Source: "youtube", Kind: "album", ID: "MPREb_qMlbe7gLeuH"}, 10)
	if err == nil || err.Error() != providers.CodeMusicFailed {
		t.Fatalf("err %v", err)
	}
	if _, err := c.SearchCollections(context.Background(), "song", "q", 3); err == nil {
		t.Fatal("songs through the collections search")
	}
}
