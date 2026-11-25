package ytweb

import (
	"context"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// Playlist is a playlist's header.
type Playlist struct {
	ID          string
	Title       string
	Channel     string
	ChannelID   string
	Description string
	// Stats: Views and VideoCount
	Stats providers.Stats
}

// PlaylistPage is a playlist's header and its first rows (100 at most), in
// the playlist's own order.
type PlaylistPage struct {
	Playlist Playlist
	Page     Page
}

// Playlist reads a playlist by id.
func (c *Client) Playlist(ctx context.Context, id string) (PlaylistPage, error) {
	var out PlaylistPage
	err := c.guard("playlist", func() error {
		id = strings.TrimPrefix(id, "VL")
		if !ValidPlaylistID(id) {
			return ErrNotFound
		}
		root, err := c.post(ctx, "browse", map[string]any{"browseId": "VL" + id})
		if err != nil {
			return err
		}
		out.Playlist = playlistHeader(root, id)
		out.Page = c.page(root, "browse", Item{})
		if out.Playlist.Title == "" && len(out.Page.Items) == 0 {
			return ErrNotFound
		}
		return nil
	})
	return out, err
}

func playlistHeader(root node, id string) Playlist {
	pl := Playlist{ID: id}
	ph := at(root, "header", "pageHeaderRenderer", "content", "pageHeaderViewModel")
	pl.Title = clip(text(at(ph, "title", "dynamicTextViewModel", "text")), maxTitle)
	pl.Description = clip(text(at(ph, "description", "descriptionPreviewViewModel", "description")), maxDesc)
	walk(at(ph, "metadata"), func(k string, x any) bool {
		if k != "text" {
			return true
		}
		t := text(x)
		switch {
		case browseID(x) != "" && pl.ChannelID == "":
			pl.Channel, pl.ChannelID = clip(strings.TrimPrefix(t, "by "), maxChannel), channelOnly(browseID(x))
		case strings.Contains(t, " view"):
			pl.Stats.Views, _ = ParseCount(t)
		case strings.Contains(t, "video") || strings.Contains(t, "episode"):
			n, _ := ParseCount(t)
			pl.Stats.VideoCount = int(n)
		}
		return false
	})
	// the older header and the metadata renderer fill what the view model left out
	if h := at(root, "header", "playlistHeaderRenderer"); h != nil {
		if pl.Title == "" {
			pl.Title = clip(text(h["title"]), maxTitle)
		}
		if pl.Channel == "" {
			pl.Channel, pl.ChannelID = clip(text(h["ownerText"]), maxChannel), channelOnly(browseID(h["ownerText"]))
		}
		if pl.Stats.VideoCount == 0 {
			n, _ := ParseCount(text(h["numVideosText"]))
			pl.Stats.VideoCount = int(n)
		}
	}
	if pl.Title == "" {
		pl.Title = clip(str(root, "metadata", "playlistMetadataRenderer", "title"), maxTitle)
	}
	return pl
}
