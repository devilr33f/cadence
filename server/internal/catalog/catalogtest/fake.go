// Package catalogtest holds an in-memory catalog provider, an in-memory ref
// table and Contract, the checks every real provider's tests run.
package catalogtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"strings"
	"sync"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
)

// Entry is one item of a Fake and what hangs off it.
type Entry struct {
	Item     catalog.Item
	Children []catalog.Key
	Variants []catalog.Key
	Continue *catalog.ContinueAt
	// Groups are the item's choice groups for every episode
	Groups  []catalog.ChoiceGroup
	Play    *catalog.Playable
	Panels  []catalog.Panel
	Pages   map[string]catalog.PanelPage
	Actions []catalog.Action
}

// Fake is a catalog.Provider over entries in memory. Search matches titles by
// substring, one batch per source (Fail makes a source's batch a failure),
// in the order of SourceList. It is also a catalog.ChoiceMemory.
type Fake struct {
	Provider   catalog.ProviderID
	Caps       catalog.Capabilities
	SourceList []catalog.SourceInfo
	// Fail is a failure code per source (SourceID "" for a provider without)
	Fail map[catalog.SourceID]catalog.FailureCode
	// Feeds are Browse paths to their items
	Feeds map[string][]catalog.Key
	// BrowseErr fails every Browse with it (an upstream down, a 502)
	BrowseErr error

	mu      sync.Mutex
	entries map[string]*Entry
	order   []string
	memory  map[string]catalog.Selection
	reports []catalog.ProgressReport
}

var (
	_ catalog.Provider     = (*Fake)(nil)
	_ catalog.ChoiceMemory = (*Fake)(nil)
)

// Add stores e under its item's Key, filling in the item's provider.
func (f *Fake) Add(e Entry) catalog.Key {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.entries == nil {
		f.entries = map[string]*Entry{}
	}
	e.Item.Key.Provider = f.Provider
	e.Item.Provider = f.Provider
	e.Item.Source = e.Item.Key.Source
	e.Item.Kind = e.Item.Key.Kind
	e.Item.HasChildren = len(e.Children) > 0 || len(e.Variants) > 0
	e.Item.Playable = e.Play != nil
	k := e.Item.Key.String()
	if _, ok := f.entries[k]; !ok {
		f.order = append(f.order, k)
	}
	f.entries[k] = &e
	return e.Item.Key
}

// Reports answers what Report recorded.
func (f *Fake) Reports() []catalog.ProgressReport {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reports)
}

func (f *Fake) ID() catalog.ProviderID             { return f.Provider }
func (f *Fake) Capabilities() catalog.Capabilities { return f.Caps }

func (f *Fake) Sources(context.Context) ([]catalog.SourceInfo, error) {
	return slices.Clone(f.SourceList), nil
}

func (f *Fake) entry(k catalog.Key) (*Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.entries[k.String()]
	if !ok {
		return nil, catalog.ErrNotFound
	}
	return e, nil
}

func (f *Fake) items(keys []catalog.Key) []catalog.Item {
	out := []catalog.Item{}
	for _, k := range keys {
		if e, err := f.entry(k); err == nil {
			out = append(out, e.Item)
		}
	}
	return out
}

func (f *Fake) Search(ctx context.Context, q catalog.SearchQuery, emit func(catalog.SearchBatch) error) error {
	if !f.Caps.Has(catalog.CapSearch) {
		return catalog.ErrUnsupported
	}
	sources := []catalog.SourceID{""}
	if len(f.SourceList) > 0 {
		sources = nil
		for _, s := range f.SourceList {
			if s.Enabled && (len(q.Sources) == 0 || slices.Contains(q.Sources, s.ID)) {
				sources = append(sources, s.ID)
			}
		}
	}
	for _, src := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		b := catalog.SearchBatch{Provider: f.Provider, Source: src}
		if code, ok := f.Fail[src]; ok {
			b.Failure = &catalog.Failure{Provider: f.Provider, Source: src, Code: code}
		} else {
			b.Items = f.match(q, src)
		}
		if err := emit(b); err != nil {
			return err
		}
	}
	return nil
}

