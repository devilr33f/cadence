package zvuk_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
	"git.shino.boo/devilreef/cadence/server/internal/providers/zvuk"
)

func TestSearchCollections(t *testing.T) {
	f := &fake{ops: map[string]func(map[string]any) string{"search": func(v map[string]any) string {
		switch {
		case v["releases"] == true:
			return `{"data":{"search":{"releases":{"items":[{"id":"590001","title":"Акустический альбом","image":{"src":"/static/a.jpg"},"artists":[{"title":"Король и Шут"}]},{"id":"","title":"none"}]}}}}`
		case v["playlists"] == true:
			return `{"data":{"search":{"playlists":{"items":[{"id":12,"title":"Панк","image":{"src":"http://insecure/x.jpg"}}]}}}}`
		case v["artists"] == true:
			return `{"data":{"search":{"artists":{"items":[{"id":"300","title":"Король и Шут"}]}}}}`
		}
		t.Errorf("search vars %v", v)
		return `{}`
	}}}
	c := start(t, f, zvuk.Config{})
	want := map[string]providers.Collection{
		providers.KindAlbum: {Source: "zvuk", Kind: "album", ID: "590001", Title: "Акустический альбом", Owner: "Король и Шут",
			ArtworkURL: "https://zvuk.com/static/a.jpg?size=600x600", WebURL: "https://zvuk.com/release/590001"},
		providers.KindPlaylist: {Source: "zvuk", Kind: "playlist", ID: "12", Title: "Панк", WebURL: "https://zvuk.com/playlist/12"},
		providers.KindArtist:   {Source: "zvuk", Kind: "artist", ID: "300", Title: "Король и Шут", WebURL: "https://zvuk.com/artist/300"},
	}
	for kind, w := range want {
		got, err := c.SearchCollections(context.Background(), kind, "король", 5)
		if err != nil || len(got) != 1 || got[0] != w {
			t.Fatalf("%s: %+v, %v", kind, got, err)
		}
	}
	if _, err := c.SearchCollections(context.Background(), "podcast", "x", 5); err == nil {
		t.Fatal("took an unknown kind")
	}
}

func trackJSON(id int) string {
	return fmt.Sprintf(`{"id":"%d","title":"t%d","duration":100,"artists":[{"title":"a"}]}`, id, id)
}

func TestCollectionSongs(t *testing.T) {
	f := &fake{ops: map[string]func(map[string]any) string{
		"getReleases": func(map[string]any) string {
			return `{"data":{"getReleases":[{"id":"590001","title":"Альбом","image":{"src":"/static/a.jpg"},"tracks":[` +
				trackJSON(1) + `,` + trackJSON(2) + `,{"id":"","title":"gone"}]}]}}`
		},
		"getPlaylistTracks": func(v map[string]any) string {
			offset := int(v["offset"].(float64))
			if v["id"] != "12" || v["limit"] != float64(50) {
				t.Errorf("playlist vars %v", v)
			}
			n := 50
			if offset > 0 {
				n = 10
			}
			items := make([]string, n)
			for i := range items {
				items[i] = trackJSON(offset + i + 1)
			}
			return `{"data":{"playlistTracks":[` + strings.Join(items, ",") + `]}}`
		},
		"getArtists": func(v map[string]any) string {
			if v["withPopTracks"] != true || v["tracksLimit"] != float64(3) {
				t.Errorf("artist vars %v", v)
			}
			return `{"data":{"getArtists":[{"id":"300","popularTracks":[` + trackJSON(7) + `]}]}}`
		},
	}}
	c := start(t, f, zvuk.Config{})
	album, err := c.CollectionSongs(context.Background(), providers.Collection{Source: "zvuk", Kind: "album", ID: "590001"}, 10)
	if err != nil || len(album) != 2 || album[0].Album != "Альбом" || album[0].ArtworkURL != "https://zvuk.com/static/a.jpg?size=600x600" {
		t.Fatalf("album %+v, %v", album, err)
	}
	list, err := c.CollectionSongs(context.Background(), providers.Collection{Source: "zvuk", Kind: "playlist", ID: "12"}, 100)
	if err != nil || len(list) != 60 || list[59].ID != "60" {
		t.Fatalf("playlist %d, %v", len(list), err)
	}
	short, err := c.CollectionSongs(context.Background(), providers.Collection{Source: "zvuk", Kind: "playlist", ID: "12"}, 20)
	if err != nil || len(short) != 20 {
		t.Fatalf("playlist %d, %v", len(short), err)
	}
	artist, err := c.CollectionSongs(context.Background(), providers.Collection{Source: "zvuk", Kind: "artist", ID: "300"}, 3)
	if err != nil || len(artist) != 1 || artist[0].ID != "7" {
		t.Fatalf("artist %+v, %v", artist, err)
	}
	if _, err := c.CollectionSongs(context.Background(), providers.Collection{Source: "spotify", Kind: "album", ID: "1"}, 3); err == nil {
		t.Fatal("took another source")
	}
}

func TestTrackRadioTakesRelatedReleasesSongsInTurn(t *testing.T) {
	f := &fake{ops: map[string]func(map[string]any) string{
		"getTracks": func(map[string]any) string {
			return `{"data":{"getTracks":[{"id":"5896627","release":{"id":"590001"}}]}}`
		},
		"getReleases": func(v map[string]any) string {
			ids := v["ids"].([]any)
			if len(ids) == 1 && ids[0] == "590001" {
				return `{"data":{"getReleases":[{"id":"590001","title":"own","related":[{"id":"10"},{"id":""},{"id":"20"}]}]}}`
			}
			if len(ids) != 2 || ids[0] != "10" || ids[1] != "20" {
				t.Errorf("related ids %v", ids)
			}
			return `{"data":{"getReleases":[
				{"id":"10","title":"r10","tracks":[` + trackJSON(101) + `,` + trackJSON(102) + `,` + trackJSON(103) + `]},
				{"id":"20","title":"r20","tracks":[{"id":"5896627","title":"seed again"},` + trackJSON(201) + `]}]}}`
		},
	}}
	c := start(t, f, zvuk.Config{})
	got, err := c.TrackRadio(context.Background(), seed, 4)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, tr := range got {
		ids = append(ids, tr.ID)
	}
	if strings.Join(ids, ",") != "101,102,201,103" || got[0].Album != "r10" {
		t.Fatalf("radio %v", ids)
	}
}

func TestCollectionsFailWithTheCodeOnly(t *testing.T) {
	f := &fake{ops: map[string]func(map[string]any) string{
		"getReleases": func(map[string]any) string { return `{"errors":[{"message":"` + probetest.Leak + `"}]}` },
	}}
	c := start(t, f, zvuk.Config{})
	_, err := c.CollectionSongs(context.Background(), providers.Collection{Source: "zvuk", Kind: "album", ID: "1"}, 3)
	if err == nil || err.Error() != providers.CodeMusicFailed {
		t.Fatalf("err %v", err)
	}
}
