// Package agenttest holds fakes of the agent's dependencies for other packages' tests.
package agenttest

import (
	"context"
	"slices"
	"sync"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/catalog/catalogtest"
)

const household = "household"

var _ agent.Catalog = (*Catalog)(nil)

// Catalog is agent.Catalog over catalogtest's torrplayer: refs from an
// in-memory ref table, a series with an episode resolving that episode
type Catalog struct {
	p    *catalogtest.Fake
	refs *catalogtest.Refs

	mu       sync.Mutex
	resolved []catalog.ResolveRequest
	devices  []string
	uncached bool
}

func NewCatalog() *Catalog { return NewCatalogOf(catalogtest.Torrplayer()) }

// NewCatalogOf is a Catalog over another provider's fake
func NewCatalogOf(p *catalogtest.Fake) *Catalog {
	return &Catalog{p: p, refs: &catalogtest.Refs{}}
}

// Forget makes Cached miss, as the catalog service's memory does after a restart or its 12 h
func (c *Catalog) Forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.uncached = true
}

func (c *Catalog) Ref(t testing.TB, k catalog.Key) string {
	t.Helper()
	r, err := c.refs.Issue(context.Background(), household, k)
	if err != nil {
		t.Fatal(err)
	}
	return string(r)
}

func (c *Catalog) key(ref catalog.Ref) (catalog.Key, error) {
	return c.refs.Lookup(context.Background(), household, ref)
}

func (c *Catalog) withRef(it catalog.Item) catalog.Item {
	r, _ := c.refs.Issue(context.Background(), household, it.Key)
	it.Ref = r
	return it
}

func (c *Catalog) Cached(ref catalog.Ref) (catalog.Item, bool) {
	c.mu.Lock()
	forgot := c.uncached
	c.mu.Unlock()
	if forgot {
		return catalog.Item{}, false
	}
	k, err := c.key(ref)
	if err != nil {
		return catalog.Item{}, false
	}
	d, err := c.p.Item(context.Background(), catalog.ItemRequest{Key: k})
	if err != nil {
		return catalog.Item{}, false
	}
	return c.withRef(d.Item), true
}

func (c *Catalog) Item(ctx context.Context, r agent.CatalogItem) (catalog.ItemDetail, error) {
	k, err := c.key(r.Ref)
	if err != nil {
		return catalog.ItemDetail{}, err
	}
	d, err := c.p.Item(ctx, catalog.ItemRequest{Key: k, Cursor: r.Cursor, Group: r.Group, Limit: r.Limit})
	if err != nil {
		return d, err
	}
	d.Item = c.withRef(d.Item)
	for i := range d.Children {
		d.Children[i] = c.withRef(d.Children[i])
	}
	for i := range d.Variants {
		d.Variants[i] = c.withRef(d.Variants[i])
	}
	return d, nil
}

func episodeKey(k catalog.Key, episode string) catalog.Key {
	if episode != "" && k.Kind != catalog.KindEpisode {
		k.Kind, k.Episode = catalog.KindEpisode, episode
	}
	return k
}

// titleOf is the key a title's choices are remembered by, as the catalog service keeps them: without its episode
func titleOf(k catalog.Key) catalog.Key {
	if k.Kind == catalog.KindEpisode {
		k.Kind = catalog.KindSeries
	}
	k.Episode = ""
	return k
}

// remembered is the title's remembered choices, as the catalog service recalls them
func (c *Catalog) remembered(ctx context.Context, k catalog.Key) catalog.Selection {
	mem, err := c.p.Recall(ctx, titleOf(k))
	if err != nil || mem == nil {
		return catalog.Selection{}
	}
	return mem
}

// Options answers the provider's groups with the title's remembered choices under sel, marking the groups they
// settled, as the catalog service does
func (c *Catalog) Options(ctx context.Context, ref catalog.Ref, episode string, sel catalog.Selection) (catalog.Options, error) {
	k, err := c.key(ref)
	if err != nil {
		return catalog.Options{}, err
	}
	mem := c.remembered(ctx, k)
	o, err := c.p.Options(ctx, catalog.OptionsRequest{Key: episodeKey(k, episode), Episode: episode, Selection: sel.Merge(mem)})
	if err != nil {
		return o, err
	}
	for g, v := range mem {
		if sel[g] == "" && o.Selected[g] == v {
			o.Remembered = append(o.Remembered, g)
		}
	}
	slices.Sort(o.Remembered)
	return o, nil
}

