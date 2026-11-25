package cards

import (
	"context"
	"strconv"
	"strings"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
)

// the youtube tools' cards: what only YouTube answers. A search's videos are wide tiles
// OK plays on the pressing screen (VideosFound, the catalog's tiles' counterpart), its
// channels and playlists lines; a browse's channel or playlist heads its children in one
// list. The catalog's poster tiles stay catalog_search's own.

// ytItem is one of youtube_search's and youtube_browse's answers as the tool wrote it
// (tools.statView): titles are their uploaders'
type ytItem struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Channel  *struct {
		Title string `json:"title"`
	} `json:"channel"`
	DurationS int    `json:"durationS"`
	Kind      string `json:"kind"`
	Upcoming  bool   `json:"upcoming"`
}

// ytLine is an item's meta: whose it is and how long, «Markiplier · 17:44»
func ytLine(rr *Render, it ytItem) string {
	length := ""
	switch {
	case it.Kind == "live":
		length = rr.T("video.live")
	case it.Upcoming:
		length = rr.T("youtube.upcoming")
	case it.DurationS > 0:
		length = clock(it.DurationS)
	}
	whose := it.Subtitle
	if it.Channel != nil {
		whose = it.Channel.Title
	}
	return joined(" · ", clip(whose, 80), length)
}

func ytEntries(rr *Render, items []ytItem) [][2]string {
	entries := make([][2]string, 0, len(items))
	for _, it := range items {
		if t := clip(it.Title, 280); t != "" {
			entries = append(entries, [2]string{t, ytLine(rr, it)})
		}
	}
	return entries
}

// youtubeSearchCards: the search's answers, each its title, whose it is and how long
func youtubeSearchCards(_ *ToolCards, r ToolResult) []Subject {
	var out struct {
		Items []ytItem `json:"items"`
	}
	if !decode(r, &out) || len(out.Items) == 0 {
		return nil
	}
	return card("youtube", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		b := &cardsv1.CardBlock{Title: rr.T("youtube.results"), Icon: "film", Tone: cardsv1.Tone_TONE_NEUTRAL,
			Children: pcards.Nodes(listed(ytEntries(rr, out.Items), maxListed))}
		return b, rr.T("search.fallback", "count", strconv.Itoa(len(out.Items)))
	})
}

// youtubeBrowseCards: what was opened (a channel, a playlist, a video) heading its
// children, one list
func youtubeBrowseCards(_ *ToolCards, r ToolResult) []Subject {
	var out struct {
		Item     ytItem   `json:"item"`
		Children []ytItem `json:"children"`
	}
	if !decode(r, &out) || clip(out.Item.Title, 80) == "" {
		return nil
	}
	all := append([]ytItem{out.Item}, out.Children...)
	return card("youtube-browse", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		b := &cardsv1.CardBlock{Title: clip(out.Item.Title, 80), Icon: "film", Tone: cardsv1.Tone_TONE_NEUTRAL,
			Children: pcards.Nodes(listed(ytEntries(rr, all), maxListed))}
		return b, joined(" · ", clip(out.Item.Title, 80), rr.T("youtube.browse",
			"count", strconv.Itoa(len(out.Children))))
	})
}

// VideoTile is a found video as its tile shows it: third parties' text, memory-only
type VideoTile struct {
	Ref, Title, Channel string
	// Views is its public count, 0 for none
	Views int64
	// DurationS is 0 when unknown
	DurationS int
	Live      bool
	// Artwork is the image proxy's id of its thumbnail, "" for none
	Artwork string
}

// VideoRow is a found channel or playlist, a line under the search's tiles
type VideoRow struct {
	Title, Handle string
	// Subscribers and Videos are its public counts, 0 for none
	Subscribers int64
	Videos      int
}

// VideoOpen plays a found video on the screen that pressed its tile: a deliberate press, the play the screen's own
// remote makes
type VideoOpen func(ctx context.Context, deviceID, ref string) error

// VideosFound is the card of one youtube_search call (key names the call): its videos as wide tiles, OK on one
// playing it on the pressing screen, its channels and playlists as lines below. Nil without either.
func VideosFound(key string, tiles []VideoTile, rows []VideoRow, open VideoOpen) Subject {
	var kept []VideoTile
	for _, t := range tiles {
		if clip(t.Title, 120) != "" && t.Ref != "" {
			kept = append(kept, t)
		}
	}
	if len(kept) > pcards.MaxListItems {
		kept = kept[:pcards.MaxListItems]
	}
	var lines []VideoRow
	for _, r := range rows {
		if clip(r.Title, 120) != "" {
			lines = append(lines, r)
		}
	}
	if len(lines) > maxListed {
		lines = lines[:maxListed]
	}
	if len(kept) == 0 && len(lines) == 0 {
		return nil
	}
	return &videosFound{key: key, tiles: kept, rows: lines, open: open}
}

type videosFound struct {
	key   string
	tiles []VideoTile
	rows  []VideoRow
	open  VideoOpen
}

func (f *videosFound) ID() string { return OpaqueID("videos-found", f.key) }

// ForTool: the youtube search's own tiles card
func (f *videosFound) ForTool() string { return "youtube_search" }

