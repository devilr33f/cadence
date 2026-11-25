package cards

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	"git.shino.boo/devilreef/cadence/server/internal/playprogress"
)

// the catalog's cards in a turn: a catalog_search's titles as poster tiles (the showcase's), and a catalog_play's
// card, live until the screen plays it or gives up. Titles and labels are third parties' text: shown, never logged.

// CatalogTile is a found title as its tile shows it
type CatalogTile struct {
	Ref   string
	Title string
	// Kind is the catalog's item kind (series, movie, title_group…)
	Kind     string
	Year     int
	Episodes int
	// Score is out of 10, 0 for none
	Score float64
	// Artwork is the image proxy's id of its poster, "" for none
	Artwork     string
	Unavailable bool
}

// CatalogOpen opens a title's page on the screen that pressed its tile
type CatalogOpen func(ctx context.Context, deviceID, ref string) error

// CatalogFound is the card of one catalog_search call (key names the call): its titles as poster tiles, OK on one
// opens its page when open is set. Nil without a title.
func CatalogFound(key string, tiles []CatalogTile, open CatalogOpen) Subject {
	var kept []CatalogTile
	for _, t := range tiles {
		if clip(t.Title, 120) != "" {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	if len(kept) > pcards.MaxListItems {
		kept = kept[:pcards.MaxListItems]
	}
	return &catalogFound{key: key, tiles: kept, open: open}
}

type catalogFound struct {
	key   string
	tiles []CatalogTile
	open  CatalogOpen
}

func (f *catalogFound) ID() string { return OpaqueID("catalog-found", f.key) }

// ForTool: the catalog search's own tiles card
func (f *catalogFound) ForTool() string { return "catalog_search" }

func (f *catalogFound) Render(_ context.Context, r *Render) (*cardsv1.Card, error) {
	list := &cardsv1.PagedList{}
	for _, t := range f.tiles {
		tile := &cardsv1.MediaTile{
			Title: clip(t.Title, 120), Subtitle: clip(tileLine(r, t), 120),
			Image: &cardsv1.ImageRef{Alt: clip(t.Title, 120), Aspect: cardsv1.Aspect_ASPECT_POSTER},
		}
		if imageIDLike(t.Artwork) {
			tile.Image.Ref = "img:" + t.Artwork
		} else {
			// a tile needs its image: one the proxy never issued leaves the slot's fill
			tile.Image.Ref = "img:none"
		}
		// a tile needs its action: OK opens the title's page, which says why one without a source cannot play
		tile.Action = r.Bind(f.openBinding(t.Ref))
		list.Items = append(list.Items, pcards.N(tile))
	}
	block := &cardsv1.CardBlock{Title: r.T("catalog.found"), Icon: "film", Tone: cardsv1.Tone_TONE_NEUTRAL,
		Children: pcards.Nodes(list)}
	return pcards.NewCard("", 0, r.T("catalog.found_fallback", "count", strconv.Itoa(len(f.tiles))), block), nil
}

func (f *catalogFound) openBinding(ref string) Binding {
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

// tileLine is what fits under a poster: the year, the episodes of a series (else its kind), the score, and why one
// cannot play: «2024 · 12 эп. · ★8,3», «2024 · Фильм · ★7,1»
func tileLine(r *Render, t CatalogTile) string {
	var parts []string
	if t.Year > 0 {
		parts = append(parts, strconv.Itoa(t.Year))
	}
	if t.Unavailable {
		return joined(" · ", append(parts, r.T("catalog.unavailable"))...)
	}
	if t.Episodes > 1 {
		parts = append(parts, r.T("catalog.episodes", "n", strconv.Itoa(t.Episodes)))
	} else if k := r.T("catalog.kind." + t.Kind); t.Kind != "" && k != "catalog.kind."+t.Kind {
		parts = append(parts, k)
	}
	if t.Score > 0 && t.Score <= 10 {
		s := strconv.FormatFloat(t.Score, 'f', 1, 64)
		if strings.HasPrefix(strings.ToLower(r.Lang), "ru") {
			s = strings.ReplaceAll(s, ".", ",")
		}
		parts = append(parts, "★"+s)
	}
	return joined(" · ", parts...)
}

// imageIDLike is the image proxy's id shape (images.IDPattern)
func imageIDLike(s string) bool {
	if len(s) != 24 {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// PlayState is where a catalog play stands on its screen
type PlayState int

const (
	// PlayFetching: the catalog resolves what to play (the call's fetch, music_play's resolving)
	PlayFetching PlayState = iota
	// PlayStarting: the screen took the play and has not reported it yet
	PlayStarting
	// PlayBuffering: the screen's player fills its cache
	PlayBuffering
	// PlayPlaying: it plays (or was paused once it had)
	PlayPlaying
	// PlayFailed: the session ended or never came before it played
	PlayFailed
)

// CatalogPlaying is what a catalog_play started, as its card shows it
type CatalogPlaying struct {
	Title       string
	Artwork     string
	Episode     string
	EpisodeName string
	// Provider is the catalog provider (torrplayer), Source its source (anime365)
	Provider, Source string
	Voiceover        string
	Quality          string
	// Screen is the name of the device it plays on
	Screen string
}

// CatalogPlay is a catalog_play's card: live, from its call's fetch (what the catalog
// cached of the ref) through the screen's own reports, one card in place.
type CatalogPlay struct {
	key string

	mu    sync.Mutex
	p     CatalogPlaying
	state PlayState
	// stage is the fetch's latest resolve stage (yt-dlp's, where the source runs through it), set while fetching
	stage playprogress.Event
	// code is the call's error code once it failed the fetch
	code    string
	changed chan struct{}
	// version grows with each change, rendered is the one last drawn: a change no screen saw yet is news to Changed
	version, rendered uint64
}

// NewCatalogPlay is the card of one catalog_play call (key names the call), starting.
func NewCatalogPlay(key string, p CatalogPlaying) *CatalogPlay {
	return &CatalogPlay{key: key, p: p, state: PlayStarting, changed: make(chan struct{})}
}

// NewCatalogFetch is the card of one catalog_play call from its fetch on (key names the
// call), with what the catalog cached of the ref: the fetch shows while it resolves, the
// same card then carries what started.
func NewCatalogFetch(key string, p CatalogPlaying) *CatalogPlay {
	return &CatalogPlay{key: key, p: p, state: PlayFetching, changed: make(chan struct{})}
}

func (c *CatalogPlay) ID() string { return OpaqueID("catalog-play", c.key) }

// ForTool: the catalog play's own live card, which replaces the call's pending card
func (c *CatalogPlay) ForTool() string { return "catalog_play" }

// Stage sets the fetch's resolve stage (yt-dlp's, where the source runs through it); a
// fetch past its first stage keeps it. It answers whether the card changed.
func (c *CatalogPlay) Stage(ev playprogress.Event) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != PlayFetching || ev.Stage == "" || ev.Done || ev.Failed {
		return false
	}
	c.stage = ev
	c.bump()
	return true
}

// Start sets what the fetch started, as the screen will play it: the card keeps its id,
// so the screen replaces its fetch in place.
func (c *CatalogPlay) Start(p CatalogPlaying) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != PlayFetching {
		return
	}
	c.p, c.state, c.stage = p, PlayStarting, playprogress.Event{}
	c.bump()
}

// Fail ends the card with the call's error code, in words on the card.
func (c *CatalogPlay) Fail(code string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == PlayPlaying || c.state == PlayFailed {
		return
	}
	c.state, c.code = PlayFailed, clip(code, 40)
	c.bump()
}

// Set moves the play to state; a played or failed play stays as it is, and a state it holds already changes
// nothing. It answers whether the card changed.
func (c *CatalogPlay) Set(state PlayState) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if state == c.state || c.state == PlayPlaying || c.state == PlayFailed {
		return false
	}
	c.state = state
	c.bump()
	return true
}

// State is where the play stands.
func (c *CatalogPlay) State() PlayState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// SetEpisodeName names the episode once the catalog answered it.
func (c *CatalogPlay) SetEpisodeName(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if name = clip(name, 80); name == "" || name == c.p.EpisodeName {
		return
	}
	c.p.EpisodeName = name
	c.bump()
}

func (c *CatalogPlay) bump() {
	c.version++
	close(c.changed)
	c.changed = make(chan struct{})
}

// Changed waits for the card to show something its last render did not.
func (c *CatalogPlay) Changed(ctx context.Context) bool {
	c.mu.Lock()
	ch, news := c.changed, c.version != c.rendered
	c.mu.Unlock()
	if news {
		return true
	}
	select {
	case <-ch:
		return true
	case <-ctx.Done():
		return false
	}
}

// Follow is how long a screen may follow the card under its lease after it was shown: past a slow torrent's start
func (c *CatalogPlay) Follow() time.Duration { return 2 * time.Minute }

func (c *CatalogPlay) Render(_ context.Context, r *Render) (*cardsv1.Card, error) {
	c.mu.Lock()
	p, state, stage, code := c.p, c.state, c.stage, c.code
	c.rendered = c.version
	c.mu.Unlock()
	label, kind, tone := r.T("catalog.starting"), cardsv1.StateKind_STATE_KIND_UNKNOWN, cardsv1.Tone_TONE_NEUTRAL
	fetching := false
	switch state {
	case PlayFetching:
		// the fetch's own word, music_play's resolving stage: «Ищу источник · ищу формат»
		fetching = true
		label = clip(mediaResolveStage(r, "", playprogress.Update{Event: stage}), 100)
	case PlayBuffering:
		label = r.T("catalog.buffering")
	case PlayPlaying:
		label, kind = r.T("catalog.playing"), cardsv1.StateKind_STATE_KIND_ON
	case PlayFailed:
		label, kind, tone = r.T("catalog.failed"), cardsv1.StateKind_STATE_KIND_UNAVAILABLE, cardsv1.Tone_TONE_WARNING
		if code != "" {
			// the call's code in words, as a failed step sets it
			label = clip(reason(r, code), 100)
		}
	}
	title := clip(p.Title, 80)
	if title == "" || title == clip(p.Episode, 80) {
		// a bare episode number is no title: the show's name, else what the card is
		title = r.T("catalog.title")
	}
	sub := episodeLine(r, p.Episode, p.EpisodeName, title)
	info := pcards.Nodes(&cardsv1.State{Label: clip(label, 100), Kind: kind})
	if !fetching {
		kv := rows([][2]string{
			{r.T("catalog.screen"), p.Screen},
			{r.T("catalog.source"), joined(" · ", clip(p.Provider, 32), clip(p.Source, 40))},
			{r.T("catalog.voiceover"), p.Voiceover},
			{r.T("catalog.quality"), p.Quality},
		})
		if kv != nil {
			info = append(info, pcards.N(kv))
		}
	} else if p.Screen != "" {
		// while it fetches, the screen it is for is all the card knows
		info = append(info, pcards.N(rows([][2]string{{r.T("catalog.screen"), p.Screen}})))
	}
	b := &cardsv1.CardBlock{Title: title, Subtitle: clip(sub, 80), Icon: "tv", Tone: tone}
	if imageIDLike(p.Artwork) {
		poster := &cardsv1.Image{Ref: "img:" + p.Artwork, Alt: title, Aspect: cardsv1.Aspect_ASPECT_POSTER}
		b.Children = pcards.Nodes(&cardsv1.Row{Gap: cardsv1.Gap_GAP_M, Children: pcards.Nodes(poster,
			&cardsv1.Column{Gap: cardsv1.Gap_GAP_S, Children: info})})
	} else {
		b.Children = info
	}
	if fetching {
		// the fetch's own bar: its stage's share where the source reports one, indeterminate until then
		progress := &cardsv1.Progress{Amount: &cardsv1.Progress_Indeterminate{Indeterminate: true}}
		if stage.Stage != "" {
			progress.Amount = &cardsv1.Progress_Fraction{Fraction: mediaStageFraction(stage.Stage)}
		}
		b.Children = append(b.Children, pcards.N(progress))
	}
	fallback := joined(" · ", title, sub, label)
	if p.Screen != "" {
		fallback += " — " + clip(p.Screen, 40)
	}
	return pcards.NewCard("", 0, clip(fallback, 280), b), nil
}
