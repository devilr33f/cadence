package cards

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
)

// MusicItem is a music search result, or the song a queue plays, as its card shows it. Titles and names are third
// parties' text: shown, never logged.
type MusicItem struct {
	// Kind is song, album, playlist or artist; "" is a song
	Kind       string
	Title      string
	Artist     string
	Owner      string
	Album      string
	DurationS  int
	TrackCount int
	ArtworkURL string
}

// MusicResults is what the music cards read of the music tools and queue.
type MusicResults interface {
	// MusicResult is the result a music_search of the device answered by id
	MusicResult(deviceID, id string) (MusicItem, bool)
	// MusicNow is the song the device's queue plays now
	MusicNow(deviceID string) (MusicItem, bool)
	// MusicPlay plays a result of the device's search now on the device: a tile's press
	MusicPlay(ctx context.Context, deviceID, id string) error
}

// MusicPlayback is the device's music session as a play card shows it: how it plays and how far its player buffered
type MusicPlayback struct {
	// State is playing, paused, buffering or idle
	State string
	// BufferingPercent is how full the cache is towards resuming while the player waits, -1 when it does not say
	BufferingPercent int
	BufferedMs       uint64
}

// MusicLive is the music queue's session as it changes: a play card follows it while a screen leases it, so it
// shows the buffering and, once a song ends, the next one in place. Music results that are MusicLive make live
// play cards.
type MusicLive interface {
	// MusicPlayback is the device's session while it plays a song of its queue
	MusicPlayback(deviceID string) (MusicPlayback, bool)
	// MusicChanged waits until a device's session may have changed (true) or ctx ends (false)
	MusicChanged(ctx context.Context) bool
}

// musicFollow is how long a screen may follow a play card under its lease
const musicFollow = 15 * time.Minute

// what is a result as a step quotes it: a song with its artist, a list with its owner
func (m MusicItem) what() string {
	who := m.Artist
	if m.Kind == "playlist" || who == "" {
		who = m.Owner
	}
	if m.Kind == "artist" {
		who = ""
	}
	return joined(" · ", clip(m.Title, 40), clip(who, 32))
}

// meta is the result's kind word, then its album, length or track count: «Альбом · 12 треков», «Трек · 3:39»
func (m MusicItem) meta(rr *Render, withAlbum bool) string {
	kind := m.Kind
	if kind == "" {
		kind = "song"
	}
	parts := []string{rr.T("music.kind." + kind)}
	if withAlbum && kind == "song" {
		parts = append(parts, clip(m.Album, 40))
	}
	switch {
	case m.TrackCount > 0:
		parts = append(parts, counted(rr, m.TrackCount, "tracks"))
	case m.DurationS > 0:
		parts = append(parts, clock(m.DurationS))
	}
	return joined(" · ", parts...)
}

// length is a song's duration or a list's track count
func (m MusicItem) length(rr *Render) string {
	switch {
	case m.TrackCount > 0:
		return counted(rr, m.TrackCount, "tracks")
	case m.DurationS > 0:
		return clock(m.DurationS)
	}
	return ""
}

func cmpKind(k string) string {
	if k == "" {
		return "song"
	}
	return k
}

// who is the line under a result's title: the artist, the playlist's owner; none for an artist
func (m MusicItem) who() string {
	switch m.Kind {
	case "artist":
		return ""
	case "playlist":
		return clip(m.Owner, 80)
	}
	return clip(joined("", m.Artist, m.Owner), 80)
}

func (t *ToolCards) cover(rawURL string) *cardsv1.ImageRef {
	if t.Images == nil || rawURL == "" {
		return nil
	}
	id := t.Images(rawURL)
	if id == "" {
		return nil
	}
	return &cardsv1.ImageRef{Ref: "img:" + id, Aspect: cardsv1.Aspect_ASPECT_SQUARE}
}

// playArgs is music_play's arguments
type playArgs struct {
	ID    string `json:"id"`
	When  string `json:"when"`
	Radio bool   `json:"radio"`
}

