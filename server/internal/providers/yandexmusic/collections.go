package yandexmusic

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	// a playlist names some songs by id alone, read this many at a time
	idsBatch = 100
	// the most songs read of one list
	maxListSongs = 500
)

// a playlist is "<owner uid>:<kind>"
var (
	playlistIDRe = regexp.MustCompile(`^([0-9]{1,20}):([0-9]{1,10})$`)
	loginRe      = regexp.MustCompile(`^[0-9A-Za-z._\-]{1,64}$`)
)

type playlistItem struct {
	UID        flexID `json:"uid"`
	Kind       flexID `json:"kind"`
	Title      string `json:"title"`
	TrackCount int    `json:"trackCount"`
	Owner      struct {
		Login string `json:"login"`
		Name  string `json:"name"`
	} `json:"owner"`
	Cover *struct {
		URI      string   `json:"uri"`
		ItemsURI []string `json:"itemsUri"`
	} `json:"cover"`
	OgImage string `json:"ogImage"`
}

var searchTypes = map[string]string{
	providers.KindAlbum:    "album",
	providers.KindPlaylist: "playlist",
	providers.KindArtist:   "artist",
}

// SearchCollections answers up to limit albums, playlists, artists or the
// account's personal station (1 to MaxLimit) in Yandex's order.
func (c *Client) SearchCollections(ctx context.Context, kind, query string, limit int) ([]providers.Collection, error) {
	if kind == providers.KindStation {
		return []providers.Collection{myWave()}, nil
	}
	typ, ok := searchTypes[kind]
	if !ok {
		return nil, errFailed
	}
	limit = min(max(limit, 1), MaxLimit)
	var out []providers.Collection
	if c.guard(func() error {
		var r struct {
			Albums struct {
				Results []albumItem `json:"results"`
			} `json:"albums"`
			Playlists struct {
				Results []playlistItem `json:"results"`
			} `json:"playlists"`
			Artists struct {
				Results []artistItem `json:"results"`
			} `json:"artists"`
		}
		if err := c.get(ctx, "/search", searchQuery(query, typ), false, &r); err != nil {
			return err
		}
		add := func(col providers.Collection, ok bool) {
			if ok && len(out) < limit {
				out = append(out, col)
			}
		}
		for _, a := range r.Albums.Results {
			add(album(a))
		}
		for _, p := range r.Playlists.Results {
			add(playlist(p))
		}
		for _, a := range r.Artists.Results {
			add(artist(a))
		}
		return nil
	}) != nil {
		return nil, errFailed
	}
	return out, nil
}

func album(a albumItem) (providers.Collection, bool) {
	id := string(a.ID)
	if !idRe.MatchString(id) || strings.TrimSpace(a.Title) == "" || (a.Available != nil && !*a.Available) {
		return providers.Collection{}, false
	}
	return providers.Collection{
		Source: providers.SourceYandex, Kind: providers.KindAlbum, ID: id, Title: clip(titled(a.Title, a.Version)),
		Owner: clip(names(a.Artists)), Tracks: max(a.TrackCount, 0), ArtworkURL: cover(a.CoverURI),
		WebURL: webBase + "/album/" + id,
	}, true
}

func playlist(p playlistItem) (providers.Collection, bool) {
	id := string(p.UID) + ":" + string(p.Kind)
	if !playlistIDRe.MatchString(id) || strings.TrimSpace(p.Title) == "" {
		return providers.Collection{}, false
	}
	owner := p.Owner.Name
	if strings.TrimSpace(owner) == "" {
		owner = p.Owner.Login
	}
	col := providers.Collection{
		Source: providers.SourceYandex, Kind: providers.KindPlaylist, ID: id, Title: clip(p.Title),
		Owner: clip(owner), Tracks: max(p.TrackCount, 0), ArtworkURL: cover(p.OgImage),
	}
	if p.Cover != nil {
		if a := cover(p.Cover.URI); a != "" {
			col.ArtworkURL = a
		} else if len(p.Cover.ItemsURI) > 0 && col.ArtworkURL == "" {
			col.ArtworkURL = cover(p.Cover.ItemsURI[0])
		}
	}
	if loginRe.MatchString(p.Owner.Login) {
		col.WebURL = webBase + "/users/" + url.PathEscape(p.Owner.Login) + "/playlists/" + string(p.Kind)
	}
	return col, true
}

func artist(a artistItem) (providers.Collection, bool) {
	id := string(a.ID)
	if !idRe.MatchString(id) || strings.TrimSpace(a.Name) == "" {
		return providers.Collection{}, false
	}
	col := providers.Collection{
		Source: providers.SourceYandex, Kind: providers.KindArtist, ID: id, Title: clip(a.Name),
		WebURL: webBase + "/artist/" + id,
	}
	if a.Cover != nil {
		col.ArtworkURL = cover(a.Cover.URI)
	}
	return col, true
}

