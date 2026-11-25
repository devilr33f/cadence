package cards

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
)

// at most this many found items on one card
const maxListed = 5

// volumeCards: the level the device set, read-only: no card raises a
// volume (rule 7)
func volumeCards(_ *ToolCards, r ToolResult) []Subject {
	var out struct {
		Level *int `json:"level"`
	}
	if !decode(r, &out) || out.Level == nil {
		return nil
	}
	level := min(max(*out.Level, 0), 100)
	return card("volume", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		icon := "volume"
		if level == 0 {
			icon = "mute"
		}
		label := strconv.Itoa(level) + " %"
		b := &cardsv1.CardBlock{Title: rr.T("volume.title"), Icon: icon, Tone: cardsv1.Tone_TONE_NEUTRAL,
			Children: pcards.Nodes(&cardsv1.Meter{Value: float64(level), Min: 0, Max: 100, Label: rr.T("volume.level"), ValueLabel: label})}
		return b, rr.T("volume.title") + ": " + label
	})
}

// modeCards: the agent mode the device switched to
func modeCards(_ *ToolCards, r ToolResult) []Subject {
	var out struct {
		Mode string `json:"mode"`
	}
	if !decode(r, &out) || clip(out.Mode, 24) == "" {
		return nil
	}
	return card("mode", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		name := clip(out.Mode, 24)
		b := &cardsv1.CardBlock{Title: rr.T("mode.title"), Icon: "sparkle", Tone: cardsv1.Tone_TONE_NEUTRAL,
			Children: pcards.Nodes(&cardsv1.Value{Value: name, Label: rr.T("mode.now")})}
		return b, rr.T("mode.title") + ": " + name
	})
}

type musicFound struct {
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Owner      string `json:"owner"`
	Album      string `json:"album"`
	DurationS  int    `json:"durationS"`
	TrackCount int    `json:"trackCount"`
	Provider   string `json:"provider"`
}

// musicSearchCards: the first results, each its title and who and where
func musicSearchCards(_ *ToolCards, r ToolResult) []Subject {
	var out struct {
		Results []musicFound `json:"results"`
	}
	if !decode(r, &out) || len(out.Results) == 0 {
		return nil
	}
	return card("music", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		entries := make([][2]string, 0, len(out.Results))
		for _, f := range out.Results {
			// what each result is and whose, never the provider on every line
			m := MusicItem{Kind: f.Kind, Title: f.Title, Artist: f.Artist, Owner: f.Owner, Album: f.Album, DurationS: f.DurationS, TrackCount: f.TrackCount}
			entries = append(entries, [2]string{m.Title, joined(" · ", m.who(), m.meta(rr, false))})
		}
		b := &cardsv1.CardBlock{Title: rr.T("music.results"), Icon: "music", Tone: cardsv1.Tone_TONE_NEUTRAL,
			Children: pcards.Nodes(listed(entries, maxListed))}
		return b, rr.T("search.fallback", "count", strconv.Itoa(len(out.Results)))
	})
}

// musicPlayCards: what the play did, playing, queued or a radio
func musicPlayCards(_ *ToolCards, r ToolResult) []Subject {
	var out struct {
		Playing, Queued, Radio bool
		Songs                  int
	}
	if !decode(r, &out) {
		return nil
	}
	return card("music-play", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		state, kind := rr.T("music.playing"), cardsv1.StateKind_STATE_KIND_ON
		switch {
		case out.Radio:
			state = rr.T("music.radio")
		case out.Queued:
			state, kind = rr.T("music.queued"), cardsv1.StateKind_STATE_KIND_UNKNOWN
		}
		line := ""
		if out.Songs > 0 {
			line = rr.T("music.tracks", "n", strconv.Itoa(out.Songs))
		}
		return done(rr, rr.T("music.title"), "music", state, kind, line), rr.T("music.title") + ": " + state
	})
}

// videoFound is one of video_search's results as the tool wrote it
type videoFound struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Channel   string `json:"channel"`
	DurationS int    `json:"durationS"`
	Live      bool   `json:"live"`
}

