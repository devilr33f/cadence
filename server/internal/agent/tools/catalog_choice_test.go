package tools_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/agenttest"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/catalog/catalogtest"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// groupCatalog is a title group on three sources, as the catalog service answers one: animevost first (the
// provider's default), anime365 with two voiceovers, aniliberty with one; a choice made is remembered per title
type groupCatalog struct {
	mu       sync.Mutex
	memory   catalog.Selection
	resolved []catalog.Selection
	options  int
}

var groupVoices = map[string][]catalog.Choice{
	"animevost":  {voice("AnimeVost")},
	"anime365":   {voice("AniDUB"), {ID: "subs", Label: "Субтитры", Stream: &catalog.StreamSource{TransType: catalog.TransSub, Quality: 720}}, {ID: "dead", Label: "SHIZA", Unavailable: true}},
	"aniliberty": {voice("AniLibria")},
}

func voice(name string) catalog.Choice {
	return catalog.Choice{ID: name, Label: name, Stream: &catalog.StreamSource{Translator: name, TransType: catalog.TransVoice, Quality: 1080}}
}

var _ agent.Catalog = (*groupCatalog)(nil)

func (g *groupCatalog) Cached(catalog.Ref) (catalog.Item, bool) {
	return catalog.Item{Ref: "g1", Provider: catalog.ProviderTorrplayer, Kind: catalog.KindTitleGroup, Title: "Solo Leveling",
		Show: &catalog.ShowInfo{Names: catalog.Names{RU: "Поднятие уровня в одиночку"}}, Artwork: catalog.Artwork{Hash: "AbCdEfGhIjKlMnOpQrStUv01"}}, true
}

func (g *groupCatalog) Item(context.Context, agent.CatalogItem) (catalog.ItemDetail, error) {
	it, _ := g.Cached("g1")
	return catalog.ItemDetail{Item: it}, nil
}

func (g *groupCatalog) Options(_ context.Context, ref catalog.Ref, _ string, sel catalog.Selection) (catalog.Options, error) {
	if ref != "g1" {
		return catalog.Options{}, catalog.ErrUnknownRef
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.options++
	settled := catalog.Selection{}.Merge(sel).Merge(g.memory).Merge(catalog.Selection{catalog.GroupSource: "animevost"})
	src := settled[catalog.GroupSource]
	if sel[catalog.GroupSource] != "" && sel[catalog.GroupSource] != g.memory[catalog.GroupSource] {
		// another source than the remembered one: its own default voiceover
		delete(settled, catalog.GroupVoiceover)
	}
	if settled[catalog.GroupVoiceover] == "" {
		settled[catalog.GroupVoiceover] = groupVoices[src][0].ID
	}
	o := catalog.Options{Selected: settled, Groups: []catalog.ChoiceGroup{
		{Kind: catalog.GroupSource, Choices: []catalog.Choice{{ID: "animevost", Label: "AnimeVost"}, {ID: "anime365", Label: "anime365"}, {ID: "aniliberty", Label: "AniLiberty"}}},
		{Kind: catalog.GroupVoiceover, Choices: groupVoices[src]},
		{Kind: catalog.GroupQuality, Choices: []catalog.Choice{{ID: "1080", Label: "1080p"}, {ID: "720", Label: "720p"}}},
	}}
	for k, v := range g.memory {
		if sel[k] == "" && settled[k] == v {
			o.Remembered = append(o.Remembered, k)
		}
	}
	return o, nil
}

func (g *groupCatalog) Resolve(_ context.Context, r agent.CatalogResolve) (catalog.Playable, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	sel := catalog.Selection{}.Merge(r.Selection).Merge(g.memory).Merge(catalog.Selection{catalog.GroupSource: "animevost", catalog.GroupQuality: "1080"})
	if sel[catalog.GroupVoiceover] == "" {
		sel[catalog.GroupVoiceover] = groupVoices[sel[catalog.GroupSource]][0].ID
	}
	g.resolved = append(g.resolved, r.Selection)
	g.memory = sel
	return catalog.Playable{Kind: catalog.PlayStream, Episode: "3", Selected: sel, Variant: "v-" + catalog.Ref(sel[catalog.GroupSource]),
		Stream: &catalog.Stream{URL: "http://torrplayer.lan/resolve/g1"}}, nil
}

func (g *groupCatalog) Act(context.Context, catalog.Ref, string, bool, string) ([]catalog.Action, error) {
	return nil, catalog.ErrUnsupported
}

func (g *groupCatalog) Alternatives(context.Context, catalog.Ref) (catalog.AlternativesView, error) {
	return catalog.AlternativesView{}, nil
}

func (g *groupCatalog) Search(context.Context, string, int) (agent.CatalogFound, error) {
	return agent.CatalogFound{}, nil
}

func (g *groupCatalog) last() catalog.Selection {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.resolved) == 0 {
		return nil
	}
	return g.resolved[len(g.resolved)-1]
}