// CollectionSongs answers up to limit songs: an album's or a playlist's in
// order, an artist's most popular, or My Wave's next batch.
func (c *Client) CollectionSongs(ctx context.Context, col providers.Collection, limit int) ([]providers.Track, error) {
	if col.Source != providers.SourceYandex || limit < 1 {
		return nil, errFailed
	}
	if col.Kind == providers.KindStation {
		return c.stationSongs(ctx, col.ID, "", limit)
	}
	limit = min(limit, maxListSongs)
	var out []providers.Track
	if c.guard(func() error {
		var err error
		switch {
		case col.Kind == providers.KindAlbum && idRe.MatchString(col.ID):
			var r struct {
				Volumes [][]trackItem `json:"volumes"`
			}
			err = c.get(ctx, "/albums/"+col.ID+"/with-tracks", nil, false, &r)
			var all []trackItem
			for _, v := range r.Volumes {
				all = append(all, v...)
			}
			out = tracks(all, limit)
		case col.Kind == providers.KindPlaylist && playlistIDRe.MatchString(col.ID):
			out, err = c.playlistSongs(ctx, col.ID, limit)
		case col.Kind == providers.KindArtist && idRe.MatchString(col.ID):
			var r struct {
				Tracks []trackItem `json:"tracks"`
			}
			q := url.Values{"page": {"0"}, "page-size": {strconv.Itoa(min(limit, 50))}}
			err = c.get(ctx, "/artists/"+col.ID+"/tracks", q, false, &r)
			out = tracks(r.Tracks, limit)
		default:
			return errFailed
		}
		return err
	}) != nil {
		return nil, errFailed
	}
	return out, nil
}

type playlistEntry struct {
	ID      flexID     `json:"id"`
	AlbumID flexID     `json:"albumId"`
	Track   *trackItem `json:"track"`
}

// playlistSongs reads a playlist's songs in order: those it names whole,
// then the rest by their ids
func (c *Client) playlistSongs(ctx context.Context, id string, limit int) ([]providers.Track, error) {
	m := playlistIDRe.FindStringSubmatch(id)
	var p struct {
		Tracks []playlistEntry `json:"tracks"`
	}
	if err := c.get(ctx, "/users/"+m[1]+"/playlists/"+m[2], nil, false, &p); err != nil {
		return nil, err
	}
	// a few more than asked: unavailable songs are left out
	entries := p.Tracks[:min(len(p.Tracks), limit+limit/4+5)]
	whole := map[string]trackItem{}
	var missing []string
	for _, e := range entries {
		switch {
		case e.Track != nil:
			whole[string(e.ID)] = *e.Track
		case idRe.MatchString(string(e.ID)) && idRe.MatchString(string(e.AlbumID)):
			missing = append(missing, string(e.ID)+":"+string(e.AlbumID))
		case idRe.MatchString(string(e.ID)):
			missing = append(missing, string(e.ID))
		}
	}
	for len(missing) > 0 {
		n := min(len(missing), idsBatch)
		form := url.Values{"track-ids": {strings.Join(missing[:n], ",")}, "with-positions": {"true"}}
		missing = missing[n:]
		var got []trackItem
		if err := c.call(ctx, http.MethodPost, "/tracks", nil, form, false, &got); err != nil {
			return nil, err
		}
		for _, it := range got {
			whole[string(it.ID)] = it
		}
	}
	ordered := make([]trackItem, 0, len(entries))
	for _, e := range entries {
		if w, ok := whole[string(e.ID)]; ok {
			ordered = append(ordered, w)
		}
	}
	return tracks(ordered, limit), nil
}

// TrackRadio answers up to limit songs of the seed's own station. A seed
// from My Wave keeps that personal station, including after a YouTube match.
func (c *Client) TrackRadio(ctx context.Context, seed providers.Track, limit int) ([]providers.Track, error) {
	if seed.Source != providers.SourceYandex || !idRe.MatchString(seed.ID) || limit < 1 {
		return nil, errFailed
	}
	if seed.Station != "" {
		return c.stationSongs(ctx, seed.Station, seed.ID, limit)
	}
	var out []providers.Track
	if c.guard(func() error {
		var r struct {
			Sequence []struct {
				Track trackItem `json:"track"`
			} `json:"sequence"`
			BatchID string `json:"batchId"`
		}
		if err := c.get(ctx, "/rotor/station/track:"+seed.ID+"/tracks", url.Values{"settings2": {"true"}}, false, &r); err != nil {
			return err
		}
		if len(r.BatchID) > 200 || strings.ContainsAny(r.BatchID, "\x00\r\n") {
			return errBadAnswer
		}
		items := make([]trackItem, 0, len(r.Sequence))
		for _, s := range r.Sequence {
			if string(s.Track.ID) != seed.ID {
				items = append(items, s.Track)
			}
		}
		out = tracks(items, limit)
		for i := range out {
			out[i].Station = "track:" + seed.ID
			out[i].StationBatch = r.BatchID
		}
		return nil
	}) != nil {
		return nil, errFailed
	}
	return out, nil
}