// videoSearchCards: the first videos, each its title, channel and length; with the results and the play to read,
// wide tiles a press plays (the music tiles' counterpart for videos)
func videoSearchCards(t *ToolCards, r ToolResult) []Subject {
	var out struct {
		Results []videoFound `json:"results"`
	}
	if !decode(r, &out) || len(out.Results) == 0 {
		return nil
	}
	var query string
	var a struct {
		Query string `json:"query"`
	}
	if len(r.Args) > 0 && json.Unmarshal(r.Args, &a) == nil {
		query = a.Query
	}
	tiles, whole := videoTiles(t, r.DeviceID, out.Results)
	if !whole {
		// a result nobody holds, or without its still, keeps the card the list
		return videoListed(r, out.Results)
	}
	dev := r.DeviceID
	return card("video-tiles", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		list := &cardsv1.PagedList{}
		for _, x := range tiles {
			id := x.id
			length := ""
			switch {
			case x.found.Live:
				length = rr.T("video.live")
			case x.found.DurationS > 0:
				length = clock(x.found.DurationS)
			}
			list.Items = append(list.Items, pcards.N(&cardsv1.MediaTile{
				Image: x.img, Title: clip(x.found.Title, 80),
				Subtitle: clip(joined(" · ", clip(x.found.Channel, 60), length), 120),
				Action: rr.Bind(Binding{Idempotent: true, Run: func(ctx context.Context, _ Act) Outcome {
					if err := t.Videos.VideoPlay(ctx, dev, id); err != nil {
						return Outcome{Refused: "failed"}
					}
					return Outcome{Record: &Record{Tool: "video_play", Args: map[string]string{"id": id},
						Result: map[string]bool{"playing": true}}}
				}}),
			}))
		}
		b := &cardsv1.CardBlock{Title: rr.T("video.results"), Subtitle: clip(query, 80), Icon: "film",
			Tone: cardsv1.Tone_TONE_NEUTRAL, Children: pcards.Nodes(list)}
		return b, rr.T("search.fallback", "count", strconv.Itoa(len(tiles)))
	})
}

// videoTile is one result with the still its tile shows
type videoTile struct {
	id    string
	found VideoFound
	img   *cardsv1.ImageRef
}

// videoTiles reads the results' own answers (their stills) where the seam holds; whole says every tile could be
// drawn, else the caller keeps the list
func videoTiles(t *ToolCards, deviceID string, results []videoFound) ([]videoTile, bool) {
	if t.Videos == nil {
		return nil, false
	}
	tiles := make([]videoTile, 0, len(results))
	for _, res := range results {
		v, ok := t.Videos.VideoResult(deviceID, res.ID)
		if !ok {
			v = VideoFound{Title: res.Title, Channel: res.Channel, DurationS: res.DurationS, Live: res.Live}
		}
		img := videoCover(t, v)
		if img == nil || clip(v.Title, 80) == "" {
			return nil, false
		}
		tiles = append(tiles, videoTile{id: res.ID, found: v, img: img})
		if len(tiles) == pcards.MaxListItems {
			break
		}
	}
	return tiles, true
}

// videoListed is the search's plain list, the answer without the results or the play
func videoListed(r ToolResult, results []videoFound) []Subject {
	return card("video", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		entries := make([][2]string, 0, len(results))
		for _, v := range results {
			length := ""
			switch {
			case v.Live:
				length = rr.T("video.live")
			case v.DurationS > 0:
				length = clock(v.DurationS)
			}
			entries = append(entries, [2]string{v.Title, joined(" · ", clip(v.Channel, 80), length)})
		}
		b := &cardsv1.CardBlock{Title: rr.T("video.results"), Icon: "film", Tone: cardsv1.Tone_TONE_NEUTRAL,
			Children: pcards.Nodes(listed(entries, maxListed))}
		return b, rr.T("search.fallback", "count", strconv.Itoa(len(results)))
	})
}

// videoCover is a video's still through the image proxy, wide as its tile
func videoCover(t *ToolCards, v VideoFound) *cardsv1.ImageRef {
	if t.Images == nil || v.ArtworkURL == "" {
		return nil
	}
	id := t.Images(v.ArtworkURL)
	if id == "" {
		return nil
	}
	return &cardsv1.ImageRef{Ref: "img:" + id, Aspect: cardsv1.Aspect_ASPECT_WIDE}
}

