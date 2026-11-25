package catalog

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// target is what a ref names: a provider's item, or a title group with the
// variant its choices pick
type target struct {
	p     Provider
	key   Key
	group *group
	// variant is the group's picked variant
	variant Key
	// recalled says the variant is the group's remembered source
	recalled bool
}

// resolveRef finds a ref's provider and, for a title group, its variant:
// the source chosen, else the remembered one, else the first
func (s *Service) resolveRef(ctx context.Context, household string, r Ref, chosen string) (target, error) {
	k, err := s.d.Refs.Lookup(ctx, household, r)
	if err != nil {
		return target{}, err
	}
	p, ok := s.provider(ctx, k.Provider)
	if !ok {
		return target{}, ErrUnknownRef
	}
	t := target{p: p, key: k, variant: k}
	if k.Kind != KindTitleGroup {
		return t, nil
	}
	g, ok := s.groupOf(household, k)
	if !ok {
		return target{}, ErrUnknownRef
	}
	t.group = g
	vs := s.variants(g)
	pick := func(src string) bool {
		for _, v := range vs {
			if string(v.Key.Source) == src {
				t.variant = v.Key
				return true
			}
		}
		return false
	}
	if chosen != "" && pick(chosen) {
		return t, nil
	}
	if mem, err := s.d.Choices.Recall(ctx, choiceID(household, k)); err == nil && pick(mem[GroupSource]) {
		t.recalled = true
		return t, nil
	}
	t.variant = vs[0].Key
	return t, nil
}

// titleOf is the key choices are remembered by: a variant without its episode
func titleOf(k Key) Key {
	if k.Kind == KindEpisode {
		k.Kind = KindSeries
	}
	k.Episode = ""
	return k
}

func (s *Service) recall(ctx context.Context, household string, p Provider, k Key) Selection {
	var sel Selection
	var err error
	if m, ok := p.(ChoiceMemory); ok {
		sel, err = m.Recall(ctx, titleOf(k))
	} else {
		sel, err = s.d.Choices.Recall(ctx, choiceID(household, titleOf(k)))
	}
	if err != nil || sel == nil {
		return Selection{}
	}
	return sel
}

func (s *Service) remember(ctx context.Context, household string, p Provider, k Key, sel Selection) {
	var err error
	if m, ok := p.(ChoiceMemory); ok {
		err = m.Remember(ctx, titleOf(k), sel)
	} else {
		err = s.d.Choices.Remember(ctx, choiceID(household, titleOf(k)), sel)
	}
	if err != nil {
		s.d.Log.Warn("catalog_remember_failed", "provider", string(p.ID()), "code", string(codeOf(err)))
	}
}

func (s *Service) need(p Provider, op Capability) error {
	if !p.Capabilities().Has(op) {
		return ErrUnsupported
	}
	return nil
}

func callCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, callTimeout)
}

// BrowseRequest's path for the service is "<provider>/<path>"; "" is Home,
// every provider's root.
func splitPath(path string) (ProviderID, string) {
	p, rest, _ := strings.Cut(path, "/")
	return ProviderID(p), rest
}

// Browse reads Home (every provider's root sections, in the owner's order)
// or one provider's feed or tree node.
func (s *Service) Browse(ctx context.Context, household string, path, cursor string, filters map[string]string, limit int) (PageView, error) {
	ctx, cancel := callCtx(ctx)
	defer cancel()
	if path == "" {
		cs := s.d.Providers(ctx)
		pages := make([]PageView, len(cs))
		var wg sync.WaitGroup
		for i, c := range cs {
			if !c.Provider.Capabilities().Has(CapBrowse) {
				continue
			}
			wg.Go(func() {
				pg, err := s.browseOne(ctx, household, c.Provider, BrowseRequest{Limit: limit})
				if err != nil {
					s.d.Log.Warn("catalog_browse_failed", "provider", string(c.Provider.ID()), "code", string(codeOf(err)))
					return
				}
				pages[i] = pg
			})
		}
		wg.Wait()
		out := PageView{Sections: []SectionView{}}
		for _, pg := range pages {
			out.Sections = append(out.Sections, pg.Sections...)
		}
		return out, nil
	}
	id, rest := splitPath(path)
	p, ok := s.provider(ctx, id)
	if !ok {
		return PageView{}, ErrNotFound
	}
	if err := s.need(p, CapBrowse); err != nil {
		return PageView{}, err
	}
	return s.browseOne(ctx, household, p, BrowseRequest{Path: rest, Cursor: cursor, Filters: filters, Limit: limit})
}

