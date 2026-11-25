package ytmusic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	browsePath = "/youtubei/v1/browse?prettyPrint=false"
	// a page of a playlist holds 100 rows; three cover the queue's 200
	maxBrowsePages = 3
)

// the search tabs, as ytmusicapi (MIT) documents their params
var collectionFilters = map[string]string{
	providers.KindAlbum:    "EgWKAQIYAWoMEA4QChADEAQQCRAF",
	providers.KindArtist:   "EgWKAQIgAWoMEA4QChADEAQQCRAF",
	providers.KindPlaylist: "Eg-KAQwIABAAGAAgACgBMABqChAEEAMQCRAFEAo=",
}

var collectionPages = map[string]string{
	pageAlbum:    providers.KindAlbum,
	pageArtist:   providers.KindArtist,
	pagePlaylist: providers.KindPlaylist,
}

var (
	albumIDRe    = regexp.MustCompile(`^MPREb_[A-Za-z0-9_-]{5,40}$`)
	playlistIDRe = regexp.MustCompile(`^VL[A-Za-z0-9_-]{10,80}$`)
	artistIDRe   = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)
	// an artist's radio ("Start mix")
	artistRadioRe = regexp.MustCompile(`^RDEM[A-Za-z0-9_-]{10,60}$`)
)

func validCollectionID(kind, id string) bool {
	switch kind {
	case providers.KindAlbum:
		return albumIDRe.MatchString(id)
	case providers.KindPlaylist:
		return playlistIDRe.MatchString(id)
	case providers.KindArtist:
		return artistIDRe.MatchString(id)
	}
	return false
}

// SearchCollections answers up to limit albums, playlists or artists (1 to
// MaxLimit) in the order YouTube Music ranks them. Its search tells no track
// count.
func (c *Client) SearchCollections(ctx context.Context, kind, query string, limit int) ([]providers.Collection, error) {
	var out []providers.Collection
	var err error
	params, ok := collectionFilters[kind]
	if !ok {
		return nil, errFailed
	}
	if providers.Guard(c.log, "youtube-music", providers.CodeMusicFailed, func() {
		var b []byte
		if b, err = c.fetch(ctx, searchPath, request{Context: c.clientContext(), Query: query, Params: params}); err != nil {
			return
		}
		var parsed bool
		if out, parsed = parseCollections(b, kind); !parsed {
			err = errFailed
		}
	}) || err != nil {
		return nil, errFailed
	}
	return out[:min(len(out), min(max(limit, 1), MaxLimit))], nil
}

type browseRequest struct {
	Context      clientContext `json:"context"`
	BrowseID     string        `json:"browseId,omitempty"`
	Continuation string        `json:"continuation,omitempty"`
}

// CollectionSongs answers up to limit songs: an album's or a playlist's in
// order (an album's rows take its title, artist and cover), or an artist's
// radio.
func (c *Client) CollectionSongs(ctx context.Context, col providers.Collection, limit int) ([]providers.Track, error) {
	var out []providers.Track
	var err error
	if col.Source != providers.SourceYouTube || !validCollectionID(col.Kind, col.ID) || limit < 1 {
		return nil, errFailed
	}
	if providers.Guard(c.log, "youtube-music", providers.CodeMusicFailed, func() {
		if col.Kind == providers.KindArtist {
			if !artistRadioRe.MatchString(col.Radio) {
				err = errFailed
				return
			}
			out, err = c.watchNext(ctx, "", col.Radio, limit)
			return
		}
		out, err = c.browseSongs(ctx, col, limit)
	}) || err != nil {
		return nil, errFailed
	}
	return out, nil
}

// browseSongs reads the list's pages until it has limit songs
func (c *Client) browseSongs(ctx context.Context, col providers.Collection, limit int) ([]providers.Track, error) {
	var out []providers.Track
	seen := map[string]bool{}
	req := browseRequest{Context: c.clientContext(), BrowseID: col.ID}
	for range maxBrowsePages {
		b, err := c.fetch(ctx, browsePath, req)
		if err != nil {
			return nil, err
		}
		page, ok := parseSongs(b)
		if !ok {
			return nil, errFailed
		}
		for _, t := range page {
			if seen[t.VideoID] {
				continue
			}
			seen[t.VideoID] = true
			if col.Kind == providers.KindAlbum {
				t = fromAlbum(t, col)
			}
			out = append(out, t)
		}
		next := continuation(b)
		if len(out) >= limit || next == "" {
			break
		}
		req = browseRequest{Context: c.clientContext(), Continuation: next}
	}
	if len(out) == 0 {
		return nil, errFailed
	}
	return out[:min(len(out), limit)], nil
}

// fromAlbum fills what an album's rows leave out
func fromAlbum(t providers.Track, col providers.Collection) providers.Track {
	if t.Album == "" {
		t.Album = col.Title
	}
	if t.Artist == "" {
		t.Artist = col.Owner
	}
	if t.ArtworkURL == "" {
		t.ArtworkURL = col.ArtworkURL
	}
	return t
}