// videoPlayCards: a video started on a screen, live or at its height
func videoPlayCards(_ *ToolCards, r ToolResult) []Subject {
	var out struct {
		Height int  `json:"height"`
		Live   bool `json:"live"`
	}
	if !decode(r, &out) {
		return nil
	}
	return card("video-play", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		line := ""
		switch {
		case out.Live:
			line = rr.T("video.live")
		case out.Height > 0:
			line = strconv.Itoa(out.Height) + "p"
		}
		state := rr.T("video.playing")
		return done(rr, rr.T("video.title"), "film", state, cardsv1.StateKind_STATE_KIND_ON, line), rr.T("video.title") + ": " + state
	})
}

// mediaActions are media_control's actions, each its icon; the catalog words
// each as media.<action>
var mediaActions = map[string]string{
	"pause": "pause", "resume": "play", "stop": "stop", "next": "next", "previous": "previous", "seek": "forward10",
	"shuffle_on": "shuffle", "shuffle_off": "shuffle", "repeat_off": "repeat", "repeat_all": "repeat", "repeat_one": "repeat",
}

// mediaControlCards: the playback action the call took, by the schema's own
// word, never the model's text
func mediaControlCards(_ *ToolCards, r ToolResult) []Subject {
	var a struct {
		Action    string `json:"action"`
		PositionS *int   `json:"positionS"`
	}
	icon, ok := mediaActions[argAction(r.Args, &a, func() string { return a.Action })]
	if !ok {
		return nil
	}
	return card("media", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		state := rr.T("media." + a.Action)
		line := ""
		if a.Action == "seek" && a.PositionS != nil {
			line = clock(*a.PositionS)
		}
		return done(rr, rr.T("media.title"), icon, state, cardsv1.StateKind_STATE_KIND_ON, line), rr.T("media.title") + ": " + state
	})
}

// catalogItemCards: the item the agent read, with its first episodes or
// variants
func catalogItemCards(_ *ToolCards, r ToolResult) []Subject {
	type episode struct {
		Label string `json:"label"`
		Name  string `json:"name"`
	}
	type item struct {
		Title    string   `json:"title"`
		Subtitle string   `json:"subtitle"`
		Episode  *episode `json:"episode"`
	}
	var out struct {
		item
		Series   string `json:"series"`
		Children []item `json:"children"`
		Variants []item `json:"variants"`
		Artwork  struct {
			Hash string `json:"hash"`
		} `json:"artwork"`
	}
	if !decode(r, &out) || clip(out.Title, 80) == "" {
		return nil
	}
	return card("catalog", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		// an episode's card names its show, and the episode under it: «Серия 1 · <name>»
		title, sub := clip(out.Title, 80), clip(out.Subtitle, 80)
		if e := out.Episode; e != nil {
			title = cmp.Or(clip(out.Series, 80), title)
			sub = episodeLine(rr, e.Label, e.Name, title)
		}
		b := &cardsv1.CardBlock{Title: title, Subtitle: sub, Icon: "film", Tone: cardsv1.Tone_TONE_NEUTRAL}
		under := out.Children
		if len(under) == 0 {
			under = out.Variants
		}
		entries := make([][2]string, 0, len(under))
		for _, it := range under {
			if e := it.Episode; e != nil {
				entries = append(entries, [2]string{episodeLine(rr, e.Label, e.Name, ""), it.Subtitle})
				continue
			}
			entries = append(entries, [2]string{it.Title, it.Subtitle})
		}
		var body []*cardsv1.Node
		if col := listed(entries, maxListed); len(col.GetChildren()) > 0 {
			body = pcards.Nodes(col)
		}
		if hash := out.Artwork.Hash; imageIDLike(hash) {
			// the item's own poster beside what it holds
			body = pcards.Nodes(&cardsv1.Row{Gap: cardsv1.Gap_GAP_M, Children: append(pcards.Nodes(
				&cardsv1.Image{Ref: "img:" + hash, Alt: title, Aspect: cardsv1.Aspect_ASPECT_POSTER}), body...)})
		}
		b.Children = body
		return b, joined(" · ", title, sub)
	})
}

