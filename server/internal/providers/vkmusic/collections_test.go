package vkmusic_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
)

func TestSearchPlaylistsAndAlbums(t *testing.T) {
	c, f := newClient(t, map[string]string{
		"audio.searchPlaylists": `{"response":{"items":[
			{"id":10,"owner_id":-1,"access_key":"ab","title":"Rock","count":42,"photo":{"photo_600":"https://sun.userapi.com/p600"}},
			{"id":0,"owner_id":-1,"title":"no id"}]}}`,
		"audio.searchAlbums": `{"response":{"items":[
			{"id":11,"owner_id":-2,"title":"Meteora","count":13,"thumbs":[{"photo_300":"https://sun.userapi.com/t300"}],"main_artists":[{"name":"Linkin Park"}]}]}}`,
	})
	got, err := c.SearchCollections(context.Background(), providers.KindPlaylist, "rock", 5)
	if err != nil {
		t.Fatal(err)
	}
	want := providers.Collection{Source: "vk", Kind: "playlist", ID: "-1_10_ab", Title: "Rock", Tracks: 42,
		ArtworkURL: "https://sun.userapi.com/p600", WebURL: "https://vk.com/music/playlist/-1_10_ab"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %+v", got)
	}
	got, err = c.SearchCollections(context.Background(), providers.KindAlbum, "meteora", 5)
	if err != nil || len(got) != 1 || got[0].Owner != "Linkin Park" || got[0].ArtworkURL != "https://sun.userapi.com/t300" || got[0].ID != "-2_11" {
		t.Fatalf("got %+v %v", got, err)
	}
	if f.queries["audio.searchAlbums"]["q"] != "meteora" {
		t.Fatalf("query %v", f.queries["audio.searchAlbums"])
	}
}

func TestSearchArtistsAndTheirSongs(t *testing.T) {
	c, f := newClient(t, map[string]string{
		"audio.searchArtists":     `{"response":{"items":[{"id":"6087213062574437613","name":"Linkin Park","domain":"linkinpark"},{"id":"bad id","name":"x"}]}}`,
		"audio.getAudiosByArtist": `{"response":{"items":[{"id":7,"owner_id":8,"title":"Faint","artist":"LP","url":"https://a.vkuseraudio.net/y.mp3"}]}}`,
	})
	got, err := c.SearchCollections(context.Background(), providers.KindArtist, "linkin", 5)
	if err != nil || len(got) != 1 || got[0].ID != "6087213062574437613" || got[0].WebURL != "https://vk.com/artist/linkinpark" {
		t.Fatalf("got %+v %v", got, err)
	}
	songs, err := c.CollectionSongs(context.Background(), got[0], 10)
	if err != nil || len(songs) != 1 || songs[0].ID != "8_7" {
		t.Fatalf("songs %+v %v", songs, err)
	}
	if q := f.queries["audio.getAudiosByArtist"]; q["artist_id"] != "6087213062574437613" || q["count"] != "10" {
		t.Fatalf("query %v", q)
	}
}

func page(from, n int) string {
	items := make([]string, n)
	for i := range n {
		items[i] = fmt.Sprintf(`{"id":%d,"owner_id":1,"title":"s%d","url":"https://a.vkuseraudio.net/%d.mp3"}`, from+i+1, from+i, from+i)
	}
	return `{"response":{"count":300,"items":[` + strings.Join(items, ",") + `]}}`
}

func TestPlaylistSongsReadPages(t *testing.T) {
	c, f := newClient(t, map[string]string{"audio.get@0": page(0, 200), "audio.get@200": page(200, 100)})
	songs, err := c.CollectionSongs(context.Background(), providers.Collection{Source: "vk", Kind: providers.KindPlaylist, ID: "-1_10_ab"}, 250)
	if err != nil || len(songs) != 250 || songs[249].ID != "1_250" {
		t.Fatalf("got %d %v", len(songs), err)
	}
	if q := f.queries["audio.get"]; q["owner_id"] != "-1" || q["album_id"] != "10" || q["access_key"] != "ab" || q["offset"] != "200" {
		t.Fatalf("query %v", q)
	}
}

func TestAPrivatePlaylistIsRefused(t *testing.T) {
	c, _ := newClient(t, map[string]string{"audio.get": vkError(201)})
	_, err := c.CollectionSongs(context.Background(), providers.Collection{Source: "vk", Kind: providers.KindPlaylist, ID: "-1_10"}, 5)
	if !errors.Is(err, providers.ErrMusicRefused) || strings.Contains(err.Error(), probetest.Leak) {
		t.Fatalf("error %v", err)
	}
}

func TestCollectionSongsRefuseForeignIDs(t *testing.T) {
	c, f := newClient(t, map[string]string{})
	for _, col := range []providers.Collection{
		{Source: "vk", Kind: providers.KindPlaylist, ID: "1_2&owner_id=3"},
		{Source: "vk", Kind: providers.KindArtist, ID: "a/b"},
		{Source: "spotify", Kind: providers.KindPlaylist, ID: "1_2"},
	} {
		if _, err := c.CollectionSongs(context.Background(), col, 5); err == nil || err.Error() != providers.CodeMusicFailed {
			t.Fatalf("%q: %v", col.ID, err)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("calls %v", f.calls)
	}
}
