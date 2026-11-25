package remote

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
)

// the transport a card offers, in its order, by media.player's names
var transport = []string{"previous", "pause", "resume", "next"}

// the card feature a screen draws a running position with
const timelineFeature = "slider.timeline"

func (s *Service) panel(caller string, es []entry, timeline bool) *cards.Panel {
	p := &cards.Panel{}
	for _, e := range es {
		p.Cards = append(p.Cards, &remoteCard{s: s, caller: caller, id: e.ID, first: &e, timeline: timeline})
	}
	if len(p.Cards) == 0 {
		p.Cards = []cards.Subject{emptyPanel{}}
	}
	return p
}

// timeline says the caller's screen draws a slider's timeline: its hello listed the feature
func (s *Service) timeline(ctx context.Context, caller string) bool {
	return s.d.CardFeatures != nil && slices.Contains(s.d.CardFeatures(ctx, caller), timelineFeature)
}

// remoteCard is one device's (or group's) card; it renders what plays there now
type remoteCard struct {
	s      *Service
	caller string
	// the entry's id: its group's or its device's
	id string
	// the entry the page was built from, for its first render; a later one (after an action) reads it again
	first *entry
	// the screen draws a position slider
	timeline bool
}

func (c *remoteCard) ID() string { return "remote-" + c.id }

// current is the card's entry as it stands, false once nothing plays there
func (c *remoteCard) current(ctx context.Context) (entry, bool) {
	if e := c.first; e != nil {
		c.first = nil
		return *e, true
	}
	es, err := c.s.entries(ctx, c.caller, true)
	if err != nil {
		return entry{}, false
	}
	for _, e := range es {
		if e.ID == c.id {
			return e, true
		}
	}
	return entry{}, false
}

// Render: the name with its room and state; the cover beside what plays; the position (a seek where the session lists
// it) or «live»; the transport; a volume slider per device while focusables last; «play here too» or «stop here»
func (c *remoteCard) Render(ctx context.Context, r *cards.Render) (*cardsv1.Card, error) {
	e, ok := c.current(ctx)
	if !ok {
		text := r.T("remote.gone")
		return pcards.NewCard("", 0, text, &cardsv1.CardBlock{Title: clip(r.T("remote.title"), 80), Icon: "music",
			Tone: cardsv1.Tone_TONE_NEUTRAL, Children: pcards.Nodes(&cardsv1.Text{Text: text, Style: cardsv1.TextStyle_TEXT_STYLE_META})}), nil
	}
	state := r.T("remote." + e.State)
	line := state
	if e.Here {
		line = r.T("remote.here")
	}
	if room := clip(e.Room, 60); room != "" {
		line = room + " · " + lowerFirst(line)
	}
	b := &cardsv1.CardBlock{Title: clip(cmpOr(e.Name, r.T("remote.title")), 80), Subtitle: clip(line, 80), Icon: "music",
		Tone: cardsv1.Tone_TONE_NEUTRAL}
	if row := nowPlaying(r, e); row != nil {
		b.Children = append(b.Children, pcards.N(row))
	}
	buttons := c.transport(r, e)
	// the transport, «play here too» or «stop here», the seek: the volume sliders take what is left
	focus := len(buttons) + 1
	if sl := c.position(r, e); sl != nil {
		b.Children = append(b.Children, pcards.N(sl))
		if sl.GetAction() != nil {
			focus++
		}
	} else if live(e) {
		b.Children = append(b.Children, pcards.N(&cardsv1.Text{Text: r.T("remote.live"), Style: cardsv1.TextStyle_TEXT_STYLE_META}))
	}
	if len(buttons) > 0 {
		row := &cardsv1.Row{Gap: cardsv1.Gap_GAP_S}
		for _, bt := range buttons {
			row.Children = append(row.Children, pcards.N(bt))
		}
		b.Children = append(b.Children, pcards.N(row))
	}
	for _, m := range e.members {
		if m.level < 0 || focus == pcards.MaxFocusables {
			continue
		}
		focus++
		b.Children = append(b.Children, pcards.N(&cardsv1.Slider{
			Value: float64(m.level), Min: 0, Max: 100, Step: 5, Unit: "%", Label: clip(cmpOr(m.name, r.T("remote.title")), 40),
			ValueLabel: strconv.Itoa(m.level) + " %", Action: r.Bind(c.s.volumeBinding(c.caller, m.id)),
		}))
	}
	if e.Here {
		b.Actions = []*cardsv1.Button{{Label: r.T("remote.leave"), Icon: "close", Action: r.Bind(c.s.leaveBinding(c.caller))}}
	} else {
		b.Actions = []*cardsv1.Button{{Label: r.T("remote.join"), Icon: "share_to_speaker",
			Action: r.Bind(c.s.joinBinding(c.caller, c.id))}}
	}
	return pcards.NewCard("", 0, clip(r.T("remote.fallback", "name", e.Name, "state", state), 280), b), nil
}