func (f *Fake) match(q catalog.SearchQuery, src catalog.SourceID) []catalog.Item {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []catalog.Item{}
	needle := strings.ToLower(q.Query)
	for _, k := range f.order {
		it := f.entries[k].Item
		if it.Source != src || (len(q.Kinds) > 0 && !slices.Contains(q.Kinds, it.Kind)) {
			continue
		}
		if it.Kind == catalog.KindEpisode || !strings.Contains(strings.ToLower(it.Title), needle) {
			continue
		}
		out = append(out, it)
		if q.Limit > 0 && len(out) == q.Limit {
			break
		}
	}
	return out
}

func (f *Fake) Browse(_ context.Context, r catalog.BrowseRequest) (catalog.BrowsePage, error) {
	if !f.Caps.Has(catalog.CapBrowse) {
		return catalog.BrowsePage{}, catalog.ErrUnsupported
	}
	if f.BrowseErr != nil {
		return catalog.BrowsePage{}, f.BrowseErr
	}
	keys, ok := f.Feeds[r.Path]
	if !ok {
		return catalog.BrowsePage{}, catalog.ErrNotFound
	}
	return catalog.BrowsePage{Sections: []catalog.Section{{ID: r.Path, Path: r.Path, Items: f.items(keys)}}}, nil
}

func (f *Fake) Item(ctx context.Context, r catalog.ItemRequest) (catalog.ItemDetail, error) {
	if !f.Caps.Has(catalog.CapItem) {
		return catalog.ItemDetail{}, catalog.ErrUnsupported
	}
	e, err := f.entry(r.Key)
	if err != nil {
		return catalog.ItemDetail{}, err
	}
	d := catalog.ItemDetail{
		Item: e.Item, Children: f.items(e.Children), Variants: f.items(e.Variants), Continue: e.Continue,
		Panels: slices.Clone(e.Panels), Actions: slices.Clone(e.Actions),
	}
	if len(e.Variants) > 0 {
		sel, _ := f.Recall(ctx, r.Key)
		for _, v := range e.Variants {
			if string(v.Source) == sel[catalog.GroupSource] {
				d.Selected = &v
			}
		}
		if d.Selected == nil {
			d.Selected = &e.Variants[0]
		}
	}
	return d, nil
}

func (f *Fake) Options(_ context.Context, r catalog.OptionsRequest) (catalog.Options, error) {
	if !f.Caps.Has(catalog.CapOptions) {
		return catalog.Options{}, catalog.ErrUnsupported
	}
	e, err := f.entry(r.Key)
	if err != nil {
		return catalog.Options{}, err
	}
	return catalog.Options{Groups: slices.Clone(e.Groups), Selected: f.selection(e, r.Selection)}, nil
}

// selection is the request's, then the remembered one, then each group's
// first playable choice.
func (f *Fake) selection(e *Entry, s catalog.Selection) catalog.Selection {
	f.mu.Lock()
	remembered := f.memory[e.Item.Key.String()]
	f.mu.Unlock()
	defaults := catalog.Selection{}
	for _, g := range e.Groups {
		for _, c := range g.Choices {
			if !c.Unavailable {
				defaults[g.Kind] = c.ID
				break
			}
		}
	}
	return s.Merge(remembered.Merge(defaults))
}

