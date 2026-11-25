package catalog_test

import (
	"context"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
)

// channels is a provider of one video whose owner is a channel, and that channel's tabs
type channels struct {
	catalog.Unsupported
	q   catalog.SearchQuery
	req catalog.ItemRequest
}

var (
	chKey    = catalog.Key{Provider: "yt", Kind: catalog.KindChannel, ID: "UC1"}
	videoKey = catalog.Key{Provider: "yt", Kind: catalog.KindVideo, ID: "v1"}
)

func (p *channels) ID() catalog.ProviderID { return "yt" }
func (p *channels) Capabilities() catalog.Capabilities {
	return catalog.Capabilities{catalog.CapSearch, catalog.CapItem}
}

func (p *channels) Search(_ context.Context, q catalog.SearchQuery, emit func(catalog.SearchBatch) error) error {
	p.q = q
	return emit(catalog.SearchBatch{Provider: "yt", Items: []catalog.Item{{Key: videoKey, Title: "a video",
		Stats: &catalog.Stats{Views: 1200}, Owner: &catalog.Owner{Key: chKey, Title: "a channel"}}}})
}

func (p *channels) Item(_ context.Context, r catalog.ItemRequest) (catalog.ItemDetail, error) {
	p.req = r
	return catalog.ItemDetail{Item: catalog.Item{Key: r.Key, Title: "a channel"},
		Children: []catalog.Item{{Key: videoKey, Title: "a video", Owner: &catalog.Owner{Title: "no key"}}},
		Tabs:     []catalog.Tab{{ID: "videos", Label: "Videos", Selected: true}, {ID: "playlists", Label: "Playlists"}},
		Sorts:    []catalog.Sort{catalog.SortLatest, catalog.SortPopular}, Sort: catalog.SortPopular, Partial: true}, nil
}

func TestAnOwnerIsAnItemOfItsOwn(t *testing.T) {
	p := &channels{}
	svc := catalog.New(catalog.Deps{Providers: func(context.Context) []catalog.Configured {
		return []catalog.Configured{{Instance: "yt", Provider: p}}
	}})
	var found []catalog.Item
	err := svc.Search(context.Background(), catalog.Household, catalog.SearchRequest{Query: "q", Sort: catalog.SortPopular,
		Filters: map[string]string{catalog.FilterUploaded: "week"}}, func(ev catalog.SearchEvent) error {
		found = append(found, ev.Items...)
		return nil
	})
	if err != nil || len(found) != 1 {
		t.Fatalf("%v %v", found, err)
	}
	if p.q.Sort != catalog.SortPopular || p.q.Filters[catalog.FilterUploaded] != "week" {
		t.Fatalf("query %+v", p.q)
	}
	v := found[0]
	if v.Stats == nil || v.Stats.Views != 1200 || v.Owner == nil || v.Owner.Ref == "" || v.Owner.Title != "a channel" {
		t.Fatalf("video %+v owner %+v", v, v.Owner)
	}
	d, err := svc.ItemIn(context.Background(), catalog.Household, v.Owner.Ref,
		catalog.ItemPage{Tab: "videos", Sort: catalog.SortPopular, Query: "fnaf", Limit: 5}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.req.Key != chKey || p.req.Tab != "videos" || p.req.Sort != catalog.SortPopular || p.req.Query != "fnaf" || p.req.Limit != 5 {
		t.Fatalf("request %+v", p.req)
	}
	if len(d.Tabs) != 2 || d.Sort != catalog.SortPopular || len(d.Sorts) != 2 || !d.Partial {
		t.Fatalf("detail %+v", d)
	}
	// an owner without a key keeps its name and gets no ref
	if o := d.Children[0].Owner; o == nil || o.Ref != "" || o.Title != "no key" {
		t.Fatalf("keyless owner %+v", o)
	}
	// the video's ref and its owner's are not the same ref
	if v.Owner.Ref == v.Ref {
		t.Fatal("one ref for both")
	}
}