// item is the result a play names, from the turn's device's results
func (t *ToolCards) item(c Call) (MusicItem, playArgs, bool) {
	var a playArgs
	if !c.args(&a) || t.Music == nil {
		return MusicItem{}, a, false
	}
	if a.ID == "current" {
		m, ok := t.Music.MusicNow(targetOf(c))
		return m, a, ok
	}
	m, ok := t.Music.MusicResult(c.DeviceID, a.ID)
	return m, a, ok
}

// targetOf is the device a call acted on: its result's target, else the turn's
func targetOf(c Call) string {
	if c.Result != nil && c.Result.Target != "" {
		return c.Result.Target
	}
	return c.DeviceID
}

func musicPlayRunning(t *ToolCards, rr *Render, c Call) string {
	m, a, ok := t.item(c)
	switch {
	case a.ID == "current":
		return rr.T("call.music_play.radio_current")
	case !ok || m.what() == "":
		return ""
	}
	return rr.T("call.music_play.loading", "what", quoted(m.what()))
}

func musicPlayDone(t *ToolCards, rr *Render, c Call) string {
	var out struct {
		Playing, Queued, Radio bool
		Pending                bool
	}
	_ = c.data(&out)
	if out.Pending {
		return musicPlayRunning(t, rr, c)
	}
	m, a, ok := t.item(c)
	key := "call.music_play.playing"
	switch {
	case out.Radio || a.ID == "current":
		key = "call.music_play.radio"
	case out.Queued:
		key = "call.music_play.queued"
	}
	what := ""
	if ok {
		what = quoted(m.what())
	}
	// a song's radio went on to the next one: the card names what plays now
	if now, has := t.nowPlaying(c); has && !out.Queued && (!ok || m.Kind == "" || m.Kind == "song") && now.Title != m.Title {
		what = quoted(now.what())
	}
	if what == "" {
		return rr.T(key + "_any")
	}
	return rr.T(key, "what", what)
}

// nowPlaying is the song the call's target plays now, while its queue plays one
func (t *ToolCards) nowPlaying(c Call) (MusicItem, bool) {
	live, ok := t.Music.(MusicLive)
	if !ok {
		return MusicItem{}, false
	}
	if _, playing := live.MusicPlayback(targetOf(c)); !playing {
		return MusicItem{}, false
	}
	now, has := t.Music.MusicNow(targetOf(c))
	return now, has && now.Title != ""
}

func musicPlayFailed(t *ToolCards, rr *Render, c Call) string {
	m, _, ok := t.item(c)
	if !ok || m.what() == "" {
		return rr.T("call.music_play.failed_any")
	}
	return rr.T("call.music_play.failed", "what", quoted(m.what()))
}

// musicPlayPending is the play loading as a media card: the cover, title, who, kind and length, and a spinner
func musicPlayPending(t *ToolCards, c Call) cardBuild {
	m, _, ok := t.item(c)
	return func(rr *Render) (*cardsv1.CardBlock, string) {
		title := t.title(rr, c)
		b := &cardsv1.CardBlock{Title: title, Icon: "music", Tone: cardsv1.Tone_TONE_NEUTRAL}
		if ok {
			b.Children = append(b.Children, t.mediaRow(rr, m, MusicItem{}))
		}
		b.Children = append(b.Children, pcards.N(&cardsv1.Progress{Amount: &cardsv1.Progress_Indeterminate{Indeterminate: true}}))
		return b, title
	}
}

