package tools

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// a play's choice of dub, before it resolves: a choice the model passed (the user's), else the title's remembered
// one, else the household's preferred dub or source (catalogVoiceovers), else one real option alone; several are
// the user's to pick, so the call answers them instead of playing one at random.

const (
	// the options an ask names: the user hears two to four
	maxAsked = 4
	// the other sources read for a preferred dub
	maxOtherSources = 3
	maxChoiceLabel  = 40
)

// choiceLabels are the labels of what a play resolved, for its card
type choiceLabels struct {
	source, voiceover map[string]string
	quality           map[string]string
}

func (l choiceLabels) of(m map[string]string, id string) string {
	if v := m[id]; v != "" {
		return v
	}
	return id
}

func labelsOf(o catalog.Options, into *choiceLabels) {
	for _, g := range o.Groups {
		var m *map[string]string
		switch g.Kind {
		case catalog.GroupSource:
			m = &into.source
		case catalog.GroupVoiceover:
			m = &into.voiceover
		case catalog.GroupQuality:
			m = &into.quality
		default:
			continue
		}
		if *m == nil {
			*m = map[string]string{}
		}
		for _, ch := range g.Choices {
			if (*m)[ch.ID] == "" {
				(*m)[ch.ID] = ch.Label
			}
		}
	}
}

// offer is one source's voiceovers (the title's own when it has no sources)
type offer struct {
	source catalog.Choice
	voices []catalog.Choice
	o      catalog.Options
}

// settle answers the selection a play resolves with, and the labels its card names
func (d ScreenDeps) settle(ctx context.Context, deviceID string, ref catalog.Ref, episode string,
	asked catalog.Selection) (catalog.Selection, choiceLabels, *agent.ToolError) {
	var lab choiceLabels
	o, err := d.Catalog.Options(ctx, ref, episode, asked)
	if err != nil {
		// the resolve says what fails; a source without options has nothing to choose
		return asked, lab, nil
	}
	labelsOf(o, &lab)
	if asked[catalog.GroupSource] != "" || asked[catalog.GroupVoiceover] != "" ||
		slices.Contains(o.Remembered, catalog.GroupSource) || slices.Contains(o.Remembered, catalog.GroupVoiceover) {
		return asked, lab, nil
	}
	var prefs settings.CatalogVoiceovers
	if d.Voiceovers != nil {
		prefs = d.Voiceovers(ctx, deviceID)
	}
	sources, srcAvoided := narrow(playable(o, catalog.GroupSource), prefs.Avoid)
	here := offer{source: current(o), o: o}
	here.voices, _ = narrow(playable(o, catalog.GroupVoiceover), prefs.Avoid)
	if len(prefs.Prefer) > 0 {
		offers := []offer{here}
		if offerRank(prefs.Prefer, here) != 0 {
			offers = append(offers, d.otherOffers(ctx, ref, episode, asked, sources, here.source.ID, prefs.Avoid)...)
		}
		best, at := len(prefs.Prefer), -1
		for i, of := range offers {
			if r := offerRank(prefs.Prefer, of); r < best {
				best, at = r, i
			}
		}
		if at >= 0 {
			of := offers[at]
			labelsOf(of.o, &lab)
			sel := catalog.Selection{}.Merge(asked)
			if of.source.ID != "" {
				sel[catalog.GroupSource] = of.source.ID
			}
			if v, i := bestVoice(prefs.Prefer, of.voices); i < len(prefs.Prefer) {
				sel[catalog.GroupVoiceover] = v.ID
			}
			return sel, lab, nil
		}
	}
	// nothing settles it: one option alone plays, several (or only avoided ones) are the user's
	if len(sources) > 1 || srcAvoided {
		return nil, lab, askChoice(catalog.GroupSource, sources)
	}
	sel := catalog.Selection{}.Merge(asked)
	if len(sources) == 1 && sources[0].ID != here.source.ID {
		// the one source left is another than the default: its own voiceovers decide
		sel[catalog.GroupSource] = sources[0].ID
		o2, err := d.Catalog.Options(ctx, ref, episode, sel)
		if err != nil {
			return sel, lab, nil
		}
		labelsOf(o2, &lab)
		here = offer{source: sources[0], o: o2}
		var avoided bool
		here.voices, avoided = narrow(playable(o2, catalog.GroupVoiceover), prefs.Avoid)
		if len(here.voices) > 1 || avoided {
			return nil, lab, askChoice(catalog.GroupVoiceover, here.voices)
		}
		if len(here.voices) == 1 {
			sel[catalog.GroupVoiceover] = here.voices[0].ID
		}
		return sel, lab, nil
	}
	voices, avoided := narrow(playable(o, catalog.GroupVoiceover), prefs.Avoid)
	if len(voices) > 1 || avoided {
		return nil, lab, askChoice(catalog.GroupVoiceover, voices)
	}
	if len(voices) == 1 {
		sel[catalog.GroupVoiceover] = voices[0].ID
	}
	return sel, lab, nil
}

