package tools_test

import (
	"slices"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/agenttest"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/catalog/catalogtest"
	"git.shino.boo/devilreef/cadence/server/internal/playprogress"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// a catalog_play's fetch card: attached from its call on with what the catalog cached of the ref, fed the resolve's
// stages through the call's context, ended by what started or the call's failure; an asked call (a voiceover to
// choose) attaches none, the question is the card

type fetchCard struct {
	fetched  []tools.CatalogFetching
	progress []playprogress.Event
	started  []tools.StartedPlay
	failed   []string
}

// Fetch answers the handle, nil on a rig without one (the play keeps its Started card alone)
func (f *fetchCard) Fetch(_ agent.Call, g tools.CatalogFetching) tools.CatalogFetch {
	if f == nil {
		return nil
	}
	f.fetched = append(f.fetched, g)
	return f
}

func (f *fetchCard) Progress(ev playprogress.Event) { f.progress = append(f.progress, ev) }
func (f *fetchCard) Started(p tools.StartedPlay)    { f.started = append(f.started, p) }
func (f *fetchCard) Fail(code string)               { f.failed = append(f.failed, code) }

// a play that works attaches its fetch card first — with the cached item's name and poster, the screen it is for —
// then ends it with what started, the details the Started card always carried
func TestCatalogPlayFetchesItsCard(t *testing.T) {
	cat := agenttest.NewCatalog()
	r := &choiceRig{cat: cat, prefs: settings.CatalogVoiceovers{Prefer: []string{"anilibria"}}, fetch: &fetchCard{}}
	ref := cat.Ref(t, catalogtest.FrierenEpisode5)
	if _, e := call(t, r.tool(), "spk", `{"ref":"`+ref+`"}`); e != nil {
		t.Fatalf("play: %+v", e)
	}
	f := r.fetch
	if len(f.fetched) != 1 || f.fetched[0].Screen != "Bedroom TV" {
		t.Fatalf("fetched %+v", f.fetched)
	}
	// the cached item names the fetch while the catalog resolves (agenttest holds it), its Russian name first
	cached, ok := cat.Cached(catalog.Ref(ref))
	title, art := cached.Title, cached.Artwork.Hash
	if cached.Show != nil && cached.Show.Names.RU != "" {
		title = cached.Show.Names.RU
	}
	if !ok || f.fetched[0].Title != title || f.fetched[0].Artwork != art {
		t.Fatalf("the fetch's item %+v against %q/%q", f.fetched[0], title, art)
	}
	if len(f.started) != 1 || f.started[0].SessionID != "sess-1" || f.started[0].DeviceName != "Bedroom TV" ||
		len(r.started) != 0 || len(f.failed) != 0 {
		t.Fatalf("started %+v (started card %+v) failed %v", f.started, r.started, f.failed)
	}
}

// a ref the catalog no longer holds fails through the card with its code, nothing started
func TestCatalogPlayFetchFailsThroughItsCard(t *testing.T) {
	r := &choiceRig{cat: agenttest.NewCatalog(), fetch: &fetchCard{}}
	if _, e := call(t, r.tool(), "spk", `{"ref":"gone"}`); e == nil || e.Code != "unknown_ref" {
		t.Fatalf("play: %+v", e)
	}
	if len(r.fetch.fetched) != 1 || r.fetch.fetched[0].Title != "" {
		t.Fatalf("fetched %+v", r.fetch.fetched)
	}
	if !slices.Equal(r.fetch.failed, []string{"unknown_ref"}) || len(r.fetch.started) != 0 {
		t.Fatalf("failed %v started %+v", r.fetch.failed, r.fetch.started)
	}
}

// an asked call (nothing settles the voiceover) attaches no card: the question is what the screen shows
func TestCatalogPlayAskAttachesNoFetchCard(t *testing.T) {
	cat := agenttest.NewCatalog()
	r := &choiceRig{cat: cat, fetch: &fetchCard{}}
	ep := cat.Ref(t, catalogtest.FrierenEpisode5)
	if _, e := call(t, r.tool(), "spk", `{"ref":"`+ep+`"}`); e == nil || e.Code != "choose_voiceover" {
		t.Fatalf("ask: %+v", e)
	}
	if len(r.fetch.fetched) != 0 {
		t.Fatalf("an asked call fetched %+v", r.fetch.fetched)
	}
}
