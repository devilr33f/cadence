package catalog

import (
	"slices"
	"time"
)

// group is one title merged across sources by GroupKey: its variants by
// provider, source and id, never by which source answered first, so the first
// variant (the default source) does not change from one search to the next
type group struct {
	key     Key
	members []Key
	items   map[string]Item
	at      time.Time
}

func groupKeyOf(p ProviderID, gk string) Key {
	return Key{Provider: p, Kind: KindTitleGroup, ID: gk}
}

// joinGroup adds a variant to its title group, made when it is the first,
// and answers whether the variant is new to it
func (s *Service) joinGroup(household, gk string, it Item) (*group, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := household + "\x1e" + string(it.Key.Provider) + "\x1e" + gk
	g, ok := s.groups[id]
	if !ok {
		g = &group{key: groupKeyOf(it.Key.Provider, gk), items: map[string]Item{}}
		s.groups[id] = g
		s.gorder = append(s.gorder, id)
		if len(s.gorder) > maxGroups {
			delete(s.groups, s.gorder[0])
			s.gorder = s.gorder[1:]
		}
	}
	g.at = s.d.Now()
	k := it.Key.String()
	_, had := g.items[k]
	g.items[k] = it
	if !had {
		g.members = append(g.members, it.Key)
		slices.SortFunc(g.members, func(a, b Key) int {
			x, y := g.items[a.String()], g.items[b.String()]
			if c := compare(string(a.Provider), string(b.Provider)); c != 0 {
				return c
			}
			if c := compare(string(x.Source), string(y.Source)); c != 0 {
				return c
			}
			return compare(a.ID, b.ID)
		})
	}
	return g, !had
}

// groupOf is a title group by its key
func (s *Service) groupOf(household string, k Key) (*group, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[household+"\x1e"+string(k.Provider)+"\x1e"+k.ID]
	if !ok {
		return nil, false
	}
	return g, true
}

// variants are a group's members in their order
func (s *Service) variants(g *group) []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Item, 0, len(g.members))
	for _, k := range g.members {
		out = append(out, g.items[k.String()])
	}
	return out
}

// groupItem is the title group as one item: its first variant's fields, the
// counts of all of them
func (s *Service) groupItem(g *group) Item {
	vs := s.variants(g)
	it := vs[0]
	it.Key, it.Ref, it.Source, it.Kind = g.key, "", "", KindTitleGroup
	it.Playable, it.HasChildren, it.Progress = true, true, nil
	show := ShowInfo{}
	if it.Show != nil {
		show = *it.Show
	}
	show.SourcesWithIt = len(vs)
	all := true
	for _, v := range vs {
		if it.Artwork.URL == "" && v.Artwork.URL != "" {
			it.Artwork = v.Artwork
		}
		if !v.Unavailable {
			all = false
		}
		if v.Show == nil {
			continue
		}
		show.EpisodesAired = max(show.EpisodesAired, v.Show.EpisodesAired)
		show.EpisodesTotal = max(show.EpisodesTotal, v.Show.EpisodesTotal)
		for _, vo := range v.Show.Voiceovers {
			if !slices.Contains(show.Voiceovers, vo) {
				show.Voiceovers = append(show.Voiceovers, vo)
			}
		}
		if show.Description == "" {
			show.Description = v.Show.Description
		}
		if show.Names.EN == "" {
			show.Names.EN = v.Show.Names.EN
		}
	}
	it.Show = &show
	it.Unavailable = all
	it.Badges = slices.DeleteFunc(slices.Clone(it.Badges), func(b Badge) bool { return b.Kind == BadgeTorrent })
	return it
}
