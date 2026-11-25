package spotify_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
	"git.shino.boo/devilreef/cadence/server/internal/providers/spotify"
)

const (
	albumID    = "4Gfnly5CzMJQqkUFfoHaP3"
	playlistID = "37i9dQZF1DXcBWIGoYBM5M"
	artistID   = "6XyY86QOPPrYVGvF9ch6wz"
)

// the answers follow the Web API reference's shapes, cut to what is read
var collectionAnswers = map[string]string{
	"album": `{"albums":{"items":[
		{"id":"` + albumID + `","name":"Meteora","total_tracks":13,"artists":[{"name":"Linkin Park"}],
		 "images":[{"url":"https://i.scdn.co/image/meteora"}]},
		null,
		{"id":"bad id","name":"x"}]}}`,
	"playlist": `{"playlists":{"items":[
		null,
		{"id":"` + playlistID + `","name":"This Is Linkin Park","owner":{"display_name":"Spotify"},"tracks":{"total":50},"images":[]},
		{"id":"5wIhhRTHuWbKCOd7AeC3w1","name":"renamed","owner":{"display_name":"someone"},"items":{"total":7}}]}}`,
	"artist": `{"artists":{"items":[{"id":"` + artistID + `","name":"Linkin Park","images":[{"url":"https://i.scdn.co/image/lp"}]}]}}`,
}

// collectionAPI is Spotify's token, search, album, playlist and artist reads
type collectionAPI struct {
	mu    sync.Mutex
	paths []string
	// status by path, for the refusals
	status map[string]int
}

func (f *collectionAPI) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/token" {
			_, _ = io.WriteString(w, `{"access_token":"app-token","expires_in":3600}`)
			return
		}
		q := r.URL.Query()
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path+"?offset="+q.Get("offset"))
		st := f.status[r.URL.Path]
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer app-token" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		if st != 0 {
			w.WriteHeader(st)
			_, _ = io.WriteString(w, probetest.Leak)
			return
		}
		offset, _ := strconv.Atoi(q.Get("offset"))
		switch r.URL.Path {
		case "/v1/search":
			if q.Get("type") == "track" {
				_, _ = io.WriteString(w, artistTracks(q.Get("q"), offset))
				return
			}
			_, _ = io.WriteString(w, collectionAnswers[q.Get("type")])
		case "/v1/albums/" + albumID + "/tracks":
			_, _ = io.WriteString(w, trackPage(offset, 60, 50, false))
		case "/v1/playlists/" + playlistID + "/tracks", "/v1/playlists/" + playlistID + "/items":
			_, _ = io.WriteString(w, trackPage(offset, 120, 100, r.URL.Path[len(r.URL.Path)-5:] == "items"))
		default:
			t.Errorf("path %s", r.URL.Path)
		}
	}
}

// trackPage is a page of total numbered tracks; renamed puts them under "item"
func trackPage(offset, total, size int, renamed bool) string {
	var items []string
	for i := offset; i < min(offset+size, total); i++ {
		id := strconv.Itoa(1000000000 + i)
		tr := `{"id":"` + id + strings.Repeat("0", 12) + `","name":"Song ` + strconv.Itoa(i) + `","type":"track","duration_ms":180000,"artists":[{"name":"Linkin Park"}]}`
		switch {
		case renamed:
			items = append(items, `{"item":`+tr+`}`)
		case size == 100:
			items = append(items, `{"track":`+tr+`}`)
		default:
			items = append(items, tr)
		}
	}
	next := "null"
	if offset+size < total {
		next = `"https://api.spotify.com/next"`
	}
	return `{"items":[` + strings.Join(items, ",") + `],"next":` + next + `}`
}

// artistTracks are ten a page: by the artist, a feature of it, and someone else's
func artistTracks(q string, offset int) string {
	if q != `artist:"Linkin Park"` {
		return `{"tracks":{"items":[]}}`
	}
	var items []string
	for i := range 10 {
		artist := artistID
		if i%3 == 2 {
			artist = "0000000000000000000000"
		}
		n := strconv.Itoa(offset + i)
		items = append(items, `{"id":"t`+n+`","name":"Track `+n+`","duration_ms":200000,"artists":[{"id":"`+artist+`","name":"x"}],"album":{"name":"a","images":[]}}`)
	}
	return `{"tracks":{"items":[` + strings.Join(items, ",") + `]}}`
}

func collectionClient(t *testing.T) (*spotify.Client, *collectionAPI, func()) {
	f := &collectionAPI{status: map[string]int{}}
	srv := httptest.NewTLSServer(f.handler(t))
	c := spotify.New(spotify.Config{AccountsURL: srv.URL, APIURL: srv.URL, ClientID: "id", ClientSecret: probetest.Key}, probetest.Client(srv))
	return c, f, srv.Close
}