// musicPlayResult is what plays: the result, the song the queue plays now when it is a list's, and its state
func musicPlayResult(t *ToolCards, c Call) []Subject {
	var out struct {
		Playing, Queued, Radio bool
		Songs                  int
	}
	if !c.data(&out) {
		return nil
	}
	m, a, ok := t.item(c)
	var now MusicItem
	if t.Music != nil && !out.Queued {
		now, _ = t.Music.MusicNow(targetOf(c))
	}
	if ok && m.Kind != "" && m.Kind != "song" && out.Songs > 0 {
		m.TrackCount = out.Songs
	}
	stateOf := func(rr *Render, pb MusicPlayback, have bool) *cardsv1.Node {
		if n := buffering(rr, pb, have); n != nil && !out.Queued {
			return n
		}
		state, kind := rr.T("music.playing"), cardsv1.StateKind_STATE_KIND_ON
		switch {
		case out.Queued:
			state, kind = rr.T("music.queued"), cardsv1.StateKind_STATE_KIND_UNKNOWN
		case have && pb.State == "paused":
			state, kind = rr.T("music.paused"), cardsv1.StateKind_STATE_KIND_OFF
		case out.Radio || a.ID == "current":
			state = rr.T("music.radio")
		}
		return pcards.N(&cardsv1.State{Label: state, Kind: kind})
	}
	if live, isLive := t.Music.(MusicLive); isLive && !out.Queued {
		return []Subject{&musicLiveCard{t: t, c: c, live: live, item: m, ok: ok, state: stateOf}}
	}
	return []Subject{&toolCard{id: CallCardID(c.Key), build: func(rr *Render) (*cardsv1.CardBlock, string) {
		title := t.title(rr, c)
		b := &cardsv1.CardBlock{Title: title, Icon: "music", Tone: cardsv1.Tone_TONE_NEUTRAL}
		switch {
		case ok:
			b.Children = append(b.Children, t.mediaRow(rr, m, now))
		case now.Title != "":
			b.Children = append(b.Children, t.mediaRow(rr, now, MusicItem{}))
		}
		b.Children = append(b.Children, stateOf(rr, MusicPlayback{BufferingPercent: -1}, false))
		return b, title
	}}}
}

// musicLiveCard is a play's card that follows the queue: the song playing now (the next one once a song ended),
// its cover and length, and the player's buffering while it loads
type musicLiveCard struct {
	t     *ToolCards
	c     Call
	live  MusicLive
	item  MusicItem
	ok    bool
	state func(rr *Render, pb MusicPlayback, have bool) *cardsv1.Node

	mu   sync.Mutex
	last string
}

func (m *musicLiveCard) ID() string { return CallCardID(m.c.Key) }

// Follow is how long a screen may follow it after it was shown
func (m *musicLiveCard) Follow() time.Duration { return musicFollow }

func (m *musicLiveCard) now() (MusicItem, MusicPlayback, bool, string) {
	var now MusicItem
	if m.t.Music != nil {
		now, _ = m.t.Music.MusicNow(targetOf(m.c))
	}
	pb, have := m.live.MusicPlayback(targetOf(m.c))
	fp := strings.Join([]string{now.Title, now.Artist, now.ArtworkURL, pb.State, strconv.Itoa(pb.BufferingPercent),
		strconv.FormatUint(pb.BufferedMs/1000, 10), strconv.FormatBool(have)}, "\x00")
	return now, pb, have, fp
}

// Changed waits for the song, its state or its buffering to differ from what the card last showed.
func (m *musicLiveCard) Changed(ctx context.Context) bool {
	for {
		_, _, _, fp := m.now()
		m.mu.Lock()
		last := m.last
		m.mu.Unlock()
		if fp != last {
			return true
		}
		if !m.live.MusicChanged(ctx) {
			return false
		}
	}
}

func (m *musicLiveCard) Render(_ context.Context, rr *Render) (*cardsv1.Card, error) {
	now, pb, have, fp := m.now()
	m.mu.Lock()
	m.last = fp
	m.mu.Unlock()
	title := m.t.title(rr, m.c)
	b := &cardsv1.CardBlock{Title: title, Icon: "music", Tone: cardsv1.Tone_TONE_NEUTRAL}
	switch {
	case m.ok && m.item.Kind != "" && m.item.Kind != "song":
		b.Children = append(b.Children, m.t.mediaRow(rr, m.item, now))
	case now.Title != "":
		// a song, or the next one its radio played after it
		b.Children = append(b.Children, m.t.mediaRow(rr, now, MusicItem{}))
	case m.ok:
		b.Children = append(b.Children, m.t.mediaRow(rr, m.item, MusicItem{}))
	}
	b.Children = append(b.Children, m.state(rr, pb, have))
	return pcards.NewCard("", 0, title, b), nil
}