func (s *Service) browseOne(ctx context.Context, household string, p Provider, r BrowseRequest) (PageView, error) {
	pg, err := p.Browse(ctx, r)
	if err != nil {
		return PageView{}, err
	}
	out := PageView{Sections: []SectionView{}, Next: pg.Next}
	for _, sec := range pg.Sections {
		items, err := s.outAll(ctx, household, p, sec.Items)
		if err == nil {
			s.enrichCached(p, items)
		}
		if err != nil {
			return PageView{}, err
		}
		v := SectionView{ID: string(p.ID()) + "/" + sec.ID, Provider: p.ID(), Title: sec.Title, Items: items, Next: sec.Next}
		if sec.Path != "" {
			v.Path = string(p.ID()) + "/" + sec.Path
		}
		out.Sections = append(out.Sections, v)
	}
	return out, nil
}

// overlay fills a read item's fields its provider left out from what was
// handed out before (torrplayer has no title read)
func (s *Service) overlay(household string, it Item) Item {
	prev, ok := s.kept(household, it.Key)
	if !ok {
		return it
	}
	if it.Title == "" || it.Title == it.Key.ID {
		it.Title = prev.Title
	}
	if it.Artwork.URL == "" {
		it.Artwork = prev.Artwork
	}
	if it.Show == nil || (it.Show.Description == "" && prev.Show != nil && len(it.Show.Genres) == 0) {
		if prev.Show != nil {
			show := *prev.Show
			it.Show = &show
		}
	}
	return it
}

// Item reads an item with its remembered choices.
func (s *Service) Item(ctx context.Context, household string, r Ref, cursor, childGroup string) (DetailView, error) {
	return s.ItemWithChoices(ctx, household, r, cursor, childGroup, nil)
}

// ItemPage is one view of an item's children: a page (Cursor), a child
// group, a tab, a sort, a query inside it.
type ItemPage struct {
	Cursor string
	Group  string
	Tab    string
	Sort   Sort
	Query  string
	Limit  int
}

// ItemIn reads an item in one view of its children (ItemPage).
func (s *Service) ItemIn(ctx context.Context, household string, r Ref, pg ItemPage, sel Selection) (DetailView, error) {
	return s.itemIn(ctx, household, r, pg, sel)
}

// ItemWithChoices reads an item: a title group answers its variants, the variant its
// choices pick and that variant's episodes, panels and actions. A source in sel
// chooses the variant for this read without remembering it (playing does that).
func (s *Service) ItemWithChoices(ctx context.Context, household string, r Ref, cursor, childGroup string,
	sel Selection) (DetailView, error) {
	return s.itemIn(ctx, household, r, ItemPage{Cursor: cursor, Group: childGroup}, sel)
}