// Remember stores a title's choices as a play made them (the catalog service's remember)
func (c *Catalog) Remember(t testing.TB, ref string, sel catalog.Selection) {
	t.Helper()
	k, err := c.key(catalog.Ref(ref))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.p.Remember(context.Background(), titleOf(k), sel); err != nil {
		t.Fatal(err)
	}
}

func (c *Catalog) Resolve(ctx context.Context, r agent.CatalogResolve) (catalog.Playable, error) {
	k, err := c.key(r.Ref)
	if err != nil {
		return catalog.Playable{}, err
	}
	req := catalog.ResolveRequest{Key: episodeKey(k, r.Episode), Episode: r.Episode,
		Selection: catalog.Selection{}.Merge(r.Selection).Merge(c.remembered(ctx, k)), From: r.From}
	if req.Episode == "" {
		req.Episode = k.Episode
	}
	c.mu.Lock()
	c.resolved = append(c.resolved, req)
	c.devices = append(c.devices, r.DeviceID)
	c.mu.Unlock()
	// as the catalog service names the device to its provider, and remembers what it resolved
	p, err := c.p.Resolve(catalog.WithDevice(ctx, r.DeviceID), req)
	if err == nil && len(p.Selected) > 0 {
		_ = c.p.Remember(ctx, titleOf(k), p.Selected)
	}
	return p, err
}

func (c *Catalog) Act(ctx context.Context, ref catalog.Ref, action string, on bool, value string) ([]catalog.Action, error) {
	k, err := c.key(ref)
	if err != nil {
		return nil, err
	}
	return c.p.Act(ctx, catalog.ActRequest{Key: k, Action: action, On: on, Value: value})
}

func (c *Catalog) Search(ctx context.Context, query string, limit int) (agent.CatalogFound, error) {
	var out agent.CatalogFound
	err := c.p.Search(ctx, catalog.SearchQuery{Query: query, Limit: limit}, func(b catalog.SearchBatch) error {
		out.Asked++
		if b.Failure != nil {
			out.Failed++
			return nil
		}
		for _, it := range b.Items {
			if limit <= 0 || len(out.Items) < limit {
				out.Items = append(out.Items, c.withRef(it))
			}
		}
		return nil
	})
	return out, err
}

// Resolved answers what Resolve was asked.
func (c *Catalog) Resolved() []catalog.ResolveRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.resolved)
}

// ResolvedFor answers the device each Resolve was for, in order.
func (c *Catalog) ResolvedFor() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.devices)
}

// Provider is the torrplayer fake under the catalog.
func (c *Catalog) Provider() *catalogtest.Fake { return c.p }

// Alternatives finds the title on the fake's sources by its Shikimori id, as the catalog service does by id
func (c *Catalog) Alternatives(ctx context.Context, ref catalog.Ref) (catalog.AlternativesView, error) {
	k, err := c.key(ref)
	if err != nil {
		return catalog.AlternativesView{}, err
	}
	d, err := c.p.Item(ctx, catalog.ItemRequest{Key: k})
	if err != nil {
		return catalog.AlternativesView{}, err
	}
	out := catalog.AlternativesView{Items: []catalog.AlternativeView{}}
	if d.Item.Show == nil || d.Item.Show.IDs.Shikimori == "" {
		return out, nil
	}
	shiki := d.Item.Show.IDs.Shikimori
	seen := map[string]bool{}
	for _, q := range []string{d.Item.Show.Names.RU, d.Item.Show.Names.EN, d.Item.Title} {
		if q == "" {
			continue
		}
		if err := c.alternativesOf(ctx, q, shiki, k, seen, &out); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (c *Catalog) alternativesOf(ctx context.Context, q, shiki string, k catalog.Key, seen map[string]bool, out *catalog.AlternativesView) error {
	return c.p.Search(ctx, catalog.SearchQuery{Query: q}, func(b catalog.SearchBatch) error {
		out.Asked++
		if b.Failure != nil {
			out.Failed++
		}
		for _, it := range b.Items {
			if it.Show == nil || it.Show.IDs.Shikimori != shiki || seen[it.Key.String()] {
				continue
			}
			seen[it.Key.String()] = true
			v := catalog.AlternativeView{Ref: c.withRef(it).Ref, Provider: it.Key.Provider, Source: it.Key.Source,
				SourceName: string(it.Key.Source), Title: it.Title, Match: "shikimori", EpisodesAired: it.Show.EpisodesAired,
				Current: it.Key == k, Voiceovers: []catalog.AltVoiceover{}}
			out.Items = append(out.Items, v)
		}
		return nil
	})
}