// buffering is the player filling its cache: how full, how far ahead, as a bar and its line; nil while it plays
func buffering(rr *Render, pb MusicPlayback, have bool) *cardsv1.Node {
	if !have || (pb.State != "buffering" && pb.BufferingPercent < 0) {
		return nil
	}
	p := &cardsv1.Progress{Amount: &cardsv1.Progress_Indeterminate{Indeterminate: true}}
	parts := []string{rr.T("music.loading")}
	if pb.BufferingPercent >= 0 {
		pct := min(pb.BufferingPercent, 100)
		p.Amount = &cardsv1.Progress_Fraction{Fraction: float64(pct) / 100}
		parts[0] = rr.T("music.loading_percent", "n", strconv.Itoa(pct))
	}
	if s := pb.BufferedMs / 1000; s > 0 {
		parts = append(parts, rr.T("music.buffered", "n", strconv.FormatUint(s, 10)))
	}
	p.Label = clip(joined(" · ", parts...), 80)
	return pcards.N(p)
}

// mediaRow is a result as a media line: its cover, title, who and kind; for a list, the song it plays now
func (t *ToolCards) mediaRow(rr *Render, m, now MusicItem) *cardsv1.Node {
	lines := &cardsv1.Column{Gap: cardsv1.Gap_GAP_S, Children: pcards.Nodes(
		&cardsv1.Text{Text: clip(m.Title, 280), Style: cardsv1.TextStyle_TEXT_STYLE_BODY})}
	if who := m.who(); who != "" {
		lines.Children = append(lines.Children, pcards.N(&cardsv1.Text{Text: who, Style: cardsv1.TextStyle_TEXT_STYLE_META}))
	}
	lines.Children = append(lines.Children, pcards.N(&cardsv1.Text{Text: clip(m.meta(rr, true), 280), Style: cardsv1.TextStyle_TEXT_STYLE_META}))
	art := m.ArtworkURL
	if now.Title != "" && m.Kind != "" && m.Kind != "song" {
		lines.Children = append(lines.Children, pcards.N(&cardsv1.Text{
			Text:  clip(rr.T("music.now", "what", joined(" — ", clip(now.Title, 60), clip(now.Artist, 40))), 280),
			Style: cardsv1.TextStyle_TEXT_STYLE_BODY}))
		if now.ArtworkURL != "" {
			art = now.ArtworkURL
		}
	}
	row := &cardsv1.Row{Gap: cardsv1.Gap_GAP_M}
	if img := t.cover(art); img != nil {
		row.Children = append(row.Children, pcards.N(&cardsv1.Image{Ref: img.GetRef(), Alt: clip(m.Title, 120), Aspect: cardsv1.Aspect_ASPECT_SQUARE}))
	}
	row.Children = append(row.Children, pcards.N(lines))
	return pcards.N(row)
}

