package catalogtest

import (
	"context"
	"errors"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
)

func TestTorrplayerFakeHoldsTheContract(t *testing.T) {
	Contract(t, Torrplayer(), Case{Query: "фрирен", Item: FrierenAniLiberty, Play: FrierenEpisode5, Episode: "5"})
}

// a provider with few capabilities and no sources, as YouTube Music's first cut
func TestMusicFakeHoldsTheContract(t *testing.T) {
	f := &Fake{Provider: catalog.ProviderYouTubeMusic,
		Caps: catalog.Capabilities{catalog.CapSearch, catalog.CapItem, catalog.CapResolve}}
	track := f.Add(Entry{
		Item: catalog.Item{Key: catalog.Key{Kind: catalog.KindTrack, ID: "dQw4w9WgXcQ"}, Title: "Hana ni Nare", Music: &catalog.MusicInfo{Artist: "Ким", Explicit: true}},
		Play: &catalog.Playable{Kind: catalog.PlayStream, Stream: &catalog.Stream{URL: "https://relay.example/s/1"}},
	})
	album := f.Add(Entry{Item: catalog.Item{Key: catalog.Key{Kind: catalog.KindAlbum, ID: "MPRE1"}, Title: "Hana"},
		Children: []catalog.Key{{Provider: catalog.ProviderYouTubeMusic, Kind: catalog.KindTrack, ID: "dQw4w9WgXcQ"}}})
	Contract(t, f, Case{Query: "hana", Item: album, Play: track})
}

// the plan's torrplayer flow: search per source with a failure, a title group
// by shared id, episodes with a recap, voiceovers, a dead source, and the
// remembered voiceover for the next episode
func TestTorrplayerTreeWalk(t *testing.T) {
	ctx := context.Background()
	f := Torrplayer()

	var batches []catalog.SearchBatch
	if err := f.Search(ctx, catalog.SearchQuery{Query: "Фрирен"}, func(b catalog.SearchBatch) error {
		batches = append(batches, b)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(batches) != 3 || batches[2].Source != "animego" || batches[2].Failure.Code != catalog.FailTimeout {
		t.Fatalf("batches %+v", batches)
	}
	groups := map[string][]catalog.Item{}
	for _, b := range batches {
		for _, it := range b.Items {
			if k, ok := catalog.GroupKey(it); ok {
				groups[k] = append(groups[k], it)
			}
		}
	}
	if len(groups["shikimori:52991"]) != 1 {
		t.Fatalf("groups %v", groups)
	}
	var all []catalog.Item
	if err := f.Search(ctx, catalog.SearchQuery{Query: "frieren"}, func(b catalog.SearchBatch) error {
		all = append(all, b.Items...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	merged := map[string]int{}
	for _, it := range all {
		if k, ok := catalog.GroupKey(it); ok {
			merged[k]++
		}
	}
	if merged["shikimori:52991"] != 1 {
		t.Fatalf("an english query finds one source: %v", merged)
	}

	d, err := f.Item(ctx, catalog.ItemRequest{Key: FrierenAniLiberty})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Children) != 3 || d.Children[2].Episode.Label != "5.5" || d.Children[2].Episode.Ordinal != 5.5 {
		t.Fatalf("episodes %+v", d.Children)
	}
	if d.Continue.Episode != "5" {
		t.Fatalf("continue %+v", d.Continue)
	}

	o, err := f.Options(ctx, catalog.OptionsRequest{Key: FrierenEpisode5, Episode: "5"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Groups[0].Kind != catalog.GroupVoiceover || o.Groups[0].Hidden != 2 || o.Selected[catalog.GroupVoiceover] != "anilibria" {
		t.Fatalf("options %+v", o)
	}

	_, err = f.Resolve(ctx, catalog.ResolveRequest{Key: FrierenEpisode5, Episode: "5",
		Selection: catalog.Selection{catalog.GroupVoiceover: "dead"}})
	var fail *catalog.Failure
	if !errors.Is(err, catalog.ErrUnplayable) || !errors.As(err, &fail) || fail.Code != catalog.FailNoSeeders {
		t.Fatalf("a dead source answers %v", err)
	}

	var stages []catalog.Stage
	p, err := f.Resolve(ctx, catalog.ResolveRequest{Key: FrierenEpisode5, Episode: "5", From: catalog.FromResume,
		Selection: catalog.Selection{catalog.GroupVoiceover: "subs"},
		OnStage:   func(s catalog.Stage) { stages = append(stages, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != catalog.PlayStream || p.Stream.ResumeMS != 300_000 || p.Stream.Skips[0].Kind != catalog.SkipOpening ||
		len(stages) == 0 {
		t.Fatalf("playable %+v stages %v", p, stages)
	}
	// the service remembers the choice per title; the next episode takes it
	if err := f.Remember(ctx, FrierenRecap, p.Selected); err != nil {
		t.Fatal(err)
	}
	o, err = f.Options(ctx, catalog.OptionsRequest{Key: FrierenRecap, Episode: "5.5"})
	if err != nil || o.Selected[catalog.GroupVoiceover] != "subs" {
		t.Fatalf("next episode %+v %v", o.Selected, err)
	}

	actions, err := f.Act(ctx, catalog.ActRequest{Key: FrierenAniLiberty, Action: "my_list", Value: "watching"})
	if err != nil || actions[0].Value != "watching" || !actions[0].Toggled {
		t.Fatalf("my list %+v %v", actions, err)
	}
	if _, err := f.Act(ctx, catalog.ActRequest{Key: FrierenAniLiberty, Action: "my_list", Value: "loved"}); !errors.Is(err, catalog.ErrRefused) {
		t.Fatalf("an unknown status answers %v", err)
	}
	if err := f.Report(ctx, catalog.ProgressReport{Key: FrierenAniLiberty, Episode: "5", PositionS: 600}); err != nil ||
		len(f.Reports()) != 1 {
		t.Fatalf("report %v", err)
	}
}

func TestRefsAreOpaqueAndPerHousehold(t *testing.T) {
	ctx := context.Background()
	var r Refs
	a, err := r.Issue(ctx, "home", FrierenAniLiberty)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := r.Issue(ctx, "home", FrierenAniLiberty)
	other, _ := r.Issue(ctx, "home", FrierenAnime365)
	if a != again || a == other || len(a) < 10 {
		t.Fatalf("refs %q %q %q", a, again, other)
	}
	if k, err := r.Lookup(ctx, "home", a); err != nil || k != FrierenAniLiberty {
		t.Fatalf("lookup %v %v", k, err)
	}
	if _, err := r.Lookup(ctx, "neighbour", a); !errors.Is(err, catalog.ErrUnknownRef) {
		t.Fatalf("another household's ref answers %v", err)
	}
	if _, err := r.Lookup(ctx, "home", "c:made-up"); !errors.Is(err, catalog.ErrUnknownRef) {
		t.Fatalf("a made-up ref answers %v", err)
	}
}
