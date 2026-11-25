package catalog_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/catalog/catalogtest"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/torrplayer"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/torrplayer/torrplayertest"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/torrserver"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/torrserver/torrservertest"
)

type images struct{ n int }

func (i *images) Ref(u string, local bool) string {
	if u == "" {
		return ""
	}
	i.n++
	if local {
		return "local-" + u[len(u)-6:]
	}
	return "public"
}

type rig struct {
	svc  *catalog.Service
	tp   *torrplayertest.Fake
	ts   *torrservertest.Fake
	imgs *images
}

func newRig(t *testing.T, edit ...func(*catalog.Deps)) *rig {
	t.Helper()
	tp := torrplayertest.New(torrplayertest.Frieren())
	tpSrv := httptest.NewServer(tp)
	t.Cleanup(tpSrv.Close)
	tpc, _ := torrplayer.NewClient(torrplayer.ClientConfig{BaseURL: tpSrv.URL, Token: "fake-token"})
	ts := torrservertest.New(torrservertest.Scripted())
	tsSrv := httptest.NewServer(ts)
	t.Cleanup(tsSrv.Close)
	tsc, _ := torrserver.NewClient(torrserver.ClientConfig{BaseURL: tsSrv.URL})
	tsp := torrserver.New(tsc)
	tsp.Poll = 5 * time.Millisecond
	ps := []catalog.Configured{{Instance: "anime", Provider: torrplayer.New(tpc)}, {Instance: "torrents", Provider: tsp}}
	imgs := &images{}
	d := catalog.Deps{
		Providers: func(context.Context) []catalog.Configured { return ps }, Images: imgs,
		ImageURL: func(id string) string { return "https://cadence.example/v2/images/" + id },
	}
	for _, e := range edit {
		e(&d)
	}
	svc := catalog.New(d)
	return &rig{svc: svc, tp: tp, ts: ts, imgs: imgs}
}