// musicSearchResult is the results as tiles, each its cover, title and what it is, a press playing it; without
// covers or presses, the list of entries
func musicSearchResult(t *ToolCards, c Call) []Subject {
	var out struct {
		Results []struct {
			ID string `json:"id"`
			musicFound
		} `json:"results"`
	}
	if !c.data(&out) || len(out.Results) == 0 {
		return nil
	}
	var query string
	var a struct {
		Query string `json:"query"`
	}
	if c.args(&a) {
		query = a.Query
	}
	type tile struct {
		id  string
		m   MusicItem
		img *cardsv1.ImageRef
	}
	tiles := make([]tile, 0, len(out.Results))
	for _, r := range out.Results {
		m := MusicItem{Kind: r.Kind, Title: r.Title, Artist: r.Artist, Owner: r.Owner, Album: r.Album, DurationS: r.DurationS, TrackCount: r.TrackCount}
		if t.Music != nil {
			if full, ok := t.Music.MusicResult(c.DeviceID, r.ID); ok {
				m = full
			}
		}
		tiles = append(tiles, tile{id: r.ID, m: m, img: t.cover(m.ArtworkURL)})
		if len(tiles) == pcards.MaxListItems {
			break
		}
	}
	whole := t.Music != nil
	for _, x := range tiles {
		whole = whole && x.img != nil && clip(x.m.Title, 80) != ""
	}
	if !whole {
		r := *c.Result
		return musicSearchCards(t, r)
	}
	dev := c.DeviceID
	return []Subject{&toolCard{id: CallCardID(c.Key), build: func(rr *Render) (*cardsv1.CardBlock, string) {
		list := &cardsv1.PagedList{}
		for _, x := range tiles {
			id := x.id
			// what it is rides on the cover; under the title whose it is and how long
			sub, badges := x.m.meta(rr, false), []*cardsv1.Badge(nil)
			if x.m.Kind != "artist" {
				badges = []*cardsv1.Badge{{Kind: cardsv1.BadgeKind_BADGE_KIND_PROVIDER, Text: clip(rr.T("music.kind."+cmpKind(x.m.Kind)), 24)}}
				sub = joined(" · ", x.m.who(), x.m.length(rr))
			}
			list.Items = append(list.Items, pcards.N(&cardsv1.MediaTile{
				Image: x.img, Title: clip(x.m.Title, 80), Subtitle: clip(sub, 80), Badges: badges,
				Action: rr.Bind(Binding{Idempotent: true, Run: func(ctx context.Context, _ Act) Outcome {
					if err := t.Music.MusicPlay(ctx, dev, id); err != nil {
						return Outcome{Refused: "failed"}
					}
					return Outcome{Record: &Record{Tool: "music_play", Args: map[string]string{"id": id}, Result: map[string]bool{"playing": true}}}
				}}),
			}))
		}
		b := &cardsv1.CardBlock{Title: rr.T("music.results"), Subtitle: clip(query, 80), Icon: "music",
			Tone: cardsv1.Tone_TONE_NEUTRAL, Children: pcards.Nodes(list)}
		return b, rr.T("search.fallback", "count", strconv.Itoa(len(tiles)))
	}}}
}

// mediaAction is media_control's action, by the schema's own word
func mediaAction(c Call) string {
	var a struct {
		Action string `json:"action"`
	}
	if !c.args(&a) {
		return ""
	}
	if _, ok := mediaActions[a.Action]; !ok {
		return ""
	}
	return a.Action
}

func mediaRunning(_ *ToolCards, rr *Render, c Call) string {
	if act := mediaAction(c); act != "" {
		return rr.T("call.media." + act)
	}
	return ""
}

// mediaDone names what plays after a skip or a resume; else what the action did
func mediaDone(t *ToolCards, rr *Render, c Call) string {
	act := mediaAction(c)
	switch act {
	case "next", "previous", "resume":
		if now, ok := t.nowPlaying(c); ok {
			return rr.T("call.music_play.playing", "what", quoted(now.what()))
		}
	case "":
		return ""
	}
	return rr.T("media." + act)
}

// mediaResult: a skip or a resume of the queue is the song it plays, followed as it loads and goes on; any other
// action its own card
func mediaResult(t *ToolCards, c Call) []Subject {
	switch mediaAction(c) {
	case "next", "previous", "resume":
		if live, ok := t.Music.(MusicLive); ok {
			if _, playing := live.MusicPlayback(targetOf(c)); playing {
				return []Subject{&musicLiveCard{t: t, c: c, live: live, state: func(rr *Render, pb MusicPlayback, have bool) *cardsv1.Node {
					if n := buffering(rr, pb, have); n != nil {
						return n
					}
					if have && pb.State == "paused" {
						return pcards.N(&cardsv1.State{Label: rr.T("music.paused"), Kind: cardsv1.StateKind_STATE_KIND_OFF})
					}
					return pcards.N(&cardsv1.State{Label: rr.T("music.playing"), Kind: cardsv1.StateKind_STATE_KIND_ON})
				}}}
			}
		}
	}
	r := *c.Result
	return mediaControlCards(t, r)
}