func (s *Service) itemIn(ctx context.Context, household string, r Ref, pg ItemPage, sel Selection) (DetailView, error) {
	cursor := pg.Cursor
	ctx, cancel := callCtx(ctx)
	defer cancel()
	t, err := s.resolveRef(ctx, household, r, sel[GroupSource])
	if err != nil {
		return DetailView{}, err
	}
	if err := s.need(t.p, CapItem); err != nil {
		return DetailView{}, err
	}
	d, err := t.p.Item(ctx, ItemRequest{Key: t.variant, Cursor: cursor, Group: pg.Group, Limit: pg.Limit, Tab: pg.Tab,
		Sort: pg.Sort, Query: pg.Query})
	if err != nil {
		return DetailView{}, err
	}
	d.Item = s.overlay(household, d.Item)
	out := DetailView{Next: d.Next, Children: []Item{}, Panels: []PanelView{}, Actions: s.actionsOut(t.variant, d.Actions),
		Tabs: d.Tabs, Sorts: d.Sorts, Sort: d.Sort, Partial: d.Partial}
	if out.Children, err = s.outAll(ctx, household, t.p, d.Children); err != nil {
		return DetailView{}, err
	}
	for _, g := range d.ChildGroups {
		out.ChildGroups = append(out.ChildGroups, ChildGroupView(g))
	}
	if d.Continue != nil {
		out.Continue = &ContinueView{Episode: d.Continue.Episode, PositionS: d.Continue.PositionS}
	}
	for _, pn := range d.Panels {
		out.Panels = append(out.Panels, PanelView(pn))
	}
	if t.group == nil {
		if out.Item, err = s.out(ctx, household, t.p, d.Item); err != nil {
			return DetailView{}, err
		}
		s.enrich(ctx, t.p, &out.Item, cursor == "")
		if out.Variants, err = s.outAll(ctx, household, t.p, d.Variants); err != nil {
			return DetailView{}, err
		}
		return out, nil
	}
	if out.Item, err = s.out(ctx, household, t.p, s.groupItem(t.group)); err != nil {
		return DetailView{}, err
	}
	s.enrich(ctx, t.p, &out.Item, cursor == "")
	for _, v := range s.variants(t.group) {
		if v.Key == t.variant {
			v = s.overlay(household, d.Item)
			v.Key = t.variant
		}
		o, err := s.out(ctx, household, t.p, v)
		if err != nil {
			return DetailView{}, err
		}
		out.Variants = append(out.Variants, o)
		if v.Key == t.variant {
			out.Selected = o.Ref
		}
	}
	return out, nil
}

// sourceGroup is a title group's variants as the source choice group
func (s *Service) sourceGroup(ctx context.Context, t target) ChoiceGroup {
	names := map[SourceID]string{}
	if srcs, err := t.p.Sources(ctx); err == nil {
		for _, src := range srcs {
			names[src.ID] = src.Name
		}
	}
	g := ChoiceGroup{Kind: GroupSource, Label: "Источник"}
	for _, v := range s.variants(t.group) {
		c := Choice{ID: string(v.Key.Source), Label: names[v.Key.Source], Unavailable: v.Unavailable}
		if c.Label == "" {
			c.Label = string(v.Key.Source)
		}
		if v.Show != nil && v.Show.EpisodesAired > 0 {
			c.Detail = strconv.Itoa(v.Show.EpisodesAired) + " эп."
		}
		c.Badges = v.Badges
		g.Choices = append(g.Choices, c)
	}
	return g
}

// Options answers an item's choice groups for an episode: a title group's
// source first, then its variant's; groups left out of sel take the
// remembered choice, then the provider's default.
func (s *Service) Options(ctx context.Context, household string, r Ref, episode string, sel Selection) (OptionsView, error) {
	ctx, cancel := callCtx(ctx)
	defer cancel()
	t, err := s.resolveRef(ctx, household, r, sel[GroupSource])
	if err != nil {
		return OptionsView{}, err
	}
	if err := s.need(t.p, CapOptions); err != nil {
		return OptionsView{}, err
	}
	mem := s.recall(ctx, household, t.p, t.variant)
	asked := Selection{}.Merge(sel)
	delete(asked, GroupSource)
	o, err := t.p.Options(ctx, OptionsRequest{Key: t.variant, Episode: episode, Selection: asked.Merge(mem)})
	if err != nil {
		return OptionsView{}, err
	}
	groups := o.Groups
	selected := Selection{}.Merge(o.Selected)
	if t.group != nil {
		groups = append([]ChoiceGroup{s.sourceGroup(ctx, t)}, groups...)
		selected[GroupSource] = string(t.variant.Source)
	}
	out := OptionsView{Groups: []ChoiceGroupView{}, Selected: selected}
	for _, g := range groups {
		v := ChoiceGroupView{Kind: g.Kind, Label: g.Label, Hidden: g.Hidden, Choices: []ChoiceView{}}
		for _, c := range g.Choices {
			v.Choices = append(v.Choices, choiceView(c))
		}
		out.Groups = append(out.Groups, v)
	}
	for k, v := range mem {
		if sel[k] == "" && selected[k] == v {
			out.Remembered = append(out.Remembered, k)
		}
	}
	if t.recalled {
		out.Remembered = append(out.Remembered, GroupSource)
	}
	slices.Sort(out.Remembered)
	return out, nil
}