// continuation is the token of a list's next page, "" at its end
func continuation(b []byte) string {
	var token string
	walkJSON(b, "continuationItemRenderer", func(raw []byte) {
		var it struct {
			Endpoint struct {
				Command struct {
					Token string `json:"token"`
				} `json:"continuationCommand"`
			} `json:"continuationEndpoint"`
		}
		if unmarshal(raw, &it) == nil && it.Endpoint.Command.Token != "" && len(it.Endpoint.Command.Token) <= 4096 {
			token = it.Endpoint.Command.Token
		}
	})
	return token
}

// fetch posts one InnerTube request and answers its body
func (c *Client) fetch(ctx context.Context, path string, v any) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, err := c.call(ctx, path, v)
	if err != nil {
		return nil, errFailed
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, errFailed
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, errFailed
	}
	return b, nil
}

type collectionItem struct {
	listItem
	NavigationEndpoint navigation      `json:"navigationEndpoint"`
	Menu               json.RawMessage `json:"menu"`
}

// parseCollections reads every row of a search answer that opens an album,
// a playlist or an artist of kind, in order
func parseCollections(b []byte, kind string) ([]providers.Collection, bool) {
	var out []providers.Collection
	seen := map[string]bool{}
	ok := walkJSON(b, "musicResponsiveListItemRenderer", func(raw []byte) {
		var it collectionItem
		if unmarshal(raw, &it) != nil {
			return
		}
		if col, ok := collection(it); ok && col.Kind == kind && !seen[col.ID] {
			seen[col.ID] = true
			out = append(out, col)
		}
	})
	return out, ok
}

func collection(it collectionItem) (providers.Collection, bool) {
	be := it.NavigationEndpoint.BrowseEndpoint
	kind := collectionPages[be.Configs.Music.PageType]
	if kind == "" || !validCollectionID(kind, be.BrowseID) || len(it.FlexColumns) == 0 {
		return providers.Collection{}, false
	}
	first := it.FlexColumns[0].Renderer.Text.Runs
	if len(first) == 0 || strings.TrimSpace(first[0].Text) == "" {
		return providers.Collection{}, false
	}
	col := providers.Collection{
		Source: providers.SourceYouTube, Kind: kind, ID: be.BrowseID, Title: clip(strings.TrimSpace(first[0].Text)),
		ArtworkURL: artwork(it.listItem), WebURL: collectionURL(kind, be.BrowseID),
	}
	if len(it.FlexColumns) > 1 {
		for _, r := range it.FlexColumns[1].Renderer.Text.Runs {
			statOf(&col.Stats, r.Text)
		}
	}
	if kind == providers.KindArtist {
		col.Radio = artistRadio(it.Menu)
		return col, true
	}
	if len(it.FlexColumns) > 1 {
		col.Owner = owner(it.FlexColumns[1].Renderer.Text.Runs)
	}
	return col, true
}

// owner is an album's artists or a playlist's owner: the runs with a page of
// their own, else the first plain one that is not the row's type, a count or
// a year
func owner(runs []run) string {
	var named, plain []string
	for _, r := range runs {
		text := strings.TrimSpace(r.Text)
		switch page := r.NavigationEndpoint.BrowseEndpoint.Configs.Music.PageType; {
		case text == "" || text == "•" || text == "&" || text == ",":
		case page == pageArtist || page == pageChannel:
			named = append(named, text)
		case page == "" && !isCollectionKind(text) && !strings.ContainsAny(text, "0123456789"):
			plain = append(plain, text)
		}
	}
	if len(named) == 0 && len(plain) > 0 {
		named = plain[:1]
	}
	return clip(strings.Join(named, ", "))
}

func isCollectionKind(s string) bool {
	switch strings.ToLower(s) {
	case "album", "single", "ep", "playlist", "artist":
		return true
	}
	return false
}

// artistRadio is the playlist of the row's "Start mix"
func artistRadio(menu json.RawMessage) string {
	var id string
	if len(menu) == 0 {
		return ""
	}
	walkJSON(menu, "watchPlaylistEndpoint", func(raw []byte) {
		var e struct {
			PlaylistID string `json:"playlistId"`
		}
		if id == "" && unmarshal(raw, &e) == nil && artistRadioRe.MatchString(e.PlaylistID) {
			id = e.PlaylistID
		}
	})
	return id
}

func collectionURL(kind, id string) string {
	switch kind {
	case providers.KindAlbum:
		return defaultBaseURL + "/browse/" + id
	case providers.KindPlaylist:
		return defaultBaseURL + "/playlist?list=" + strings.TrimPrefix(id, "VL")
	}
	return defaultBaseURL + "/channel/" + id
}
