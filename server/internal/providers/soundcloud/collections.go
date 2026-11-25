package soundcloud

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	// a playlist names its first songs whole and the rest by id alone, read this many at a time
	idsBatch = 50
	// the most songs read of one list
	maxListSongs = 500
)

type listItem struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Username   string `json:"username"`
	Permalink  string `json:"permalink_url"`
	Artwork    string `json:"artwork_url"`
	Avatar     string `json:"avatar_url"`
	TrackCount int    `json:"track_count"`
	User       struct {
		Username string `json:"username"`
		Avatar   string `json:"avatar_url"`
	} `json:"user"`
	Tracks []trackItem `json:"tracks"`
}

var searchPaths = map[string]string{
	providers.KindAlbum:    "/search/albums",
	providers.KindPlaylist: "/search/playlists_without_albums",
	providers.KindArtist:   "/search/users",
}

// SearchCollections answers up to limit albums, playlists or artists (1 to
// MaxLimit) in SoundCloud's order.
func (c *Client) SearchCollections(ctx context.Context, kind, query string, limit int) ([]providers.Collection, error) {
	path, ok := searchPaths[kind]
	if !ok {
		return nil, errFailed
	}
	limit = min(max(limit, 1), MaxLimit)
	var out []providers.Collection
	if c.guard(func() error {
		var a struct {
			Collection []listItem `json:"collection"`
		}
		if err := c.get(ctx, path, url.Values{"q": {query}, "limit": {strconv.Itoa(limit)}}, &a); err != nil {
			return err
		}
		for _, it := range a.Collection {
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

func collection(kind string, it listItem) (providers.Collection, bool) {
	if it.ID <= 0 {
		return providers.Collection{}, false
	}
	col := providers.Collection{
		Source: providers.SourceSoundCloud, Kind: kind, ID: strconv.FormatInt(it.ID, 10), WebURL: page(it.Permalink),
	}
	if kind == providers.KindArtist {
		col.Title, col.ArtworkURL = clip(it.Username), artwork(it.Avatar)
	} else {
		col.Title, col.Owner, col.Tracks = clip(it.Title), clip(it.User.Username), max(it.TrackCount, 0)
		col.ArtworkURL = artwork(it.Artwork, it.User.Avatar)
	}
	return col, strings.TrimSpace(col.Title) != ""
}

// CollectionSongs answers up to limit songs: an album's or a playlist's in
// order, or an artist's most played.
func (c *Client) CollectionSongs(ctx context.Context, col providers.Collection, limit int) ([]providers.Track, error) {
	if col.Source != providers.SourceSoundCloud || !idRe.MatchString(col.ID) || limit < 1 {
		return nil, errFailed
	}
	limit = min(limit, maxListSongs)
	var out []providers.Track
	if c.guard(func() error {
		var err error
		switch col.Kind {
		case providers.KindAlbum, providers.KindPlaylist:
			out, err = c.listSongs(ctx, col.ID, limit)
		case providers.KindArtist:
			var a struct {
				Collection []trackItem `json:"collection"`
			}
			err = c.get(ctx, "/users/"+col.ID+"/toptracks", url.Values{"limit": {strconv.Itoa(min(limit, 50))}}, &a)
			out = tracks(a.Collection, limit)
		default:
			return errFailed
		}
		return err
	}) != nil {
		return nil, errFailed
	}
	return out, nil
}

// listSongs reads a playlist's songs in order: those it names whole, then
// the rest by their ids
func (c *Client) listSongs(ctx context.Context, id string, limit int) ([]providers.Track, error) {
	var l listItem
	if err := c.get(ctx, "/playlists/"+id, url.Values{}, &l); err != nil {
		return nil, err
	}
	whole := map[int64]trackItem{}
	var missing []int64
	for _, it := range l.Tracks[:min(len(l.Tracks), limit)] {
		if it.Title != "" {
			whole[it.ID] = it
		} else if it.ID > 0 {
			missing = append(missing, it.ID)
		}
	}
	for len(missing) > 0 {
		n := min(len(missing), idsBatch)
		ids := make([]string, n)
		for i, m := range missing[:n] {
			ids[i] = strconv.FormatInt(m, 10)
		}
		missing = missing[n:]
		var got []trackItem
		if err := c.get(ctx, "/tracks", url.Values{"ids": {strings.Join(ids, ",")}}, &got); err != nil {
			return nil, err
		}
		for _, it := range got {
			whole[it.ID] = it
		}
	}
	ordered := make([]trackItem, 0, len(l.Tracks))
	for _, it := range l.Tracks {
		if w, ok := whole[it.ID]; ok {
			ordered = append(ordered, w)
		}
	}
	return tracks(ordered, limit), nil
}