// PlayRequest is a resolve as the device asked it.
type PlayRequest struct {
	Ref      Ref         `json:"ref"`
	Episode  string      `json:"episode,omitempty"`
	Choices  Selection   `json:"choices,omitempty"`
	From     ResolveFrom `json:"from,omitempty"`
	DeviceID string      `json:"-"`
	OnStage  func(Stage) `json:"-"`
	// Profile, when set, is used instead of the device's
	Profile *DeviceProfile `json:"-"`
}

// Resolve turns a ref and the choices into a playable for the device, and
// remembers the choices it resolved: the source of a title group in the
// service's store, the rest in the provider's memory (torrplayer's prefs)
// or the store. Its urls are signed: never logged, stored or shown to the
// model.
func (s *Service) Resolve(ctx context.Context, household string, r PlayRequest) (PlayableView, error) {
	t, err := s.resolveRef(ctx, household, r.Ref, r.Choices[GroupSource])
	if err != nil {
		return PlayableView{}, err
	}
	if err := s.need(t.p, CapResolve); err != nil {
		return PlayableView{}, err
	}
	prof := DeviceProfile{}
	switch {
	case r.Profile != nil:
		prof = *r.Profile
	case s.d.Profile != nil && r.DeviceID != "":
		prof = s.d.Profile(ctx, r.DeviceID)
	}
	asked := Selection{}.Merge(r.Choices)
	delete(asked, GroupSource)
	if _, ok := t.p.(ChoiceMemory); !ok {
		asked = asked.Merge(s.recall(ctx, household, t.p, t.variant))
	}
	from := r.From
	if from == "" {
		from = FromResume
	}
	pctx := WithDevice(ctx, r.DeviceID)
	pl, err := t.p.Resolve(pctx, ResolveRequest{Key: t.variant, Episode: r.Episode, Selection: asked, From: from,
		Profile: prof, OnStage: r.OnStage})
	if err != nil {
		s.d.Log.Info("catalog_resolve_failed", "provider", string(t.p.ID()), "source", string(t.variant.Source),
			"code", string(codeOf(err)))
		return PlayableView{}, err
	}
	if len(pl.Selected) > 0 {
		s.remember(ctx, household, t.p, t.variant, pl.Selected)
	}
	if t.group != nil {
		_ = s.d.Choices.Remember(ctx, choiceID(household, t.key), Selection{GroupSource: string(t.variant.Source)})
	}
	vref, err := s.d.Refs.Issue(ctx, household, t.variant)
	if err != nil {
		return PlayableView{}, err
	}
	out := PlayableView{Kind: pl.Kind, Stream: streamView(pl.Stream), Selected: pl.Selected, Episode: pl.Episode, Ref: vref}
	if t.group != nil {
		if out.Selected == nil {
			out.Selected = Selection{}
		}
		out.Selected[GroupSource] = string(t.variant.Source)
	}
	if pl.Handoff != nil {
		out.Handoff = &HandoffView{Provider: pl.Handoff.Provider, Target: pl.Handoff.Target}
	}
	if pl.MatchedBy != nil {
		if out.MatchedBy, err = s.d.Refs.Issue(ctx, household, *pl.MatchedBy); err != nil {
			return PlayableView{}, err
		}
	}
	s.d.Log.Info("catalog_resolved", "provider", string(t.p.ID()), "source", string(t.variant.Source),
		"kind", string(pl.Kind), "subtitles", len(out.subtitles()), "renditions", out.renditions())
	return out, nil
}