func (r *rig) search(t *testing.T, q string) []catalog.SearchEvent {
	t.Helper()
	var evs []catalog.SearchEvent
	if err := r.svc.Search(context.Background(), catalog.Household, catalog.SearchRequest{Query: q}, func(ev catalog.SearchEvent) error {
		evs = append(evs, ev)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestSearchStreamsMergesAndFails(t *testing.T) {
	r := newRig(t)
	evs := r.search(t, "фрирен")
	if len(evs[0].Pending) != 7 {
		t.Fatalf("pending %+v", evs[0].Pending)
	}
	if !evs[len(evs)-1].Done {
		t.Fatal("no done")
	}
	var groups, updated []catalog.Item
	failed := map[catalog.SourceID]catalog.FailureCode{}
	answered := 0
	for _, ev := range evs[1 : len(evs)-1] {
		answered++
		if ev.Failure != nil {
			failed[ev.Source] = ev.Failure.Code
		}
		for _, it := range ev.Items {
			if it.Kind == catalog.KindTitleGroup {
				groups = append(groups, it)
			}
			if it.Ref == "" || !strings.HasPrefix(string(it.Ref), "c:") {
				t.Fatalf("an item without a ref: %+v", it)
			}
		}
		updated = append(updated, ev.Updated...)
	}
	if answered != 7 {
		t.Fatalf("answers %d", answered)
	}
	if failed["animego"] != catalog.FailFailed || len(failed) != 1 {
		t.Fatalf("failed %v", failed)
	}
	if len(groups) != 1 || len(updated) != 1 || updated[0].Ref != groups[0].Ref || updated[0].Show.SourcesWithIt != 2 {
		t.Fatalf("groups %+v updated %+v", groups, updated)
	}
	if groups[0].Artwork.Hash == "" || !strings.HasPrefix(groups[0].Artwork.Hash, "local-") {
		t.Fatalf("poster %q", groups[0].Artwork.Hash)
	}
}

func groupRef(t *testing.T, r *rig) catalog.Ref {
	t.Helper()
	for _, ev := range r.search(t, "фрирен") {
		for _, it := range ev.Items {
			if it.Kind == catalog.KindTitleGroup {
				return it.Ref
			}
		}
	}
	t.Fatal("no group")
	return ""
}

func TestTitleGroupRemembersSourceAndVoiceover(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	ref := groupRef(t, r)
	d, err := r.svc.Item(ctx, catalog.Household, ref, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Variants) != 2 || d.Selected != d.Variants[0].Ref || len(d.Children) != 3 || d.Item.Kind != catalog.KindTitleGroup {
		t.Fatalf("detail %+v", d)
	}
	o, err := r.svc.Options(ctx, catalog.Household, ref, "5", nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.Groups[0].Kind != catalog.GroupSource || len(o.Groups[0].Choices) != 2 || o.Selected[catalog.GroupSource] != "aniliberty" ||
		len(o.Remembered) != 0 {
		t.Fatalf("options %+v", o)
	}
	// Home Assistant's source node reads the chosen variant without making a play remember it early
	chosen, err := r.svc.ItemWithChoices(ctx, catalog.Household, ref, "", "",
		catalog.Selection{catalog.GroupSource: "anime365"})
	if err != nil || chosen.Selected != d.Variants[1].Ref {
		t.Fatalf("chosen source %+v %v", chosen, err)
	}
	unchanged, err := r.svc.Item(ctx, catalog.Household, ref, "", "")
	if err != nil || unchanged.Selected != d.Variants[0].Ref {
		t.Fatalf("a read remembered the source: %+v %v", unchanged, err)
	}
	pl, err := r.svc.Resolve(ctx, catalog.Household, catalog.PlayRequest{Ref: ref, Episode: "5",
		Choices: catalog.Selection{catalog.GroupSource: "anime365", catalog.GroupVoiceover: "Crunchyroll"}})
	if err != nil {
		t.Fatal(err)
	}
	if pl.Kind != catalog.PlayStream || pl.Stream == nil || pl.Selected[catalog.GroupSource] != "anime365" {
		t.Fatalf("playable %+v", pl)
	}
	if r.tp.Prefs("anime365", "t-9931")["translator"] != "Crunchyroll" {
		t.Fatal("the voiceover is not in torrplayer's prefs")
	}
	// next episode, same source and voiceover, nothing chosen
	pl, err = r.svc.Resolve(ctx, catalog.Household, catalog.PlayRequest{Ref: ref, Episode: "5.5"})
	if err != nil || pl.Selected[catalog.GroupSource] != "anime365" || pl.Selected[catalog.GroupVoiceover] != "Crunchyroll" {
		t.Fatalf("remembered %+v %v", pl.Selected, err)
	}
	// the options say both came from the title's memory: a play need not ask again
	if o, err = r.svc.Options(ctx, catalog.Household, ref, "5", nil); err != nil ||
		!slices.Equal(o.Remembered, []catalog.GroupKind{catalog.GroupQuality, catalog.GroupSource, catalog.GroupVoiceover}) {
		t.Fatalf("remembered groups %+v %v", o.Remembered, err)
	}
	d, _ = r.svc.Item(ctx, catalog.Household, ref, "", "")
	if d.Selected != d.Variants[1].Ref {
		t.Fatal("the remembered source is not selected")
	}
	if err := r.svc.Report(ctx, catalog.Household, pl.Ref, "5.5", 300, 600, true); err != nil {
		t.Fatal(err)
	}
	if rep := r.tp.Reports(); len(rep) != 1 || rep[0].Source != "anime365" || rep[0].State != "ended" {
		t.Fatalf("reports %+v", rep)
	}
}

func TestRefsAreTheHouseholdsAndActionsIssued(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	ref := groupRef(t, r)
	if _, err := r.svc.Item(ctx, "other", ref, "", ""); !errors.Is(err, catalog.ErrUnknownRef) {
		t.Fatalf("another household's ref: %v", err)
	}
	if _, err := r.svc.Item(ctx, catalog.Household, "c:made-up", "", ""); !errors.Is(err, catalog.ErrUnknownRef) {
		t.Fatalf("a made-up ref: %v", err)
	}
	d, _ := r.svc.Item(ctx, catalog.Household, ref, "", "")
	var sub catalog.ActionView
	for _, a := range d.Actions {
		if a.Kind == catalog.ActionSubscribe {
			sub = a
		}
	}
	if !strings.HasPrefix(sub.ID, "a:") {
		t.Fatalf("action id %q", sub.ID)
	}
	if _, err := r.svc.Act(ctx, catalog.Household, catalog.ActCall{Ref: ref, Action: "subscribe", On: true}); !errors.Is(err, catalog.ErrUnknownAction) {
		t.Fatalf("a provider's own action id: %v", err)
	}
	acts, err := r.svc.Act(ctx, catalog.Household, catalog.ActCall{Ref: ref, Action: sub.ID, On: true})
	if err != nil || !acts[1].Toggled {
		t.Fatalf("subscribe %+v %v", acts, err)
	}
	other := groupRef(t, r)
	_ = other
	ts := r.search(t, "dune")
	var torrent catalog.Ref
	for _, ev := range ts {
		if ev.Provider == catalog.ProviderTorrServer && len(ev.Items) > 0 {
			torrent = ev.Items[0].Ref
		}
	}
	if _, err := r.svc.Act(ctx, catalog.Household, catalog.ActCall{Ref: torrent, Action: sub.ID, On: true}); !errors.Is(err, catalog.ErrUnknownAction) {
		t.Fatalf("an action of another item: %v", err)
	}
}

func TestTorrServerPlaysAndRemembersTheFile(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	pg, err := r.svc.Browse(ctx, catalog.Household, "torrserver/library", "", nil, 0)
	if err != nil || len(pg.Sections) != 1 || len(pg.Sections[0].Items) != 2 {
		t.Fatalf("library %+v %v", pg, err)
	}
	var series catalog.Ref
	for _, it := range pg.Sections[0].Items {
		if it.Title == "Тьма, сезон 1" {
			series = it.Ref
		}
	}
	pl, err := r.svc.Resolve(ctx, catalog.Household, catalog.PlayRequest{Ref: series, Choices: catalog.Selection{catalog.GroupFile: "2"}})
	if err != nil || pl.Stream.URL == "" || !strings.HasSuffix(pl.Stream.URL, "/2") {
		t.Fatalf("play %+v %v", pl, err)
	}
	pl, err = r.svc.Resolve(ctx, catalog.Household, catalog.PlayRequest{Ref: series})
	if err != nil || pl.Selected[catalog.GroupFile] != "2" {
		t.Fatalf("remembered file %+v %v", pl.Selected, err)
	}
	home, err := r.svc.Browse(ctx, catalog.Household, "", "", nil, 0)
	if err != nil || len(home.Sections) < 1 || home.Sections[len(home.Sections)-1].ID != "torrserver/library" {
		t.Fatalf("home %+v", home.Sections)
	}
}

func TestSearchTimesOutASlowSource(t *testing.T) {
	lib := torrplayertest.Frieren()
	lib.Slow = map[string]time.Duration{"anime365": time.Second}
	tp := torrplayertest.New(lib)
	srv := httptest.NewServer(tp)
	defer srv.Close()
	c, _ := torrplayer.NewClient(torrplayer.ClientConfig{BaseURL: srv.URL, Token: "fake-token"})
	ps := []catalog.Configured{{Provider: torrplayer.New(c)}}
	svc := catalog.New(catalog.Deps{Providers: func(context.Context) []catalog.Configured { return ps }})
	restore := catalog.SetSearchTimeout(200 * time.Millisecond)
	defer restore()
	failed := map[catalog.SourceID]catalog.FailureCode{}
	err := svc.Search(context.Background(), catalog.Household, catalog.SearchRequest{Query: "фрирен"}, func(ev catalog.SearchEvent) error {
		if ev.Failure != nil {
			failed[ev.Source] = ev.Failure.Code
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if failed["anime365"] != catalog.FailTimeout {
		t.Fatalf("failed %v", failed)
	}
}

func TestSearchStopsWhenTheCallerGoes(t *testing.T) {
	f := catalogtest.Torrplayer()
	svc := catalog.New(catalog.Deps{Providers: func(context.Context) []catalog.Configured {
		return []catalog.Configured{{Provider: f}}
	}})
	stop := errors.New("gone")
	n := 0
	err := svc.Search(context.Background(), catalog.Household, catalog.SearchRequest{Query: "фрирен"}, func(catalog.SearchEvent) error {
		n++
		if n == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || n != 2 {
		t.Fatalf("%v after %d", err, n)
	}
}

func TestBackdropsAreHomeArtwork(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	ref := groupRef(t, r)
	if _, err := r.svc.Resolve(ctx, catalog.Household, catalog.PlayRequest{Ref: ref, Episode: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Report(ctx, catalog.Household, ref, "1", 100, 1440, false); err != nil {
		t.Fatal(err)
	}
	imgs, err := r.svc.Backdrops().Backdrops(ctx, "dev", 4)
	if err != nil || len(imgs) == 0 || !strings.HasPrefix(imgs[0].URL, "https://cadence.example/v2/images/") {
		t.Fatalf("backdrops %+v %v", imgs, err)
	}
}