func (f *Fake) Resolve(_ context.Context, r catalog.ResolveRequest) (catalog.Playable, error) {
	if !f.Caps.Has(catalog.CapResolve) {
		return catalog.Playable{}, catalog.ErrUnsupported
	}
	e, err := f.entry(r.Key)
	if err != nil {
		return catalog.Playable{}, err
	}
	if e.Play == nil {
		return catalog.Playable{}, catalog.ErrUnplayable
	}
	sel := f.selection(e, r.Selection)
	for _, g := range e.Groups {
		for _, c := range g.Choices {
			if sel[g.Kind] == c.ID && c.Unavailable {
				return catalog.Playable{}, &catalog.Failure{Provider: f.Provider, Source: e.Item.Source, Code: c.Reason}
			}
		}
	}
	if r.OnStage != nil {
		if e.Play.Stream != nil && e.Play.Stream.InfoHash != "" {
			r.OnStage(catalog.StageConnectingPeers)
		}
		r.OnStage(catalog.StageStarting)
	}
	p := *e.Play
	p.Selected, p.Episode = sel, r.Episode
	if p.Stream != nil {
		s := *p.Stream
		if r.From == catalog.FromStart {
			s.ResumeMS = 0
		}
		p.Stream = &s
	}
	return p, nil
}

func (f *Fake) Act(_ context.Context, r catalog.ActRequest) ([]catalog.Action, error) {
	if !f.Caps.Has(catalog.CapAct) {
		return nil, catalog.ErrUnsupported
	}
	e, err := f.entry(r.Key)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range e.Actions {
		a := &e.Actions[i]
		if a.ID != r.Action {
			continue
		}
		if !a.Enabled {
			return nil, catalog.ErrRefused
		}
		if len(a.Values) > 0 {
			if r.Value != "" && !slices.Contains(a.Values, r.Value) {
				return nil, catalog.ErrRefused
			}
			a.Value, a.Toggled = r.Value, r.Value != ""
		} else {
			a.Toggled = r.On
		}
		return slices.Clone(e.Actions), nil
	}
	return nil, catalog.ErrNotFound
}

func (f *Fake) Panel(_ context.Context, r catalog.PanelRequest) (catalog.PanelPage, error) {
	if !f.Caps.Has(catalog.CapPanel) {
		return catalog.PanelPage{}, catalog.ErrUnsupported
	}
	e, err := f.entry(r.Key)
	if err != nil {
		return catalog.PanelPage{}, err
	}
	p, ok := e.Pages[r.Panel+"\x1f"+r.Cursor]
	if !ok {
		return catalog.PanelPage{}, catalog.ErrNotFound
	}
	return p, nil
}

func (f *Fake) Report(_ context.Context, r catalog.ProgressReport) error {
	if !f.Caps.Has(catalog.CapReport) {
		return catalog.ErrUnsupported
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, r)
	return nil
}

func (f *Fake) Recall(_ context.Context, title catalog.Key) (catalog.Selection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps(f.memory[title.String()]), nil
}

func (f *Fake) Remember(_ context.Context, title catalog.Key, s catalog.Selection) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.memory == nil {
		f.memory = map[string]catalog.Selection{}
	}
	f.memory[title.String()] = maps(s)
	return nil
}

func maps(s catalog.Selection) catalog.Selection {
	out := catalog.Selection{}
	for k, v := range s {
		out[k] = v
	}
	return out
}

// Refs is an in-memory catalog.RefIssuer: random refs per household, kept
// for the life of the value.
type Refs struct {
	mu    sync.Mutex
	byRef map[catalog.Ref]refEntry
	byKey map[string]catalog.Ref
}

type refEntry struct {
	household string
	key       catalog.Key
}

var _ catalog.RefIssuer = (*Refs)(nil)

func (r *Refs) Issue(_ context.Context, household string, k catalog.Key) (catalog.Ref, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byRef == nil {
		r.byRef, r.byKey = map[catalog.Ref]refEntry{}, map[string]catalog.Ref{}
	}
	id := household + "\x1e" + k.String()
	if ref, ok := r.byKey[id]; ok {
		return ref, nil
	}
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	ref := catalog.Ref("c:" + hex.EncodeToString(b))
	r.byRef[ref] = refEntry{household, k}
	r.byKey[id] = ref
	return ref, nil
}

func (r *Refs) Lookup(_ context.Context, household string, ref catalog.Ref) (catalog.Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byRef[ref]
	if !ok || e.household != household {
		return catalog.Key{}, catalog.ErrUnknownRef
	}
	return e.key, nil
}