type choiceRig struct {
	cat     agent.Catalog
	prefs   settings.CatalogVoiceovers
	sent    []string
	started []tools.StartedPlay
	// fetch, when set, records catalog_play's fetch card (nil leaves the play without one)
	fetch *fetchCard
}

func (r *choiceRig) tool() agent.Tool {
	return screenTool(tools.ScreenDeps{
		Catalog: r.cat,
		Target: func(_ context.Context, _ agent.Call, _ string, _ agent.Need) (agent.Target, *agent.ToolError) {
			return agent.Target{ID: "bedroom-tv", Name: "Bedroom TV"}, nil
		},
		Command: func(_ context.Context, id, capability, name string, _ []byte) (json.RawMessage, error) {
			r.sent = append(r.sent, id+" "+capability+"/"+name)
			return json.RawMessage(`{"sessionId":"sess-1"}`), nil
		},
		Voiceovers: func(context.Context, string) settings.CatalogVoiceovers { return r.prefs },
		Fetch:      r.fetch.Fetch,
		Started:    func(_ agent.Call, p tools.StartedPlay) { r.started = append(r.started, p) },
	}, "catalog_play")
}

// a title with no remembered choice and no preferred one is the user's to choose: the call names the best options
// (dubs before subtitles, nothing unplayable) and plays nothing; the choice made then plays, and is remembered
func TestCatalogPlayAsksWhenNothingSettlesTheVoiceover(t *testing.T) {
	cat := agenttest.NewCatalog()
	r := &choiceRig{cat: cat}
	ep := cat.Ref(t, catalogtest.FrierenEpisode5)
	_, e := call(t, r.tool(), "spk", `{"ref":"`+ep+`"}`)
	if e == nil || e.Code != "choose_voiceover" || !slices.Equal(e.Candidates, []string{"AniLibria", "Субтитры"}) ||
		!strings.Contains(e.Message, `"AniLibria" (voiceover "anilibria")`) || !strings.Contains(e.Hint, "Ask the user") {
		t.Fatalf("ask %+v", e)
	}
	if len(r.sent) != 0 || len(cat.Resolved()) != 0 {
		t.Fatalf("played %v", r.sent)
	}
	if _, e := call(t, r.tool(), "spk", `{"ref":"`+ep+`","voiceover":"subs"}`); e != nil {
		t.Fatalf("the user's choice: %+v", e)
	}
	// the title remembers it: the next play takes it unasked
	if _, e := call(t, r.tool(), "spk", `{"ref":"`+ep+`","episode":"1"}`); e != nil {
		t.Fatalf("remembered: %+v", e)
	}
	res := cat.Resolved()
	if len(res) != 2 || res[1].Selection[catalog.GroupVoiceover] != "subs" || len(r.sent) != 2 {
		t.Fatalf("resolved %+v sent %v", res, r.sent)
	}
}

// the household's preferred dub plays unasked; an avoided one is never offered, so one left alone plays
func TestCatalogPlayTakesThePreferredVoiceover(t *testing.T) {
	for _, c := range []struct {
		name  string
		prefs settings.CatalogVoiceovers
		want  string
	}{
		{"preferred, an unplayable one skipped", settings.CatalogVoiceovers{Prefer: []string{"AniDUB", "anilibria"}}, "anilibria"},
		{"preferred by its translator", settings.CatalogVoiceovers{Prefer: []string{"crunchyroll"}}, "subs"},
		{"the other avoided", settings.CatalogVoiceovers{Avoid: []string{"AniLibria"}}, "subs"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cat := agenttest.NewCatalog()
			r := &choiceRig{cat: cat, prefs: c.prefs}
			if _, e := call(t, r.tool(), "spk", `{"ref":"`+cat.Ref(t, catalogtest.FrierenEpisode5)+`"}`); e != nil {
				t.Fatalf("%+v", e)
			}
			if res := cat.Resolved(); len(res) != 1 || res[0].Selection[catalog.GroupVoiceover] != c.want {
				t.Fatalf("resolved %+v", res)
			}
		})
	}
}

