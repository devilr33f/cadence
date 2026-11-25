package zvuk

import (
	"context"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	// a playlist's songs are read this many at a time
	playlistPage = 50
	// the most songs read of one list
	maxListSongs = 500
	// an artist's popular songs Zvuk answers at most
	maxArtistSongs = 100
)

type listItem struct {
	ID      zid     `json:"id"`
	Title   string  `json:"title"`
	Image   *image  `json:"image"`
	Artists []named `json:"artists"`
}

type artistItem struct {
	ID    zid    `json:"id"`
	Title string `json:"title"`
	Image *image `json:"image"`
}

// search's result kinds, by collection kind
var searchKinds = map[string]string{
	providers.KindAlbum:    "releases",
	providers.KindPlaylist: "playlists",
	providers.KindArtist:   "artists",
}

// SearchCollections answers up to limit albums, playlists or artists (1 to
// MaxLimit) in Zvuk's order.
func (c *Client) SearchCollections(ctx context.Context, kind, query string, limit int) ([]providers.Collection, error) {
	sk, ok := searchKinds[kind]
	if !ok {
		return nil, errFailed
	}
	limit = min(max(limit, 1), MaxLimit)
	var out []providers.Collection
	if c.guard(func() error {
		var a searchAnswer
		if err := c.search(ctx, query, limit, sk, &a); err != nil {
			return err
		}
		var items []listItem
		switch kind {
		case providers.KindAlbum:
			items = a.Search.Releases.Items
		case providers.KindPlaylist:
			items = a.Search.Playlists.Items
		default:
			for _, it := range a.Search.Artists.Items {
				items = append(items, listItem{ID: it.ID, Title: it.Title, Image: it.Image})
			}
		}
		for _, it := range items {
			if col, ok := collection(kind, it); ok && len(out) < limit {
				out = append(out, col)
			}
		}
		return nil
	}) != nil {
		return nil, errFailed
	}
	return out, nil
}

// web pages, by collection kind
var pageKinds = map[string]string{
	providers.KindAlbum:    "release",
	providers.KindPlaylist: "playlist",
	providers.KindArtist:   "artist",
}

func collection(kind string, it listItem) (providers.Collection, bool) {
	id := string(it.ID)
	if !idRe.MatchString(id) || strings.TrimSpace(it.Title) == "" {
		return providers.Collection{}, false
	}
	col := providers.Collection{
		Source: providers.SourceZvuk, Kind: kind, ID: id, Title: clip(it.Title),
		ArtworkURL: artwork(it.Image), WebURL: page(pageKinds[kind], id),
	}
	if kind == providers.KindAlbum {
		col.Owner = artists(it.Artists)
	}
	return col, true
}

// CollectionSongs answers up to limit songs: an album's or a playlist's in
// order, or an artist's most popular.
func (c *Client) CollectionSongs(ctx context.Context, col providers.Collection, limit int) ([]providers.Track, error) {
	if col.Source != providers.SourceZvuk || !idRe.MatchString(col.ID) || limit < 1 {
		return nil, errFailed
	}
	limit = min(limit, maxListSongs)
	var out []providers.Track
	if c.guard(func() error {
		var err error
		switch col.Kind {
		case providers.KindAlbum:
			var rs []release
			if rs, err = c.releases(ctx, []string{col.ID}, 1); err == nil && len(rs) > 0 {
				out = tracks(rs[0].songs(), limit)
			}
		case providers.KindPlaylist:
			out, err = c.playlistSongs(ctx, col.ID, limit)
		case providers.KindArtist:
			var a struct {
				GetArtists []struct {
					PopularTracks []trackItem `json:"popularTracks"`
				} `json:"getArtists"`
			}
			err = c.graphql(ctx, "getArtists", artistsQuery, map[string]any{
				"ids": []string{col.ID}, "withPopTracks": true, "tracksLimit": min(limit, maxArtistSongs),
			}, &a)
			if err == nil && len(a.GetArtists) > 0 {
				out = tracks(a.GetArtists[0].PopularTracks, limit)
			}
		default:
			return errFailed
		}
		return err
	}) != nil {
		return nil, errFailed
	}
	return out, nil
}

func (c *Client) playlistSongs(ctx context.Context, id string, limit int) ([]providers.Track, error) {
	var out []providers.Track
	for offset := 0; len(out) < limit && offset < maxListSongs; offset += playlistPage {
		var a struct {
			PlaylistTracks []trackItem `json:"playlistTracks"`
		}
		if err := c.graphql(ctx, "getPlaylistTracks", playlistTracksQuery, map[string]any{
			"id": id, "limit": playlistPage, "offset": offset,
		}, &a); err != nil {
			return nil, err
		}
		out = append(out, tracks(a.PlaylistTracks, limit-len(out))...)
		if len(a.PlaylistTracks) < playlistPage {
			break
		}
	}
	return out, nil
}

type release struct {
	ID      zid         `json:"id"`
	Title   string      `json:"title"`
	Image   *image      `json:"image"`
	Tracks  []trackItem `json:"tracks"`
	Related []struct {
		ID zid `json:"id"`
	} `json:"related"`
}

// songs are the release's tracks, each named with the release where Zvuk
// leaves it out
func (r release) songs() []trackItem {
	out := make([]trackItem, len(r.Tracks))
	for i, t := range r.Tracks {
		if t.Release == nil {
			t.Release = &releaseRef{ID: r.ID, Title: r.Title, Image: r.Image}
		}
		out[i] = t
	}
	return out
}

// releases reads releases by id, empty ones (ids Zvuk does not know) left out
func (c *Client) releases(ctx context.Context, ids []string, related int) ([]release, error) {
	var a struct {
		GetReleases []release `json:"getReleases"`
	}
	if err := c.graphql(ctx, "getReleases", releasesQuery, map[string]any{"ids": ids, "relatedLimit": related}, &a); err != nil {
		return nil, err
	}
	out := a.GetReleases[:0]
	for _, r := range a.GetReleases {
		if idRe.MatchString(string(r.ID)) {
			out = append(out, r)
		}
	}
	return out, nil
}