// otherOffers reads the voiceovers of up to three other sources than the default
func (d ScreenDeps) otherOffers(ctx context.Context, ref catalog.Ref, episode string, asked catalog.Selection,
	sources []catalog.Choice, skip string, avoid []string) []offer {
	var out []offer
	for _, s := range sources {
		if s.ID == skip {
			continue
		}
		if len(out) == maxOtherSources {
			break
		}
		sel := catalog.Selection{}.Merge(asked)
		sel[catalog.GroupSource] = s.ID
		o, err := d.Catalog.Options(ctx, ref, episode, sel)
		if err != nil {
			continue
		}
		of := offer{source: s, o: o}
		of.voices, _ = narrow(playable(o, catalog.GroupVoiceover), avoid)
		out = append(out, of)
	}
	return out
}

// current is the source o settles on, a zero choice for a title without sources
func current(o catalog.Options) catalog.Choice {
	id := o.Selected[catalog.GroupSource]
	for _, g := range o.Groups {
		if g.Kind != catalog.GroupSource {
			continue
		}
		for _, ch := range g.Choices {
			if ch.ID == id {
				return ch
			}
		}
	}
	return catalog.Choice{ID: id}
}

// playable are a group's choices that can play, one per name
func playable(o catalog.Options, kind catalog.GroupKind) []catalog.Choice {
	var out []catalog.Choice
	seen := map[string]bool{}
	for _, g := range o.Groups {
		if g.Kind != kind {
			continue
		}
		for _, ch := range g.Choices {
			k := settings.VoiceoverKey(ch.Label)
			if ch.Unavailable || ch.ID == "" || seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, ch)
		}
	}
	// a dub before subtitles and the original, else as the source ranks them
	slices.SortStableFunc(out, func(a, b catalog.Choice) int { return voiceRank(a) - voiceRank(b) })
	return out
}

func voiceRank(ch catalog.Choice) int {
	if ch.Stream != nil && ch.Stream.TransType != "" && ch.Stream.TransType != catalog.TransVoice {
		return 1
	}
	return 0
}

// narrow drops the avoided choices; avoided says every choice was one
func narrow(chs []catalog.Choice, avoid []string) ([]catalog.Choice, bool) {
	if len(avoid) == 0 {
		return chs, false
	}
	kept := slices.DeleteFunc(slices.Clone(chs), func(ch catalog.Choice) bool { return prefIndex(avoid, ch) < len(avoid) })
	if len(kept) == 0 && len(chs) > 0 {
		return chs, true
	}
	return kept, false
}

// offerRank is an offer's best preference index (its source's or a voiceover's), len(prefs) for none
func offerRank(prefs []string, of offer) int {
	best := len(prefs)
	if of.source.ID != "" {
		best = prefIndex(prefs, of.source)
	}
	if _, i := bestVoice(prefs, of.voices); i < best {
		best = i
	}
	return best
}

func bestVoice(prefs []string, voices []catalog.Choice) (catalog.Choice, int) {
	best, at := len(prefs), -1
	for i, v := range voices {
		if p := prefIndex(prefs, v); p < best {
			best, at = p, i
		}
	}
	if at < 0 {
		return catalog.Choice{}, best
	}
	return voices[at], best
}

// prefIndex is the first name of prefs that names ch (its id, label or translator), len(prefs) for none
func prefIndex(prefs []string, ch catalog.Choice) int {
	names := []string{ch.ID, ch.Label}
	if ch.Stream != nil {
		names = append(names, ch.Stream.Translator)
	}
	for i, p := range prefs {
		for _, n := range names {
			if sameName(p, n) {
				return i
			}
		}
	}
	return len(prefs)
}

// sameName is one name, case and separators aside, or one holding the other of at least four letters
// ("AniLibria" and "AniLibria.TV")
func sameName(a, b string) bool {
	a, b = settings.VoiceoverKey(a), settings.VoiceoverKey(b)
	if a == "" || b == "" {
		return false
	}
	if len([]rune(a)) > len([]rune(b)) {
		a, b = b, a
	}
	return a == b || (len([]rune(a)) >= 4 && strings.Contains(b, a))
}

// askChoice is the error that has the model ask the user: the best options by label, each with the id to pass
func askChoice(kind catalog.GroupKind, chs []catalog.Choice) *agent.ToolError {
	arg := "voiceover"
	if kind == catalog.GroupSource {
		arg = "source"
	}
	var opts, names []string
	for _, ch := range chs {
		if len(opts) == maxAsked {
			break
		}
		label := safeLabel(ch.Label)
		if label == "" {
			label = safeLabel(ch.ID)
		}
		names = append(names, label)
		opts = append(opts, strconv.Quote(label)+" ("+arg+" "+strconv.Quote(safeLabel(ch.ID))+")")
	}
	return &agent.ToolError{
		Code: "choose_voiceover",
		Message: "This title has no remembered or preferred " + arg + " and several can play: " +
			strings.Join(opts, ", ") + ".",
		Hint: "Do not pick one yourself. Ask the user in one short question which they want, naming these " +
			"options briefly, then call catalog_play again with " + arg + " set to the chosen id.",
		Candidates: names,
	}
}

// safeLabel is a third party's label as an ask names it: one line, letters, digits and plain punctuation, cut
func safeLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == ' ', strings.ContainsRune(".,-_+&()!'", r):
			return r
		}
		return ' '
	}, s)
	return clipText(strings.Join(strings.Fields(s), " "), maxChoiceLabel)
}
