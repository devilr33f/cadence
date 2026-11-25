package ytweb

import (
	"context"
	"strconv"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// Video is a video's own facts, from the player's answer: exact views,
// likes and publish time, which it carries even where it will not play to
// this client (a datacentre's address gets UNPLAYABLE or LOGIN_REQUIRED).
type Video struct {
	Item
	Description string
	Category    string
	// LiveNow is a stream on air
	LiveNow bool
}

// Video reads one video.
func (c *Client) Video(ctx context.Context, id string) (Video, error) {
	var out Video
	err := c.guard("video", func() error {
		var err error
		out, err = c.video(ctx, id)
		return err
	})
	return out, err
}

func (c *Client) video(ctx context.Context, id string) (Video, error) {
	if !ValidVideoID(id) {
		return Video{}, ErrNotFound
	}
	root, err := c.post(ctx, "player", map[string]any{"videoId": id})
	if err != nil {
		return Video{}, err
	}
	vd := at(root, "videoDetails")
	if str(vd, "videoId") != id {
		return Video{}, ErrNotFound
	}
	mf := at(root, "microformat", "playerMicroformatRenderer")
	v := Video{Item: Item{Kind: KindVideo, ID: id, Title: clip(str(vd, "title"), maxTitle),
		Channel: clip(str(vd, "author"), maxChannel), ChannelID: channelOnly(str(vd, "channelId")),
		Thumbnail: VideoThumbnail(id)}}
	v.DurationS, _ = strconv.Atoi(str(vd, "lengthSeconds"))
	v.Stats.Views, _ = strconv.ParseInt(str(vd, "viewCount"), 10, 64)
	v.Stats.Likes, _ = strconv.ParseInt(str(mf, "likeCount"), 10, 64)
	if t, err := time.Parse(time.RFC3339, str(mf, "publishDate")); err == nil {
		v.Stats.Published = t
	} else if t, err := time.Parse("2006-01-02", str(mf, "publishDate")); err == nil {
		v.Stats.Published = t
	}
	v.Description = clip(str(vd, "shortDescription"), maxDesc)
	v.Category = clip(str(mf, "category"), 64)
	v.LiveNow, _ = at(mf, "liveBroadcastDetails")["isLiveNow"].(bool)
	if v.LiveNow {
		v.Kind = KindLive
	}
	if b, _ := vd["isUpcoming"].(bool); b {
		v.Upcoming = true
	}
	c.stats.put(id, v.Stats)
	return v, nil
}

// Stats reads the exact counts of up to len(ids) videos at once, four at a
// time, within ctx: an id that does not answer in time is left out, so a
// listing is never held up by it. Counts read in the last 30 min are reused.
func (c *Client) Stats(ctx context.Context, ids []string) map[string]providers.Stats {
	out := map[string]providers.Stats{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, id := range ids {
		if s, ok := c.stats.get(id); ok {
			out[id] = s
			continue
		}
		if !ValidVideoID(id) {
			continue
		}
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			v, err := c.Video(ctx, id)
			if err != nil {
				return
			}
			mu.Lock()
			out[id] = v.Stats
			mu.Unlock()
		})
	}
	wg.Wait()
	return out
}
