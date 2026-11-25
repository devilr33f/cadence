package vkmusic

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	// audio.get answers at most this many songs a call
	pageSize = 200
	// the most songs read of one list
	maxListSongs = 500
	// audio.getAudiosByArtist and getRecommendations answer at most this many
	maxArtistSongs = 100
)

type listItem struct {
	ID          int64   `json:"id"`
	OwnerID     int64   `json:"owner_id"`
	AccessKey   string  `json:"access_key"`
	Title       string  `json:"title"`
	Count       int     `json:"count"`
	Photo       *photo  `json:"photo"`
	Thumbs      []photo `json:"thumbs"`
	MainArtists []struct {
		Name string `json:"name"`
	} `json:"main_artists"`
}

type artistItem struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Domain string `json:"domain"`
}

var (
	artistIDRe = regexp.MustCompile(`^[0-9A-Za-z_.-]{1,64}$`)
	domainRe   = regexp.MustCompile(`^[0-9A-Za-z_.]{1,64}$`)
)

var searchMethods = map[string]string{
	providers.KindAlbum:    "audio.searchAlbums",
	providers.KindPlaylist: "audio.searchPlaylists",
	providers.KindArtist:   "audio.searchArtists",
}

// SearchCollections answers up to limit albums, playlists or artists (1 to
// MaxLimit) in VK's order.
func (c *Client) SearchCollections(ctx context.Context, kind, query string, limit int) ([]providers.Collection, error) {
	method, ok := searchMethods[kind]
	if !ok {
		return nil, errFailed
	}
	limit = min(max(limit, 1), MaxLimit)
	var out []providers.Collection
	if c.guard(func() error {
		q := url.Values{"q": {query}, "count": {strconv.Itoa(limit)}}
		if kind == providers.KindArtist {
			var a struct {
				Items []artistItem `json:"items"`
			}
			if err := c.call(ctx, method, q, &a); err != nil {
				return err
			}
			for _, it := range a.Items {
				if col, ok := artist(it); ok && len(out) < limit {
					out = append(out, col)
				}
			}
			return nil
		}
		var a struct {
			Items []listItem `json:"items"`
		}
		if err := c.call(ctx, method, q, &a); err != nil {
			return err
		}
		for _, it := range a.Items {
			if col, ok := list(kind, it); ok && len(out) < limit {
				out = append(out, col)
			}
		}
		return nil
	}) != nil {
		return nil, errFailed
	}
	return out, nil
}

func artist(it artistItem) (providers.Collection, bool) {
	if !artistIDRe.MatchString(it.ID) || strings.TrimSpace(it.Name) == "" {
		return providers.Collection{}, false
	}
	col := providers.Collection{Source: providers.SourceVK, Kind: providers.KindArtist, ID: it.ID, Title: clip(it.Name)}
	if domainRe.MatchString(it.Domain) {
		col.WebURL = "https://vk.com/artist/" + it.Domain
	}
	return col, true
}

func list(kind string, it listItem) (providers.Collection, bool) {
	if it.ID <= 0 || it.OwnerID == 0 || strings.TrimSpace(it.Title) == "" {
		return providers.Collection{}, false
	}
	id := strconv.FormatInt(it.OwnerID, 10) + "_" + strconv.FormatInt(it.ID, 10)
	if it.AccessKey != "" {
		id += "_" + it.AccessKey
	}
	if !idRe.MatchString(id) {
		return providers.Collection{}, false
	}
	names := make([]string, 0, len(it.MainArtists))
	for _, a := range it.MainArtists {
		if n := strings.TrimSpace(a.Name); n != "" {
			names = append(names, n)
		}
	}
	col := providers.Collection{
		Source: providers.SourceVK, Kind: kind, ID: id, Title: clip(it.Title), Owner: clip(strings.Join(names, ", ")),
		Tracks: max(it.Count, 0), WebURL: "https://vk.com/music/" + kind + "/" + id,
	}
	if it.Photo != nil {
		col.ArtworkURL = it.Photo.best()
	}
	if col.ArtworkURL == "" && len(it.Thumbs) > 0 {
		col.ArtworkURL = it.Thumbs[0].best()
	}
	return col, true
}

// CollectionSongs answers up to limit songs: an album's or a playlist's in
// order, or an artist's own. A list vk keeps from this account is
// ErrMusicRefused.
func (c *Client) CollectionSongs(ctx context.Context, col providers.Collection, limit int) ([]providers.Track, error) {
	if col.Source != providers.SourceVK || limit < 1 {
		return nil, errFailed
	}
	limit = min(limit, maxListSongs)
	var out []providers.Track
	err := c.guard(func() error {
		var err error
		switch col.Kind {
		case providers.KindAlbum, providers.KindPlaylist:
			if !idRe.MatchString(col.ID) {
				return errFailed
			}
			out, err = c.listSongs(ctx, col.ID, limit)
		case providers.KindArtist:
			if !artistIDRe.MatchString(col.ID) {
				return errFailed
			}
			var a itemsAnswer
			q := url.Values{"artist_id": {col.ID}, "count": {strconv.Itoa(min(limit, maxArtistSongs))}}
			err = c.call(ctx, "audio.getAudiosByArtist", q, &a)
			out = tracks(a.Items, limit, "")
		default:
			return errFailed
		}
		return err
	})
	var api apiError
	switch {
	case err == nil:
		return out, nil
	case errors.As(err, &api) && api.signIn() && api != 5:
		return nil, providers.ErrMusicRefused
	}
	return nil, errFailed
}

// listSongs reads an album's or a playlist's songs in order, a page at a time
func (c *Client) listSongs(ctx context.Context, id string, limit int) ([]providers.Track, error) {
	p := strings.SplitN(id, "_", 3)
	q := url.Values{"owner_id": {p[0]}, "album_id": {p[1]}, "count": {strconv.Itoa(pageSize)}}
	if len(p) == 3 {
		q.Set("access_key", p[2])
	}
	var out []providers.Track
	for offset := 0; len(out) < limit && offset < maxListSongs; offset += pageSize {
		q.Set("offset", strconv.Itoa(offset))
		var a itemsAnswer
		if err := c.call(ctx, "audio.get", q, &a); err != nil {
			return nil, err
		}
		out = append(out, tracks(a.Items, limit-len(out), "")...)
		if len(a.Items) < pageSize {
			break
		}
	}
	return out, nil
}
