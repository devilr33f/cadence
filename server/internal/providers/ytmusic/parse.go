package ytmusic

import (
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytweb"
)

const (
	maxTextRunes = 200
	pageArtist   = "MUSIC_PAGE_TYPE_ARTIST"
	pageAlbum    = "MUSIC_PAGE_TYPE_ALBUM"
	pagePlaylist = "MUSIC_PAGE_TYPE_PLAYLIST"
	pageChannel  = "MUSIC_PAGE_TYPE_USER_CHANNEL"
	// artworkSize is the side a cover is asked for; the search lists 60 and 120
	artworkSize = "=w544-h544"
)

var (
	videoIDRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	durationRe = regexp.MustCompile(`^(\d{1,2}:)?\d{1,2}:\d{2}$`)
	sizeRe     = regexp.MustCompile(`=w\d+-h\d+[^/?#]*$`)
)

// WatchURL is the video's page on YouTube Music.
func WatchURL(id string) string { return defaultBaseURL + "/watch?v=" + id }

// ValidVideoID says whether id is a YouTube video id.
func ValidVideoID(id string) bool { return videoIDRe.MatchString(id) }

type run struct {
	Text               string     `json:"text"`
	NavigationEndpoint navigation `json:"navigationEndpoint"`
}

type navigation struct {
	WatchEndpoint struct {
		VideoID string `json:"videoId"`
	} `json:"watchEndpoint"`
	BrowseEndpoint struct {
		BrowseID string `json:"browseId"`
		Configs  struct {
			Music struct {
				PageType string `json:"pageType"`
			} `json:"browseEndpointContextMusicConfig"`
		} `json:"browseEndpointContextSupportedConfigs"`
	} `json:"browseEndpoint"`
}

type columnText struct {
	Runs []run `json:"runs"`
}

type listItem struct {
	PlaylistItemData struct {
		VideoID string `json:"videoId"`
	} `json:"playlistItemData"`
	Thumbnail struct {
		Renderer struct {
			Thumbnail struct {
				Thumbnails []struct {
					URL string `json:"url"`
				} `json:"thumbnails"`
			} `json:"thumbnail"`
		} `json:"musicThumbnailRenderer"`
	} `json:"thumbnail"`
	FlexColumns []struct {
		Renderer struct {
			Text columnText `json:"text"`
		} `json:"musicResponsiveListItemFlexColumnRenderer"`
	} `json:"flexColumns"`
	// an album's or a playlist's rows put the length here
	FixedColumns []struct {
		Renderer struct {
			Text columnText `json:"text"`
		} `json:"musicResponsiveListItemFixedColumnRenderer"`
	} `json:"fixedColumns"`
}

// parseSongs reads every song row of a search answer, in its order: the
// rows sit deep in tabs, shelves and sections whose shape changes, so it
// walks the whole answer for them. ok is false for a body that is not JSON.
func parseSongs(b []byte) ([]providers.Track, bool) {
	var out []providers.Track
	seen := map[string]bool{}
	ok := walkJSON(b, "musicResponsiveListItemRenderer", func(raw []byte) {
		var it listItem
		if unmarshal(raw, &it) != nil {
			return
		}
		if t, ok := song(it); ok && !seen[t.VideoID] {
			seen[t.VideoID] = true
			out = append(out, t)
		}
	})
	return out, ok
}

func unmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// walkJSON calls f with every value under key, in the answer's order; false
// for a body that is not JSON
func walkJSON(b []byte, key string, f func([]byte)) bool {
	var root any
	if json.Unmarshal(b, &root) != nil {
		return false
	}
	walk(root, key, func(v any) {
		if raw, err := json.Marshal(v); err == nil {
			f(raw)
		}
	})
	return true
}

func walk(v any, key string, f func(any)) {
	switch x := v.(type) {
	case map[string]any:
		// sorted: the rows' order is the ranking
		for _, k := range slices.Sorted(maps.Keys(x)) {
			if k == key {
				f(x[k])
				continue
			}
			walk(x[k], key, f)
		}
	case []any:
		for _, c := range x {
			walk(c, key, f)
		}
	}
}

