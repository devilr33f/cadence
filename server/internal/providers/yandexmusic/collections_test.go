package yandexmusic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func song(id, album string) string {
	return `{"id":` + id + `,"title":"Song ` + id + `","durationMs":1000,"artists":[{"name":"A"}],"albums":[{"id":` + album + `,"title":"Al"}]}`
}

func collectionsFake(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/search":
			switch q.Get("type") {
			case "album":
				_, _ = io.WriteString(w, `{"result":{"albums":{"results":[
					{"id":21940,"title":"Meteora","version":"Bonus Edition","trackCount":16,
					 "artists":[{"name":"Linkin Park"}],"coverUri":"avatars.yandex.net/get-music-content/1/a/%%"},
					{"id":"x","title":"bad id"}]}}}`)
			case "playlist":
				_, _ = io.WriteString(w, `{"result":{"playlists":{"results":[
					{"uid":103372440,"kind":2131,"title":"Вечный рок","trackCount":108,
					 "owner":{"login":"music-blog","name":"Яндекс Музыка"},
					 "cover":{"uri":"avatars.yandex.net/get-music-user-playlist/1/p/%%?17"}}]}}}`)
			case "artist":
				_, _ = io.WriteString(w, `{"result":{"artists":{"results":[
					{"id":36800,"name":"Linkin Park","cover":{"uri":"avatars.yandex.net/get-music-content/1/p/%%"}}]}}}`)
			default:
				t.Errorf("search type %q", q.Get("type"))
			}
		case "/albums/21940/with-tracks":
			_, _ = io.WriteString(w, `{"result":{"id":21940,"volumes":[[`+song("1", "21940")+`,`+song("2", "21940")+`],[`+song("3", "21940")+`]]}}`)
		case "/users/103372440/playlists/2131":
			_, _ = io.WriteString(w, `{"result":{"tracks":[
				{"id":10,"track":`+song("10", "5")+`},{"id":11,"albumId":6},{"id":"12"},{"id":13,"track":`+song("13", "5")+`}]}}`)
		case "/tracks":
			_ = r.ParseForm()
			if r.Method != http.MethodPost || r.PostForm.Get("track-ids") != "11:6,12" {
				t.Errorf("tracks %s %v", r.Method, r.PostForm)
			}
			_, _ = io.WriteString(w, `{"result":[`+song("12", "7")+`,`+song("11", "6")+`]}`)
		case "/artists/36800/tracks":
			if q.Get("page-size") != "2" {
				t.Errorf("artist %s", r.URL)
			}
			_, _ = io.WriteString(w, `{"result":{"pager":{},"tracks":[`+song("20", "1")+`,`+song("21", "1")+`]}}`)
		case "/rotor/station/track:178529/tracks":
			_, _ = io.WriteString(w, `{"result":{"batchId":"batch-track","sequence":[{"track":`+song("178529", "1")+`},{"track":`+song("30", "1")+`},{"track":`+song("31", "1")+`}]}}`)
		default:
			t.Errorf("path %s", r.URL.Path)
		}
	}
}

func TestSearchCollections(t *testing.T) {
	srv := httptest.NewTLSServer(collectionsFake(t))
	defer srv.Close()
	c := newClient(srv, Config{})
	want := map[string]providers.Collection{
		providers.KindAlbum: {Source: "yandex", Kind: "album", ID: "21940", Title: "Meteora (Bonus Edition)", Owner: "Linkin Park",
			Tracks: 16, ArtworkURL: "https://avatars.yandex.net/get-music-content/1/a/400x400", WebURL: "https://music.yandex.ru/album/21940"},
		providers.KindPlaylist: {Source: "yandex", Kind: "playlist", ID: "103372440:2131", Title: "Вечный рок", Owner: "Яндекс Музыка",
			Tracks: 108, ArtworkURL: "https://avatars.yandex.net/get-music-user-playlist/1/p/400x400?17",
			WebURL: "https://music.yandex.ru/users/music-blog/playlists/2131"},
		providers.KindArtist: {Source: "yandex", Kind: "artist", ID: "36800", Title: "Linkin Park",
			ArtworkURL: "https://avatars.yandex.net/get-music-content/1/p/400x400", WebURL: "https://music.yandex.ru/artist/36800"},
	}
	for kind, w := range want {
		got, err := c.SearchCollections(context.Background(), kind, "q", 5)
		if err != nil || len(got) != 1 || got[0] != w {
			t.Fatalf("%s: %+v %v", kind, got, err)
		}
	}
	if _, err := c.SearchCollections(context.Background(), "video", "q", 5); err == nil {
		t.Fatal("unknown kind searched")
	}
}

func ids(ts []providers.Track) string {
	s := ""
	for _, t := range ts {
		s += t.ID + ","
	}
	return s
}

func TestCollectionSongs(t *testing.T) {
	srv := httptest.NewTLSServer(collectionsFake(t))
	defer srv.Close()
	c := newClient(srv, Config{})
	cases := []struct {
		col   providers.Collection
		limit int
		want  string
	}{
		{providers.Collection{Source: "yandex", Kind: "album", ID: "21940"}, 10, "1,2,3,"},
		{providers.Collection{Source: "yandex", Kind: "playlist", ID: "103372440:2131"}, 10, "10,11,12,13,"},
		{providers.Collection{Source: "yandex", Kind: "artist", ID: "36800"}, 2, "20,21,"},
	}
	for _, tc := range cases {
		got, err := c.CollectionSongs(context.Background(), tc.col, tc.limit)
		if err != nil || ids(got) != tc.want {
			t.Fatalf("%s: %s %v", tc.col.Kind, ids(got), err)
		}
	}
	for _, bad := range []providers.Collection{
		{Source: "spotify", Kind: "album", ID: "21940"},
		{Source: "yandex", Kind: "playlist", ID: "103372440/2131"},
		{Source: "yandex", Kind: "album", ID: "../x"},
	} {
		if _, err := c.CollectionSongs(context.Background(), bad, 5); err == nil {
			t.Fatalf("%+v read", bad)
		}
	}
}

func TestTrackRadioLeavesTheSeedOut(t *testing.T) {
	srv := httptest.NewTLSServer(collectionsFake(t))
	defer srv.Close()
	got, err := newClient(srv, Config{}).TrackRadio(context.Background(), providers.Track{Source: "yandex", ID: "178529"}, 5)
	if err != nil || ids(got) != "30,31," {
		t.Fatalf("%s %v", ids(got), err)
	}
	if got[0].Station != "track:178529" || got[0].StationBatch != "batch-track" {
		t.Fatalf("track radio station %+v", got[0])
	}
}