func (v PlayableView) subtitles() []SubtitleView {
	if v.Stream == nil {
		return nil
	}
	return v.Stream.Subtitles
}

func (v PlayableView) renditions() int {
	if v.Stream == nil {
		return 0
	}
	return len(v.Stream.Renditions)
}

// ActCall is an action as the device asked it.
type ActCall struct {
	Ref    Ref    `json:"ref"`
	Action string `json:"action"`
	On     bool   `json:"on,omitempty"`
	Value  string `json:"value,omitempty"`
}

// ErrUnknownAction is an action id the service did not issue for the ref,
// or one that expired.
var ErrUnknownAction = errors.New("catalog: unknown action")

// Act runs an action the service issued for the ref and answers the item's
// actions with their new state (and new ids).
func (s *Service) Act(ctx context.Context, household string, c ActCall) ([]ActionView, error) {
	ctx, cancel := callCtx(ctx)
	defer cancel()
	t, err := s.resolveRef(ctx, household, c.Ref, "")
	if err != nil {
		return nil, err
	}
	a, ok := s.takeAction(c.Action)
	if !ok || (a.key != t.variant && a.key != t.key) {
		return nil, ErrUnknownAction
	}
	if err := s.need(t.p, CapAct); err != nil {
		return nil, err
	}
	acts, err := t.p.Act(ctx, ActRequest{Key: a.key, Action: a.id, On: c.On, Value: c.Value})
	if err != nil {
		s.d.Log.Info("catalog_act_failed", "provider", string(t.p.ID()), "code", string(codeOf(err)))
		return nil, err
	}
	return s.actionsOut(a.key, acts), nil
}

// Panel reads a page of an item's panel as data; PanelCards renders it.
func (s *Service) Panel(ctx context.Context, household string, r Ref, panel, cursor string) (PanelData, error) {
	ctx, cancel := callCtx(ctx)
	defer cancel()
	t, err := s.resolveRef(ctx, household, r, "")
	if err != nil {
		return PanelData{}, err
	}
	if err := s.need(t.p, CapPanel); err != nil {
		return PanelData{}, err
	}
	pg, err := t.p.Panel(ctx, PanelRequest{Key: t.variant, Panel: panel, Cursor: cursor})
	if err != nil {
		return PanelData{}, err
	}
	out := PanelData{Text: pg.Text, Facts: pg.Facts, Lyrics: pg.Lyrics, Chapters: pg.Chapters, Next: pg.Next}
	for _, c := range pg.Comments {
		if c.Avatar.URL != "" && s.d.Images != nil {
			c.Avatar.Hash = s.d.Images.Ref(c.Avatar.URL, false)
		}
		out.Comments = append(out.Comments, c)
	}
	if out.Items, err = s.outAll(ctx, household, t.p, pg.Items); err != nil {
		return PanelData{}, err
	}
	return out, nil
}

// Report records playback progress where the provider keeps it; a provider
// that keeps none answers nil.
func (s *Service) Report(ctx context.Context, household string, r Ref, episode string, positionS, durationS int, watched bool) error {
	ctx, cancel := callCtx(ctx)
	defer cancel()
	t, err := s.resolveRef(ctx, household, r, "")
	if err != nil {
		return err
	}
	if !t.p.Capabilities().Has(CapReport) {
		return nil
	}
	return t.p.Report(ctx, ProgressReport{Key: t.variant, Episode: episode, PositionS: positionS, DurationS: durationS,
		Watched: watched})
}