// episodeLine is an episode as a card names it: «Серия 5 · <its name>», its name left out when it is only the
// number again or the show's
func episodeLine(rr *Render, label, name, show string) string {
	line := ""
	if l := clip(label, 32); l != "" {
		line = rr.T("catalog.episode", "n", l)
	}
	if n := clip(name, 60); n != "" && n != label && n != show {
		line = joined(" · ", line, n)
	}
	return line
}

// catalogAlternativesCards: the title's other sources, each its episodes, voiceovers and quality
func catalogAlternativesCards(_ *ToolCards, r ToolResult) []Subject {
	var out struct {
		Items []struct {
			Source      string   `json:"source"`
			Episodes    int      `json:"episodes"`
			Voiceovers  []string `json:"voiceovers"`
			Quality     int      `json:"quality"`
			Current     bool     `json:"current"`
			Unavailable bool     `json:"unavailable"`
		} `json:"items"`
	}
	if !decode(r, &out) || len(out.Items) == 0 {
		return nil
	}
	return card("catalog-alts", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		entries := make([][2]string, 0, len(out.Items))
		for _, a := range out.Items {
			eps, q, now := "", "", ""
			if a.Episodes > 0 {
				eps = rr.T("catalog.episodes", "n", strconv.Itoa(a.Episodes))
			}
			if a.Quality > 0 {
				q = strconv.Itoa(a.Quality) + "p"
			}
			vo := ""
			for i, v := range a.Voiceovers {
				if i == 3 {
					vo += " +" + strconv.Itoa(len(a.Voiceovers)-3)
					break
				}
				vo = joined(", ", vo, clip(v, 24))
			}
			switch {
			case a.Current:
				now = rr.T("catalog.current")
			case a.Unavailable:
				now = rr.T("catalog.unavailable")
			}
			entries = append(entries, [2]string{a.Source, joined(" · ", eps, vo, q, now)})
		}
		b := &cardsv1.CardBlock{Title: rr.T("catalog.alternatives"), Icon: "film", Tone: cardsv1.Tone_TONE_NEUTRAL,
			Children: pcards.Nodes(listed(entries, maxListed))}
		return b, rr.T("catalog.alternatives") + ": " + strconv.Itoa(len(out.Items))
	})
}

// mediaSwitchCards: what the call switched in the episode playing, by kind
func mediaSwitchCards(_ *ToolCards, r ToolResult) []Subject {
	var a map[string]json.RawMessage
	if json.Unmarshal(r.Args, &a) != nil {
		return nil
	}
	var kinds []string
	for _, k := range []string{"source", "voiceover", "quality", "audio", "subtitles"} {
		if _, ok := a[k]; ok {
			kinds = append(kinds, k)
		}
	}
	return card("media-switch", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		words := make([]string, 0, len(kinds))
		for _, k := range kinds {
			words = append(words, rr.T("switch."+k))
		}
		state := rr.T("switch.done")
		return done(rr, rr.T("media.title"), "settings", state, cardsv1.StateKind_STATE_KIND_ON, joined(", ", words...)), state
	})
}

var itemActionIcons = map[string]string{
	"like": "like", "dislike": "dislike", "save": "bookmark", "subscribe": "subscribe", "my_list": "playlist", "watched": "check",
}

// mediaItemActionCards: the like, save or subscription the call made
func mediaItemActionCards(_ *ToolCards, r ToolResult) []Subject {
	var a struct {
		Action string `json:"action"`
		On     *bool  `json:"on"`
	}
	icon, ok := itemActionIcons[argAction(r.Args, &a, func() string { return a.Action })]
	if !ok {
		return nil
	}
	on := a.On == nil || *a.On
	return card("item-action", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		state, kind := rr.T("item."+a.Action), cardsv1.StateKind_STATE_KIND_ON
		if !on {
			state, kind = rr.T("item.undone", "what", state), cardsv1.StateKind_STATE_KIND_OFF
		}
		return done(rr, rr.T("item.title"), icon, state, kind, ""), state
	})
}

// argAction decodes args into a and answers the action it names, "" when it
// does not decode
func argAction(args json.RawMessage, a any, action func() string) string {
	if json.Unmarshal(args, a) != nil {
		return ""
	}
	return action()
}

// clock is a length as m:ss or h:mm:ss
func clock(s int) string {
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