// song is a row with a video id and a title; its second column runs
// artists, an album and a duration, joined by separators.
func song(it listItem) (providers.Track, bool) {
	if len(it.FlexColumns) < 2 {
		return providers.Track{}, false
	}
	first := it.FlexColumns[0].Renderer.Text.Runs
	if len(first) == 0 {
		return providers.Track{}, false
	}
	id := first[0].NavigationEndpoint.WatchEndpoint.VideoID
	if id == "" {
		id = it.PlaylistItemData.VideoID
	}
	title := strings.TrimSpace(first[0].Text)
	if !ValidVideoID(id) || title == "" {
		return providers.Track{}, false
	}
	t := providers.Track{Source: providers.SourceYouTube, ID: id, VideoID: id, Title: clip(title), WebURL: WatchURL(id)}
	var artists, plain []string
	for _, r := range it.FlexColumns[1].Renderer.Text.Runs {
		text := strings.TrimSpace(r.Text)
		switch page := r.NavigationEndpoint.BrowseEndpoint.Configs.Music.PageType; {
		case text == "" || text == "•" || text == "&" || text == ",":
		case page == pageArtist:
			artists = append(artists, text)
		case page == pageAlbum:
			t.Album = clip(text)
		case durationRe.MatchString(text):
			t.DurationS = seconds(text)
		case statOf(&t.Stats, text):
		case page == "" || page == pageChannel:
			plain = append(plain, text)
		}
	}
	// a playlist's or an artist page's rows name the album in a later column;
	// a search's rows put the plays there
	for _, col := range it.FlexColumns[min(2, len(it.FlexColumns)):] {
		for _, r := range col.Renderer.Text.Runs {
			if r.NavigationEndpoint.BrowseEndpoint.Configs.Music.PageType == pageAlbum && t.Album == "" {
				t.Album = clip(strings.TrimSpace(r.Text))
			}
			statOf(&t.Stats, r.Text)
		}
	}
	for _, col := range it.FixedColumns {
		for _, r := range col.Renderer.Text.Runs {
			if text := strings.TrimSpace(r.Text); t.DurationS == 0 && durationRe.MatchString(text) {
				t.DurationS = seconds(text)
			}
		}
	}
	// an artist without a page of its own is plain text
	if len(artists) == 0 && len(plain) > 0 && !isKind(plain[0]) {
		artists = plain[:1]
	}
	t.Artist = clip(strings.Join(artists, ", "))
	t.ArtworkURL = artwork(it)
	return t, true
}

// isKind is the row's type word some answers lead the column with
func isKind(s string) bool {
	switch strings.ToLower(s) {
	case "song", "video", "episode":
		return true
	}
	return false
}

func artwork(it listItem) string {
	th := it.Thumbnail.Renderer.Thumbnail.Thumbnails
	if len(th) == 0 {
		return ""
	}
	u := th[len(th)-1].URL
	if !strings.HasPrefix(u, "https://") || len(u) > 1024 {
		return ""
	}
	return providers.MusicArtwork(sizeRe.ReplaceAllString(u, artworkSize))
}

// statRe is a row's count as YouTube Music shows it with hl=en
var statRe = regexp.MustCompile(`^\s*[\d.,]+\s*[KMB]?\s+(plays|views|subscribers|monthly audience)\s*$`)

// statOf reads one of a row's counts into s; false for any other text
func statOf(s *providers.Stats, text string) bool {
	m := statRe.FindStringSubmatch(text)
	if m == nil {
		return false
	}
	n, ok := ytweb.ParseCount(text)
	if !ok {
		return false
	}
	switch m[1] {
	case "plays":
		s.Plays = n
	case "views":
		s.Views = n
	case "subscribers":
		s.Subscribers = n
	case "monthly audience":
		s.MonthlyAudience = n
	}
	return true
}

func seconds(s string) int {
	n := 0
	for _, p := range strings.Split(s, ":") {
		v, _ := strconv.Atoi(p)
		n = n*60 + v
	}
	return n
}

func clip(s string) string {
	r := []rune(s)
	if len(r) <= maxTextRunes {
		return s
	}
	return string(r[:maxTextRunes])
}