// transport is the card's previous, pause or resume, and next, as the session lists them
func (c *remoteCard) transport(r *cards.Render, e entry) []*cardsv1.Button {
	playing := e.State != "paused"
	var out []*cardsv1.Button
	for _, a := range transport {
		if !slices.Contains(e.actions, a) || (a == "pause" && !playing) || (a == "resume" && playing) {
			continue
		}
		icon := a
		if a == "resume" {
			icon = "play"
		}
		out = append(out, &cardsv1.Button{Label: r.T("remote." + a), Icon: icon, Primary: a == "pause" || a == "resume",
			Action: r.Bind(c.s.transportBinding(c.caller, c.id, a))})
	}
	return out
}

// position is the session's position as a timeline slider, in seconds, which the screen runs on from the session's
// stamp; it seeks only where the session lists seek. nil for a screen without timelines and for a live session
func (c *remoteCard) position(r *cards.Render, e entry) *cardsv1.Slider {
	if !c.timeline || e.DurationMs == 0 {
		return nil
	}
	maxS := float64(e.DurationMs) / 1000
	step := 10.0
	switch {
	case e.DurationMs < 2*60_000:
		step = 5
	case e.DurationMs > 60*60_000:
		step = 30
	}
	anchor := e.PositionAtMs
	if anchor <= 0 {
		anchor = c.s.d.Now().UnixMilli()
	}
	rate := 0.0
	if e.State == "playing" {
		rate = float64(e.rate)
		if rate <= 0 {
			rate = 1
		}
		rate = min(rate, 4)
	}
	sl := &cardsv1.Slider{
		Value: min(float64(e.PositionMs)/1000, maxS), Min: 0, Max: maxS, Step: min(step, maxS), Label: r.T("remote.position"),
		Timeline: &cardsv1.SliderTimeline{AnchorAtMs: anchor, Rate: rate},
	}
	if slices.Contains(e.actions, "seek") {
		sl.Action = r.Bind(c.s.seekBinding(c.caller, c.id))
	}
	return sl
}

// live is a session with no length to seek in: a stream, a channel; a loading one may not know its length yet
func live(e entry) bool {
	return e.DurationMs == 0 && (e.State != "buffering" || e.md.Kind == "live" || e.md.Kind == "channel")
}

type emptyPanel struct{}

func (emptyPanel) ID() string { return "remote-empty" }

func (emptyPanel) Render(_ context.Context, r *cards.Render) (*cardsv1.Card, error) {
	text := r.T("remote.empty")
	return pcards.NewCard("", 0, text, &cardsv1.CardBlock{Title: clip(r.T("remote.title"), 80), Icon: "music",
		Tone: cardsv1.Tone_TONE_NEUTRAL, Children: pcards.Nodes(&cardsv1.Text{Text: text, Style: cardsv1.TextStyle_TEXT_STYLE_META})}), nil
}

func cmpOr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// lowerFirst is s with its first letter lower-cased, as a state reads after the room
func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToLower(r)) + s[n:]
}

// clip is s on one line, at most n runes
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(strings.ToValidUTF8(s, "")), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
