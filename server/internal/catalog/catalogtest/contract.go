package catalogtest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
)

// Case is what Contract asks a provider for: a query that finds something,
// an item Item reads, and one Resolve plays (Episode its episode's label).
// Leave a field empty to skip its check.
type Case struct {
	Query   string
	Item    catalog.Key
	Play    catalog.Key
	Episode string
}

// Contract checks what every provider must hold, whatever it serves:
// operations outside its Capabilities answer ErrUnsupported; what it answers
// is its own (provider set, no refs: the service issues them, known kinds);
// search batches name a known source and carry items or a failure, never
// both; a playable's kind matches what it carries.
func Contract(t *testing.T, p catalog.Provider, c Case) {
	t.Helper()
	ctx := context.Background()
	if p.ID() == "" {
		t.Fatal("provider has no id")
	}
	caps := p.Capabilities()
	for _, op := range caps {
		if !slices.Contains(catalog.AllCapabilities, op) {
			t.Fatalf("unknown capability %q", op)
		}
	}
	unsupported := map[catalog.Capability]func() error{
		catalog.CapSearch: func() error {
			return p.Search(ctx, catalog.SearchQuery{Query: "x"}, func(catalog.SearchBatch) error { return nil })
		},
		catalog.CapBrowse:  func() error { _, err := p.Browse(ctx, catalog.BrowseRequest{}); return err },
		catalog.CapItem:    func() error { _, err := p.Item(ctx, catalog.ItemRequest{}); return err },
		catalog.CapOptions: func() error { _, err := p.Options(ctx, catalog.OptionsRequest{}); return err },
		catalog.CapResolve: func() error { _, err := p.Resolve(ctx, catalog.ResolveRequest{}); return err },
		catalog.CapAct:     func() error { _, err := p.Act(ctx, catalog.ActRequest{}); return err },
		catalog.CapPanel:   func() error { _, err := p.Panel(ctx, catalog.PanelRequest{}); return err },
		catalog.CapReport:  func() error { return p.Report(ctx, catalog.ProgressReport{}) },
	}
	for op, call := range unsupported {
		if !caps.Has(op) {
			if err := call(); !errors.Is(err, catalog.ErrUnsupported) {
				t.Errorf("%s is not a capability but answers %v", op, err)
			}
		}
	}
	sources, err := p.Sources(ctx)
	if err != nil {
		t.Fatalf("sources: %v", err)
	}
	known := map[catalog.SourceID]bool{}
	for _, s := range sources {
		known[s.ID] = true
	}
	own := func(where string, it catalog.Item) {
		t.Helper()
		switch {
		case it.Provider != p.ID() || it.Key.Provider != p.ID():
			t.Errorf("%s: an item of %q/%q", where, it.Provider, it.Key.Provider)
		case it.Ref != "":
			t.Errorf("%s: a provider issued a ref", where)
		case !slices.Contains(catalog.Kinds, it.Kind) || it.Key.Kind != it.Kind:
			t.Errorf("%s: kind %q, key kind %q", where, it.Kind, it.Key.Kind)
		case it.Source != it.Key.Source:
			t.Errorf("%s: source %q, key source %q", where, it.Source, it.Key.Source)
		case len(sources) > 0 && !known[it.Source]:
			t.Errorf("%s: unknown source %q", where, it.Source)
		}
	}
	if caps.Has(catalog.CapSearch) && c.Query != "" {
		n := 0
		err := p.Search(ctx, catalog.SearchQuery{Query: c.Query}, func(b catalog.SearchBatch) error {
			if b.Provider != p.ID() {
				t.Errorf("batch of provider %q", b.Provider)
			}
			if len(sources) > 0 && !known[b.Source] {
				t.Errorf("batch of unknown source %q", b.Source)
			}
			if b.Failure != nil && len(b.Items) > 0 {
				t.Errorf("source %q failed and answered items", b.Source)
			}
			for _, it := range b.Items {
				own("search", it)
				n++
			}
			return nil
		})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if n == 0 {
			t.Errorf("search %q found nothing", c.Query)
		}
		stop := errors.New("stop")
		calls := 0
		err = p.Search(ctx, catalog.SearchQuery{Query: c.Query}, func(catalog.SearchBatch) error { calls++; return stop })
		if !errors.Is(err, stop) || calls != 1 {
			t.Errorf("an emit error must stop the search: %v after %d batches", err, calls)
		}
	}
	if caps.Has(catalog.CapItem) && c.Item.ID != "" {
		d, err := p.Item(ctx, catalog.ItemRequest{Key: c.Item})
		if err != nil {
			t.Fatalf("item: %v", err)
		}
		own("item", d.Item)
		for _, ch := range d.Children {
			own("child", ch)
		}
		for _, v := range d.Variants {
			own("variant", v)
		}
		if _, err := p.Item(ctx, catalog.ItemRequest{Key: catalog.Key{Provider: p.ID(), Kind: catalog.KindMovie,
			ID: "no-such-item"}}); !errors.Is(err, catalog.ErrNotFound) {
			t.Errorf("an unknown key answers %v, want ErrNotFound", err)
		}
	}
	if caps.Has(catalog.CapResolve) && c.Play.ID != "" {
		pl, err := p.Resolve(ctx, catalog.ResolveRequest{Key: c.Play, Episode: c.Episode, From: catalog.FromResume})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		switch pl.Kind {
		case catalog.PlayStream, catalog.PlayMatch:
			if pl.Stream == nil || pl.Stream.URL == "" || pl.Handoff != nil {
				t.Errorf("a %s playable carries no stream", pl.Kind)
			}
			if pl.Kind == catalog.PlayMatch && pl.MatchedBy == nil {
				t.Error("a match names nothing it matched")
			}
		case catalog.PlayHandoff:
			if pl.Handoff == nil || pl.Stream != nil {
				t.Error("a handoff carries no handoff")
			}
		default:
			t.Errorf("playable kind %q", pl.Kind)
		}
	}
}