func (f *videosFound) Render(_ context.Context, r *Render) (*cardsv1.Card, error) {
	var children []*cardsv1.Node
	if len(f.tiles) > 0 {
		list := &cardsv1.PagedList{}
		for _, t := range f.tiles {
			tile := &cardsv1.MediaTile{Title: clip(t.Title, 120), Subtitle: clip(videoTileLine(r, t), 120),
				Image: &cardsv1.ImageRef{Alt: clip(t.Title, 120), Aspect: cardsv1.Aspect_ASPECT_WIDE}}
			if imageIDLike(t.Artwork) {
				tile.Image.Ref = "img:" + t.Artwork
			} else {
				// a tile needs its image: one the proxy never issued leaves the slot's fill
				tile.Image.Ref = "img:none"
			}
			tile.Action = r.Bind(f.playBinding(t.Ref))
			list.Items = append(list.Items, pcards.N(tile))
		}
		children = append(children, pcards.N(list))
	}
	if len(f.rows) > 0 {
		entries := make([][2]string, 0, len(f.rows))
		for _, row := range f.rows {
			meta := joined(" · ", clip(row.Handle, 40), viewsLabel(r, row.Subscribers, "subscribers"),
				videoCountLabel(r, row.Videos))
			entries = append(entries, [2]string{clip(row.Title, 120), meta})
		}
		children = append(children, pcards.N(&cardsv1.Column{Gap: cardsv1.Gap_GAP_M, Children: pcards.Nodes(listed(entries, maxListed))}))
	}
	block := &cardsv1.CardBlock{Title: r.T("youtube.results"), Icon: "film", Tone: cardsv1.Tone_TONE_NEUTRAL,
		Children: children}
	count := strconv.Itoa(len(f.tiles) + len(f.rows))
	return pcards.NewCard("", 0, r.T("search.fallback", "count", count), block), nil
}

func (f *videosFound) playBinding(ref string) Binding {
	return Binding{Idempotent: true, Run: func(ctx context.Context, a Act) Outcome {
		if f.open == nil || ref == "" {
			return Outcome{Refused: "unavailable"}
		}
		if err := f.open(ctx, a.DeviceID, ref); err != nil {
			return Outcome{Refused: "unavailable"}
		}
		return Outcome{}
	}}
}

// videoTileLine is a video's line under its title: whose it is, its count, its length, «Markiplier · 133 тыс.
// просмотров · 46:00»
func videoTileLine(r *Render, t VideoTile) string {
	length := ""
	switch {
	case t.Live:
		length = r.T("video.live")
	case t.DurationS > 0:
		length = clock(t.DurationS)
	}
	return joined(" · ", clip(t.Channel, 60), viewsLabel(r, t.Views, "views"), length)
}

// viewsLabel is a public count in words: «133 тыс. просмотров», «1.2M views», «39M subscribers». The word is the
// many form, which every compacted count takes.
func viewsLabel(r *Render, n int64, noun string) string {
	switch {
	case n <= 0:
		return ""
	case n < 1000:
		// a small count keeps its own plural form
		return counted(r, int(n), noun)
	}
	ru := strings.HasPrefix(strings.ToLower(r.Lang), "ru")
	ruWord := " просмотров"
	switch noun {
	case "subscribers":
		ruWord = " подписчиков"
	case "videos":
		ruWord = " видео"
	}
	val, unit, word := float64(n)/1000, " тыс.", ruWord
	if n >= 1000000 {
		val, unit = float64(n)/1000000, " млн"
	}
	if !ru {
		val, unit, word = float64(n)/1000, "K", " "+noun
		if n >= 1000000 {
			val, unit, word = float64(n)/1000000, "M", " "+noun
		}
	}
	s := strconv.FormatFloat(val, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	if ru {
		s = strings.ReplaceAll(s, ".", ",")
	}
	return s + unit + word
}

// videoCountLabel is a channel's video count, «2,4 тыс. видео»
func videoCountLabel(r *Render, n int) string {
	if n <= 0 {
		return ""
	}
	if n < 1000 {
		return counted(r, n, "videos")
	}
	return viewsLabel(r, int64(n), "videos")
}

// screenStateCards: what a screen_state read — its route, its page, what plays there. The
// screen itself shows its state; this is the turn's card of what was read, for the room's
// other screens and the drawer.
func screenStateCards(_ *ToolCards, r ToolResult) []Subject {
	var s struct {
		Route string `json:"route"`
		Page  *struct {
			Title string `json:"title"`
		} `json:"page"`
		Focused *struct {
			Title string `json:"title"`
		} `json:"focused"`
		Player *struct {
			Item struct {
				Title string `json:"title"`
			} `json:"item"`
			PositionS int `json:"positionS"`
		} `json:"player"`
	}
	if !decode(r, &s) || s.Route == "" {
		return nil
	}
	return card("screen-state", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		name := ""
		if s.Page != nil {
			name = clip(s.Page.Title, 80)
		}
		if name == "" && s.Player != nil {
			name = clip(s.Player.Item.Title, 80)
		}
		at, playing := "", ""
		if s.Player != nil {
			playing = clip(s.Player.Item.Title, 80)
			if s.Player.PositionS > 0 {
				at = " · " + clock(s.Player.PositionS)
			}
		}
		page, focused := "", ""
		if s.Page != nil {
			page = clip(s.Page.Title, 80)
		}
		if s.Focused != nil {
			focused = clip(s.Focused.Title, 80)
		}
		info := pcards.Nodes(&cardsv1.State{Label: clip(s.Route, 40), Kind: cardsv1.StateKind_STATE_KIND_UNKNOWN})
		if kv := rows([][2]string{
			{rr.T("screen.page"), page},
			{rr.T("screen.focused"), focused},
			{rr.T("catalog.playing"), playing + at},
		}); kv != nil {
			info = append(info, pcards.N(kv))
		}
		b := &cardsv1.CardBlock{Title: rr.T("screen.read"), Subtitle: name, Icon: "tv", Tone: cardsv1.Tone_TONE_NEUTRAL,
			Children: info}
		return b, joined(" · ", rr.T("screen.read"), name, s.Route)
	})
}