// a title on several sources: a preferred dub on another source than the default wins over the order of the
// sources, a preferred source plays its own default dub, an avoided default is never taken, and with nothing
// preferred the sources are the user's to choose
func TestCatalogPlayChoosesAmongATitlesSources(t *testing.T) {
	cases := []struct {
		name          string
		prefs         settings.CatalogVoiceovers
		source, voice string
		ask           []string
	}{
		{"a preferred dub elsewhere", settings.CatalogVoiceovers{Prefer: []string{"AniLibria", "anime365"}}, "aniliberty", "AniLibria", nil},
		{"a preferred source", settings.CatalogVoiceovers{Prefer: []string{"anime365"}}, "anime365", "", nil},
		{"a preferred dub before a preferred source", settings.CatalogVoiceovers{Prefer: []string{"aniliberty", "AniDUB"}}, "aniliberty", "", nil},
		{"nothing preferred", settings.CatalogVoiceovers{}, "", "", []string{"AnimeVost", "anime365", "AniLiberty"}},
		{"the worst avoided", settings.CatalogVoiceovers{Avoid: []string{"animevost"}}, "", "", []string{"anime365", "AniLiberty"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := &groupCatalog{}
			r := &choiceRig{cat: g, prefs: c.prefs}
			_, e := call(t, r.tool(), "spk", `{"ref":"g1"}`)
			if c.ask != nil {
				if e == nil || e.Code != "choose_voiceover" || !slices.Equal(e.Candidates, c.ask) || !strings.Contains(e.Message, `(source "anime365")`) {
					t.Fatalf("ask %+v", e)
				}
				if len(r.sent) != 0 {
					t.Fatalf("played %v", r.sent)
				}
				return
			}
			if e != nil {
				t.Fatalf("%+v", e)
			}
			got := g.last()
			if got[catalog.GroupSource] != c.source || got[catalog.GroupVoiceover] != c.voice {
				t.Fatalf("resolved %+v", got)
			}
		})
	}
	// the source the user picked is remembered: the next play neither asks nor reads the other sources
	g := &groupCatalog{}
	r := &choiceRig{cat: g}
	if _, e := call(t, r.tool(), "spk", `{"ref":"g1","source":"anime365"}`); e != nil {
		t.Fatalf("picked: %+v", e)
	}
	g.options = 0
	if _, e := call(t, r.tool(), "spk", `{"ref":"g1","episode":"4"}`); e != nil || g.options != 1 {
		t.Fatalf("remembered: %+v, %d option reads", e, g.options)
	}
	// the play's card names what plays where
	sp := r.started[len(r.started)-1]
	if sp.DeviceName != "Bedroom TV" || sp.SessionID != "sess-1" || sp.Provider != "torrplayer" || sp.Source != "anime365" ||
		sp.Voiceover != "AniDUB" || sp.Quality != "1080p" || sp.Episode != "3" || sp.Title != "Поднятие уровня в одиночку" ||
		sp.ArtworkHash != "AbCdEfGhIjKlMnOpQrStUv01" || sp.Ref != "v-anime365" {
		t.Fatalf("started %+v", sp)
	}
}

// a search's items go to the turn's screens with what their tiles show; nothing found shows nothing
func TestCatalogSearchShowsItsTitles(t *testing.T) {
	f := newScreenFake()
	var shown [][]catalog.Item
	d := f.deps()
	d.Found = func(c agent.Call, items []catalog.Item) {
		if c.SessionID != "s1" {
			t.Errorf("call %+v", c)
		}
		shown = append(shown, items)
	}
	search := screenTool(d, "catalog_search")
	if _, e := call(t, search, "spk", `{"query":"фрирен"}`); e != nil {
		t.Fatal(e)
	}
	if _, e := call(t, search, "spk", `{"query":"nothing like it"}`); e != nil {
		t.Fatal(e)
	}
	if len(shown) != 1 || len(shown[0]) == 0 || shown[0][0].Ref == "" || shown[0][0].Show == nil || shown[0][0].Show.Year != 2023 {
		t.Fatalf("shown %+v", shown)
	}
}

// the same title on its other sources, the one it plays from marked
func TestCatalogAlternatives(t *testing.T) {
	f := newScreenFake()
	tool := screenTool(f.deps(), "catalog_alternatives")
	out, e := call(t, tool, "spk", `{"ref":"`+f.cat.Ref(t, catalogtest.FrierenAniLiberty)+`"}`)
	if e != nil {
		t.Fatal(e)
	}
	var got struct {
		Items []struct {
			Ref, Source, Match string
			Current            bool
		}
	}
	if err := json.Unmarshal(out, &got); err != nil || len(got.Items) != 2 {
		t.Fatalf("%s", out)
	}
	cur := 0
	for _, it := range got.Items {
		if it.Current {
			cur++
			if it.Source != "aniliberty" {
				t.Fatalf("current %+v", it)
			}
		}
		if it.Match != "shikimori" || it.Ref == "" {
			t.Fatalf("item %+v", it)
		}
	}
	if cur != 1 {
		t.Fatalf("%s", out)
	}
	if _, e := call(t, tool, "spk", `{"ref":"c:made-up"}`); e == nil || e.Code != "unknown_ref" {
		t.Fatalf("made-up %+v", e)
	}
}
