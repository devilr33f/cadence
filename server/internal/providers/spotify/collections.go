package spotify

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	albumPage    = 50
	playlistPage = 100
	// an artist's songs are its tracks the search finds, two pages of them
	artistPages = 2
	maxPages    = 5
)

var idRe = regexp.MustCompile(`^[0-9A-Za-z]{22}$`)

func validID(id string) bool { return idRe.MatchString(id) }

type named struct {
	Name string `json:"name"`
}

type collectionsAnswer struct {
	Albums struct {
		Items []*struct {
			ID          string  `json:"id"`
			Name        string  `json:"name"`
			Artists     []named `json:"artists"`
			TotalTracks int     `json:"total_tracks"`
			Images      []image `json:"images"`
		} `json:"items"`
	} `json:"albums"`
	Playlists struct {
		Items []*struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Owner struct {
				DisplayName string `json:"display_name"`
			} `json:"owner"`
			// the track count is "tracks" until Spotify's 2026 rename to "items"
			Tracks struct {
				Total int `json:"total"`
			} `json:"tracks"`
			Items struct {
				Total int `json:"total"`
			} `json:"items"`
			Images []image `json:"images"`
		} `json:"items"`
	} `json:"playlists"`
	Artists struct {
		Items []*struct {
			ID     string  `json:"id"`
			Name   string  `json:"name"`
			Images []image `json:"images"`
		} `json:"items"`
	} `json:"artists"`
}

// SearchCollections answers up to limit albums, playlists or artists (1 to
// MaxLimit) in Spotify's order.
func (c *Client) SearchCollections(ctx context.Context, kind, query string, limit int) ([]providers.Collection, error) {
	var out []providers.Collection
	var err error
	switch kind {
	case providers.KindAlbum, providers.KindPlaylist, providers.KindArtist:
	default:
		return nil, errFailed
	}
	if providers.Guard(c.cfg.Log, "spotify", providers.CodeMusicFailed, func() { out, err = c.searchCollections(ctx, kind, query, limit) }) || err != nil {
		return nil, errFailed
	}
	return out, nil
}

func (c *Client) searchCollections(ctx context.Context, kind, query string, limit int) ([]providers.Collection, error) {
	limit = min(max(limit, 1), MaxLimit)
	var a collectionsAnswer
	if err := c.get(ctx, searchPath, url.Values{"q": {query}, "type": {kind}, "limit": {strconv.Itoa(limit)}}, &a); err != nil {
		return nil, err
	}
	var out []providers.Collection
	add := func(id, title, owner string, tracks int, images []image) {
		if !validID(id) || strings.TrimSpace(title) == "" || len(out) == limit {
			return
		}
		out = append(out, providers.Collection{
			Source: providers.SourceSpotify, Kind: kind, ID: id, Title: clip(title), Owner: clip(owner),
			Tracks: max(tracks, 0), ArtworkURL: largest(images), WebURL: "https://open.spotify.com/" + kind + "/" + id,
		})
	}
	// spotify answers null for an item it cannot show
	for _, it := range a.Albums.Items {
		if it != nil {
			add(it.ID, it.Name, names(it.Artists), it.TotalTracks, it.Images)
		}
	}
	for _, it := range a.Playlists.Items {
		if it != nil {
			add(it.ID, it.Name, it.Owner.DisplayName, max(it.Tracks.Total, it.Items.Total), it.Images)
		}
	}
	for _, it := range a.Artists.Items {
		if it != nil {
			add(it.ID, it.Name, "", 0, it.Images)
		}
	}
	return out, nil
}

func names(ns []named) string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		if s := strings.TrimSpace(n.Name); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, ", ")
}

