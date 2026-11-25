package ytmusic

import (
	"context"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const nextPath = "/youtubei/v1/next?prettyPrint=false"

type nextRequest struct {
	Context     clientContext `json:"context"`
	VideoID     string        `json:"videoId,omitempty"`
	PlaylistID  string        `json:"playlistId"`
	IsAudioOnly bool          `json:"isAudioOnly"`
}

// Radio answers up to limit songs YouTube Music's radio of the video plays
// after it (its "up next" mix), the video itself left out.
func (c *Client) Radio(ctx context.Context, videoID string, limit int) ([]providers.Track, error) {
	var out []providers.Track
	var err error
	if !ValidVideoID(videoID) {
		return nil, errFailed
	}
	if providers.Guard(c.log, "youtube-music", providers.CodeMusicFailed, func() { out, err = c.watchNext(ctx, videoID, "RDAMVM"+videoID, limit) }) || err != nil {
		return nil, errFailed
	}
	return out, nil
}

// watchNext reads the queue YouTube Music plays for a playlist (a radio),
// from the video when one is given; the video itself is left out
func (c *Client) watchNext(ctx context.Context, videoID, playlistID string, limit int) ([]providers.Track, error) {
	b, err := c.fetch(ctx, nextPath, nextRequest{
		Context: c.clientContext(), VideoID: videoID, PlaylistID: playlistID, IsAudioOnly: true,
	})
	if err != nil {
		return nil, err
	}
	tracks, ok := parseQueue(b)
	if !ok {
		return nil, errFailed
	}
	out := tracks[:0]
	for _, t := range tracks {
		if t.VideoID != videoID {
			out = append(out, t)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type queueItem struct {
	VideoID string `json:"videoId"`
	Title   struct {
		Runs []run `json:"runs"`
	} `json:"title"`
	LongByline struct {
		Runs []run `json:"runs"`
	} `json:"longBylineText"`
	LengthText struct {
		Runs []run `json:"runs"`
	} `json:"lengthText"`
	Thumbnail struct {
		Thumbnails []struct {
			URL string `json:"url"`
		} `json:"thumbnails"`
	} `json:"thumbnail"`
}

// parseQueue reads a watch-next answer's queue rows in order
func parseQueue(b []byte) ([]providers.Track, bool) {
	var out []providers.Track
	seen := map[string]bool{}
	ok := walkJSON(b, "playlistPanelVideoRenderer", func(raw []byte) {
		var it queueItem
		if unmarshal(raw, &it) != nil || !ValidVideoID(it.VideoID) || seen[it.VideoID] || len(it.Title.Runs) == 0 {
			return
		}
		title := strings.TrimSpace(it.Title.Runs[0].Text)
		if title == "" {
			return
		}
		seen[it.VideoID] = true
		t := providers.Track{Source: providers.SourceYouTube, ID: it.VideoID, VideoID: it.VideoID, Title: clip(title), WebURL: WatchURL(it.VideoID)}
		var artists []string
		for _, r := range it.LongByline.Runs {
			switch r.NavigationEndpoint.BrowseEndpoint.Configs.Music.PageType {
			case pageArtist:
				artists = append(artists, strings.TrimSpace(r.Text))
			case pageAlbum:
				t.Album = clip(strings.TrimSpace(r.Text))
			}
		}
		if len(artists) == 0 && len(it.LongByline.Runs) > 0 {
			artists = []string{strings.TrimSpace(it.LongByline.Runs[0].Text)}
		}
		t.Artist = clip(strings.Join(artists, ", "))
		if len(it.LengthText.Runs) > 0 && durationRe.MatchString(it.LengthText.Runs[0].Text) {
			t.DurationS = seconds(it.LengthText.Runs[0].Text)
		}
		if th := it.Thumbnail.Thumbnails; len(th) > 0 {
			if u := th[len(th)-1].URL; strings.HasPrefix(u, "https://") && len(u) <= 1024 {
				t.ArtworkURL = providers.MusicArtwork(sizeRe.ReplaceAllString(u, artworkSize))
			}
		}
		out = append(out, t)
	})
	return out, ok
}