func TestSearchCollections(t *testing.T) {
	c, _, done := collectionClient(t)
	defer done()
	albums, err := c.SearchCollections(context.Background(), "album", "meteora", 5)
	if err != nil || len(albums) != 1 {
		t.Fatalf("%v %+v", err, albums)
	}
	want := providers.Collection{
		Source: "spotify", Kind: "album", ID: albumID, Title: "Meteora", Owner: "Linkin Park", Tracks: 13,
		ArtworkURL: "https://i.scdn.co/image/meteora", WebURL: "https://open.spotify.com/album/" + albumID,
	}
	if albums[0] != want {
		t.Fatalf("album %+v", albums[0])
	}
	lists, err := c.SearchCollections(context.Background(), "playlist", "linkin park", 5)
	if err != nil || len(lists) != 2 || lists[0].Owner != "Spotify" || lists[0].Tracks != 50 || lists[1].Tracks != 7 {
		t.Fatalf("%v %+v", err, lists)
	}
	artists, err := c.SearchCollections(context.Background(), "artist", "linkin park", 5)
	if err != nil || len(artists) != 1 || artists[0].ID != artistID || artists[0].WebURL != "https://open.spotify.com/artist/"+artistID {
		t.Fatalf("%v %+v", err, artists)
	}
	if _, err := c.SearchCollections(context.Background(), "song", "x", 5); err == nil {
		t.Fatal("songs through the collections search")
	}
}

func TestAlbumSongsReadItsPages(t *testing.T) {
	c, f, done := collectionClient(t)
	defer done()
	album := providers.Collection{Source: "spotify", Kind: "album", ID: albumID, Title: "Meteora", ArtworkURL: "https://i.scdn.co/image/meteora"}
	got, err := c.CollectionSongs(context.Background(), album, 200)
	if err != nil || len(got) != 60 {
		t.Fatalf("%v, %d songs", err, len(got))
	}
	if got[59].Title != "Song 59" || got[0].Album != "Meteora" || got[0].ArtworkURL != album.ArtworkURL || got[0].DurationS != 180 || got[0].VideoID != "" {
		t.Fatalf("songs %+v %+v", got[0], got[59])
	}
	if strings.Join(f.paths, " ") != "/v1/albums/"+albumID+"/tracks?offset=0 /v1/albums/"+albumID+"/tracks?offset=50" {
		t.Fatalf("paths %v", f.paths)
	}
}

func TestPlaylistSongsStopAtTheLimit(t *testing.T) {
	c, f, done := collectionClient(t)
	defer done()
	list := providers.Collection{Source: "spotify", Kind: "playlist", ID: playlistID}
	got, err := c.CollectionSongs(context.Background(), list, 110)
	if err != nil || len(got) != 110 {
		t.Fatalf("%v, %d songs", err, len(got))
	}
	if len(f.paths) != 2 {
		t.Fatalf("paths %v", f.paths)
	}
}

func TestPlaylistSongsTryTheRenamedPath(t *testing.T) {
	c, f, done := collectionClient(t)
	defer done()
	f.status["/v1/playlists/"+playlistID+"/tracks"] = http.StatusNotFound
	got, err := c.CollectionSongs(context.Background(), providers.Collection{Source: "spotify", Kind: "playlist", ID: playlistID}, 5)
	if err != nil || len(got) != 5 || got[0].Title != "Song 0" {
		t.Fatalf("%v %+v", err, got)
	}
}

// spotify keeps its own playlists from apps without a user: a code, never its text
func TestARefusedPlaylistIsTheRefusalAlone(t *testing.T) {
	for _, st := range []int{http.StatusForbidden, http.StatusNotFound} {
		c, f, done := collectionClient(t)
		f.status["/v1/playlists/"+playlistID+"/tracks"] = st
		f.status["/v1/playlists/"+playlistID+"/items"] = st
		_, err := c.CollectionSongs(context.Background(), providers.Collection{Source: "spotify", Kind: "playlist", ID: playlistID}, 5)
		done()
		if !errors.Is(err, providers.ErrMusicRefused) || strings.Contains(err.Error(), probetest.Leak) {
			t.Fatalf("%d: %v", st, err)
		}
	}
	c, f, done := collectionClient(t)
	defer done()
	f.status["/v1/albums/"+albumID+"/tracks"] = http.StatusBadGateway
	_, err := c.CollectionSongs(context.Background(), providers.Collection{Source: "spotify", Kind: "album", ID: albumID}, 5)
	if err == nil || err.Error() != providers.CodeMusicFailed {
		t.Fatalf("err %v", err)
	}
}

func TestArtistSongsAreItsOwnTracks(t *testing.T) {
	c, f, done := collectionClient(t)
	defer done()
	artist := providers.Collection{Source: "spotify", Kind: "artist", ID: artistID, Title: "Linkin Park"}
	got, err := c.CollectionSongs(context.Background(), artist, 200)
	if err != nil {
		t.Fatal(err)
	}
	// two pages of ten, a third of them someone else's
	if len(got) != 14 || got[0].ID != "t0" || got[2].ID != "t3" || len(f.paths) != 2 {
		t.Fatalf("%d songs %v, paths %v", len(got), got, f.paths)
	}
}

func TestCollectionSongsRefuseForeignIDs(t *testing.T) {
	c, f, done := collectionClient(t)
	defer done()
	for _, col := range []providers.Collection{
		{Source: "spotify", Kind: "album", ID: "../me/tracks"},
		{Source: "youtube", Kind: "album", ID: albumID},
		{Source: "spotify", Kind: "song", ID: albumID},
	} {
		if _, err := c.CollectionSongs(context.Background(), col, 5); err == nil || err.Error() != providers.CodeMusicFailed {
			t.Fatalf("%+v: %v", col, err)
		}
	}
	if len(f.paths) != 0 {
		t.Fatalf("paths %v", f.paths)
	}
}