// CollectionSongs answers up to limit songs: an album's or a playlist's in
// order, or the artist's tracks the search ranks first. A list Spotify keeps
// from the app (403 or 404: its own editorial playlists) is ErrMusicRefused.
func (c *Client) CollectionSongs(ctx context.Context, col providers.Collection, limit int) ([]providers.Track, error) {
	var out []providers.Track
	var err error
	if col.Source != providers.SourceSpotify || !validID(col.ID) || limit < 1 {
		return nil, errFailed
	}
	if providers.Guard(c.cfg.Log, "spotify", providers.CodeMusicFailed, func() {
		switch col.Kind {
		case providers.KindAlbum:
			out, err = c.albumSongs(ctx, col, limit)
		case providers.KindPlaylist:
			out, err = c.playlistSongs(ctx, col.ID, limit)
		case providers.KindArtist:
			out, err = c.artistSongs(ctx, col, limit)
		default:
			err = errFailed
		}
	}) {
		return nil, errFailed
	}
	switch {
	case refused(err):
		return nil, providers.ErrMusicRefused
	case err != nil || len(out) == 0:
		return nil, errFailed
	}
	return out, nil
}

func refused(err error) bool {
	return errors.Is(err, statusError(http.StatusForbidden)) || errors.Is(err, statusError(http.StatusNotFound))
}

type page[T any] struct {
	Items []T    `json:"items"`
	Next  string `json:"next"`
}

// pages reads a list's pages until it has limit songs or it ends
func pages[T any](ctx context.Context, c *Client, path string, size, limit int, read func(T) (providers.Track, bool)) ([]providers.Track, error) {
	var out []providers.Track
	for n := 0; n < maxPages && len(out) < limit; n++ {
		var p page[T]
		q := url.Values{"limit": {strconv.Itoa(size)}, "offset": {strconv.Itoa(n * size)}}
		if err := c.get(ctx, path, q, &p); err != nil {
			return nil, err
		}
		for _, it := range p.Items {
			if t, ok := read(it); ok && len(out) < limit {
				out = append(out, t)
			}
		}
		if p.Next == "" || len(p.Items) < size {
			break
		}
	}
	return out, nil
}

// an album's tracks leave its name and cover out
func (c *Client) albumSongs(ctx context.Context, col providers.Collection, limit int) ([]providers.Track, error) {
	return pages(ctx, c, "/v1/albums/"+col.ID+"/tracks", albumPage, limit, func(it trackItem) (providers.Track, bool) {
		t, ok := track(it)
		t.Album, t.ArtworkURL = col.Title, col.ArtworkURL
		return t, ok
	})
}

type playlistEntry struct {
	// "track" until Spotify's 2026 rename to "item"; null for a removed one
	Track *trackItem `json:"track"`
	Item  *trackItem `json:"item"`
}

// playlistSongs reads /tracks, or /items where Spotify renamed it
func (c *Client) playlistSongs(ctx context.Context, id string, limit int) ([]providers.Track, error) {
	read := func(e playlistEntry) (providers.Track, bool) {
		it := e.Track
		if it == nil {
			it = e.Item
		}
		if it == nil {
			return providers.Track{}, false
		}
		return track(*it)
	}
	out, err := pages(ctx, c, "/v1/playlists/"+id+"/tracks", playlistPage, limit, read)
	if errors.Is(err, statusError(http.StatusNotFound)) {
		return pages(ctx, c, "/v1/playlists/"+id+"/items", playlistPage, limit, read)
	}
	return out, err
}

// artistSongs are the artist's own tracks a search of its name ranks first:
// the search needs no endpoint Spotify keeps from new apps
func (c *Client) artistSongs(ctx context.Context, col providers.Collection, limit int) ([]providers.Track, error) {
	var out []providers.Track
	q := `artist:"` + strings.ReplaceAll(col.Title, `"`, "") + `"`
	for n := range artistPages {
		var a searchAnswer
		err := c.get(ctx, searchPath, url.Values{"q": {q}, "type": {"track"}, "limit": {strconv.Itoa(MaxLimit)}, "offset": {strconv.Itoa(n * MaxLimit)}}, &a)
		if err != nil {
			return nil, err
		}
		for _, it := range a.Tracks.Items {
			if t, ok := track(it); ok && byArtist(it, col.ID) && len(out) < limit {
				out = append(out, t)
			}
		}
		if len(a.Tracks.Items) < MaxLimit || len(out) >= limit {
			break
		}
	}
	return out, nil
}

func byArtist(it trackItem, id string) bool {
	for _, a := range it.Artists {
		if a.ID == id {
			return true
		}
	}
	return false
}
