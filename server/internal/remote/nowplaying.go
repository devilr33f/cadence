package remote

import (
	"strconv"
	"strings"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
)

// the sources a card names when the session names no provider (remote.source.<source>)
var sourceLabels = map[string]bool{"airplay": true, "soloist": true, "url": true, "stream": true, "video": true}

// the item kinds a screen plays as a video: a series line in place of the artist and album
var videoKinds = map[string]bool{"episode": true, "movie": true, "video": true}

// nowPlaying is the cover beside what plays: its title, artist and album (a video's series line), and where it plays
// from; nil when the session says nothing
func nowPlaying(r *cards.Render, e entry) *cardsv1.Row {
	lines := infoLines(r, e)
	if len(lines) == 0 && e.Art == "" {
		return nil
	}
	row := &cardsv1.Row{Gap: cardsv1.Gap_GAP_M}
	if e.Art != "" {
		row.Children = append(row.Children, pcards.N(&cardsv1.Image{Ref: e.Art, Alt: clip(r.T("remote.cover"), 120),
			Aspect: cardsv1.Aspect_ASPECT_SQUARE}))
	}
	if len(lines) > 0 {
		col := &cardsv1.Column{Gap: cardsv1.Gap_GAP_S}
		for _, l := range lines {
			col.Children = append(col.Children, pcards.N(l))
		}
		row.Children = append(row.Children, pcards.N(col))
	}
	return row
}

func infoLines(r *cards.Render, e entry) []*cardsv1.Text {
	var out []*cardsv1.Text
	add := func(s string, n int, style cardsv1.TextStyle) {
		if s = clip(s, n); s != "" {
			out = append(out, &cardsv1.Text{Text: s, Style: style})
		}
	}
	add(e.Title, 120, cardsv1.TextStyle_TEXT_STYLE_TITLE)
	if e.Source == "video" || videoKinds[e.md.Kind] {
		// a channel's video has no series: its channel rides in artist
		add(cmpOr(seriesLine(e), e.Artist), 280, cardsv1.TextStyle_TEXT_STYLE_BODY)
	} else {
		add(e.Artist, 280, cardsv1.TextStyle_TEXT_STYLE_BODY)
		add(e.Album, 280, cardsv1.TextStyle_TEXT_STYLE_META)
	}
	add(sourceLine(r, e), 120, cardsv1.TextStyle_TEXT_STYLE_META)
	return out
}

// seriesLine is «Series · S1 · E5 · episode name», each part only when the session has it
func seriesLine(e entry) string {
	var parts []string
	if s := clip(e.md.Series, 120); s != "" {
		parts = append(parts, s)
	}
	if e.md.Season > 0 {
		parts = append(parts, "S"+strconv.FormatUint(uint64(e.md.Season), 10))
	}
	if ep := clip(e.md.Episode, 24); ep != "" {
		parts = append(parts, "E"+ep)
	}
	if n := clip(e.md.EpisodeName, 120); n != "" {
		parts = append(parts, n)
	}
	return strings.Join(parts, " · ")
}

// sourceLine is where it plays from: the provider the device names (and its source under it), the music queue's
// provider, else the source's own label; a group's stream names its leader
func sourceLine(r *cards.Render, e entry) string {
	if p := clip(e.md.Provider, 60); p != "" {
		if ps := clip(e.md.ProviderSource, 40); ps != "" {
			return p + " · " + ps
		}
		return p
	}
	if p := clip(e.provider, 60); p != "" {
		return p
	}
	switch {
	case sourceLabels[e.Source]:
		return r.T("remote.source." + e.Source)
	case e.Source == "group" && e.group != "":
		for _, m := range e.members {
			if m.id == e.DeviceID {
				return m.name
			}
		}
	}
	return ""
}
