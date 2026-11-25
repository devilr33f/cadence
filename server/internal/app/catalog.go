package app

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/catalog/ytcatalog"
	"git.shino.boo/devilreef/cadence/server/internal/images"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ryd"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytdlp"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytmusic"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/streams"
)

// catalogInstanceCheck takes a catalog provider instance (torrplayer,
// torrserver) or a music one (youtube-music) for catalogSources.
func catalogInstanceCheck(ref func(ctx context.Context, name string) (providers.Ref, error)) func(context.Context, string) error {
	return func(ctx context.Context, name string) error {
		r, err := ref(ctx, name)
		switch {
		case errors.Is(err, providers.ErrNotFound), errors.Is(err, providers.ErrUnknownModule):
			return settings.ErrInvalid
		case err != nil:
			return err
		case r.Kind != providers.KindCatalog && r.Kind != providers.KindMusic:
			return settings.ErrInvalid
		}
		return nil
	}
}

type catalogSourceDeps struct {
	global    func(ctx context.Context) (settings.Values, error)
	instances *providers.Instances
	relay     ytcatalog.Relay
	// web reads YouTube itself for the youtube-music instance's video source
	web ytcatalog.Web
	log *slog.Logger
}

// catalogSources builds the providers catalogSources names, in its order,
// at each call: a setting or an instance change applies without a restart.
// A youtube-music instance is wrapped with the musicPlayer instance; one
// that does not build (or is another music module) is left out, logged by
// name only.
func catalogSources(d catalogSourceDeps) func(ctx context.Context) []catalog.Configured {
	var mu sync.Mutex
	wrapped := map[string]*ytcatalog.Provider{}
	type pair struct {
		m providers.Music
		p providers.Player
	}
	built := map[string]pair{}
	return func(ctx context.Context) []catalog.Configured {
		g, err := d.global(ctx)
		if err != nil {
			return nil
		}
		var out []catalog.Configured
		for _, name := range g.CatalogSources {
			p, err := providers.Get[catalog.Provider](ctx, d.instances, name, providers.KindCatalog)
			if err == nil {
				out = append(out, catalog.Configured{Instance: name, Provider: p})
				continue
			}
			if !errors.Is(err, providers.ErrWrongKind) {
				d.log.Warn("catalog_source_unavailable", "instance", name)
				continue
			}
			m, err := d.instances.Music(ctx, name)
			if _, yt := m.(*ytmusic.Client); err != nil || !yt {
				d.log.Warn("catalog_source_unavailable", "instance", name)
				continue
			}
			var pl providers.Player
			if g.MusicPlayer != "" {
				pl, _ = d.instances.Player(ctx, g.MusicPlayer)
			}
			mu.Lock()
			w := wrapped[name]
			if b := built[name]; w == nil || b.m != m || b.p != pl {
				w = ytcatalog.New(ytcatalog.Config{Music: m, Player: pl, Relay: d.relay, Web: d.web})
				wrapped[name], built[name] = w, pair{m, pl}
			}
			mu.Unlock()
			out = append(out, catalog.Configured{Instance: name, Provider: w})
		}
		return out
	}
}

// catalogRelay has the device fetch a provider's server-bound stream (YouTube's)
// from the relay at the public origin; without an origin the device fetches
// the upstream itself.
func catalogRelay(origin string, relay *streams.Relay) ytcatalog.Relay {
	if origin == "" {
		return nil
	}
	return func(ctx context.Context, deviceID string, ups []streams.Upstream) ([]string, error) {
		token, err := relay.AddSet(deviceID, ups, streams.Options{OnFirstByte: func() { ytdlp.RelayFirstByte(ctx) }})
		if err != nil {
			return nil, err
		}
		out := make([]string, len(ups))
		for i := range ups {
			out[i] = origin + "/v2/stream/" + token + "/" + strconv.Itoa(i)
		}
		return out, nil
	}
}

// proxiedImage is a public image url as a screen fetches it, through the image proxy at the public origin; nil
// without an origin, so the screen gets the url itself
func proxiedImage(origin string, p *images.Proxy) func(rawURL string) string {
	if origin == "" {
		return nil
	}
	return func(rawURL string) string {
		id := p.Ref(rawURL, false)
		if id == "" {
			return ""
		}
		return origin + "/v2/images/" + id
	}
}

// ownImages answers a session cover that is one of the server's own proxied images (origin + /v2/images/<id>) from
// the proxy itself, so the dashboard and Home Assistant get it without the server fetching its own address
func ownImages(origin string, p *images.Proxy) func(ctx context.Context, rawURL string) (string, []byte, bool, error) {
	prefix := origin + "/v2/images/"
	return func(ctx context.Context, rawURL string) (string, []byte, bool, error) {
		id, ok := strings.CutPrefix(rawURL, prefix)
		if origin == "" || !ok || id == "" || strings.ContainsAny(id, "/?#") {
			return "", nil, false, nil
		}
		ct, data, err := p.Get(ctx, id)
		return ct, data, true, err
	}
}

// imageURL is an image id's url at the public origin, "" without one
func imageURL(origin string) func(id string) string {
	if origin == "" {
		return nil
	}
	return func(id string) string { return origin + "/v2/images/" + id }
}

// agentCatalog is the agent's catalog (agent.Catalog: the catalog tools and
// the screen note) over the catalog service, in the household's refs: the
// service's views back in the contract's types, actions under the ids the
// service issued.
type agentCatalog struct{ s *catalog.Service }

var _ agent.Catalog = agentCatalog{}

func (a agentCatalog) Cached(ref catalog.Ref) (catalog.Item, bool) {
	return a.s.Peek(context.Background(), catalog.Household, ref)
}

// Series is an episode's show as the catalog last handed it out (tools' seriesOf)
func (a agentCatalog) Series(ref catalog.Ref) (catalog.Item, bool) {
	return a.s.Series(context.Background(), catalog.Household, ref)
}

func (a agentCatalog) Search(ctx context.Context, query string, limit int) (agent.CatalogFound, error) {
	return a.SearchWith(ctx, agent.CatalogQuery{Query: query, Limit: limit})
}

func (a agentCatalog) SearchWith(ctx context.Context, q agent.CatalogQuery) (agent.CatalogFound, error) {
	var out agent.CatalogFound
	limit := q.Limit
	at := map[catalog.Ref]int{}
	req := catalog.SearchRequest{Query: q.Query, Kinds: q.Kinds, Providers: q.Providers, Sources: q.Sources, Sort: q.Sort,
		Filters: q.Filters, Limit: limit, Cursor: q.Cursor}
	err := a.s.Search(ctx, catalog.Household, req, func(ev catalog.SearchEvent) error {
		if ev.Pending != nil {
			out.Asked = len(ev.Pending)
		}
		if ev.Failure != nil {
			out.Failed++
		}
		if ev.Next != "" {
			out.Next = ev.Next
		}
		out.Unenforced = append(out.Unenforced, ev.Unenforced...)
		for _, it := range ev.Items {
			if _, ok := at[it.Ref]; !ok && (limit <= 0 || len(out.Items) < limit) {
				at[it.Ref] = len(out.Items)
				out.Items = append(out.Items, it)
			}
		}
		// a title group grown by a later source
		for _, it := range ev.Updated {
			if i, ok := at[it.Ref]; ok {
				out.Items[i] = it
			}
		}
		return nil
	})
	return out, err
}

func (a agentCatalog) Item(ctx context.Context, r agent.CatalogItem) (catalog.ItemDetail, error) {
	v, err := a.s.ItemIn(ctx, catalog.Household, r.Ref, catalog.ItemPage{Cursor: r.Cursor, Group: r.Group, Tab: r.Tab,
		Sort: r.Sort, Query: r.Query, Limit: r.Limit}, nil)
	if err != nil {
		return catalog.ItemDetail{}, err
	}
	d := catalog.ItemDetail{Item: v.Item, Children: v.Children, Next: v.Next, Variants: v.Variants,
		Actions: actionsOf(v.Actions), Tabs: v.Tabs, Sorts: v.Sorts, Sort: v.Sort, Partial: v.Partial}
	if r.Limit > 0 && len(d.Children) > r.Limit {
		d.Children = d.Children[:r.Limit]
	}
	for _, g := range v.ChildGroups {
		d.ChildGroups = append(d.ChildGroups, catalog.ChildGroup(g))
	}
	if v.Selected != "" {
		if k, err := a.s.Lookup(ctx, catalog.Household, v.Selected); err == nil {
			d.Selected = &k
		}
	}
	if v.Continue != nil {
		d.Continue = &catalog.ContinueAt{Episode: v.Continue.Episode, PositionS: v.Continue.PositionS}
	}
	for _, p := range v.Panels {
		d.Panels = append(d.Panels, catalog.Panel(p))
	}
	return d, nil
}

func actionsOf(vs []catalog.ActionView) []catalog.Action {
	out := make([]catalog.Action, 0, len(vs))
	for _, v := range vs {
		out = append(out, catalog.Action(v))
	}
	return out
}

func (a agentCatalog) Options(ctx context.Context, ref catalog.Ref, episode string, sel catalog.Selection) (catalog.Options, error) {
	v, err := a.s.Options(ctx, catalog.Household, ref, episode, sel)
	if err != nil {
		return catalog.Options{}, err
	}
	o := catalog.Options{Selected: v.Selected, Remembered: v.Remembered}
	for _, g := range v.Groups {
		cg := catalog.ChoiceGroup{Kind: g.Kind, Label: g.Label, Hidden: g.Hidden}
		for _, c := range g.Choices {
			ch := catalog.Choice{ID: c.ID, Label: c.Label, Detail: c.Detail, Badges: c.Badges, Unavailable: c.Unavailable,
				Reason: c.Reason}
			if c.Stream != nil {
				ch.Stream = &catalog.StreamSource{Translator: c.Stream.Translator, TransType: c.Stream.TransType,
					Language: c.Stream.Language, Quality: c.Stream.Quality}
			}
			if c.Torrent != nil {
				t := catalog.TorrentSource(*c.Torrent)
				ch.Torrent = &t
			}
			cg.Choices = append(cg.Choices, ch)
		}
		o.Groups = append(o.Groups, cg)
	}
	return o, nil
}

func (a agentCatalog) Resolve(ctx context.Context, r agent.CatalogResolve) (catalog.Playable, error) {
	prof := r.Profile
	v, err := a.s.Resolve(ctx, catalog.Household, catalog.PlayRequest{Ref: r.Ref, Episode: r.Episode, Choices: r.Selection,
		From: r.From, DeviceID: r.DeviceID, Profile: &prof})
	if err != nil {
		return catalog.Playable{}, err
	}
	p := catalog.Playable{Kind: v.Kind, Selected: v.Selected, Episode: v.Episode, Variant: v.Ref}
	if s := v.Stream; s != nil {
		st := &catalog.Stream{URL: s.URL, AudioURL: s.AudioURL, ResumeMS: s.ResumeMS, DurationMS: s.DurationMS, Live: s.Live}
		if s.Expires != nil {
			st.Expires = *s.Expires
		}
		for _, x := range s.Renditions {
			st.Renditions = append(st.Renditions, catalog.Rendition{Height: x.Height, FPS: x.FPS, VCodec: x.VCodec, URL: x.URL,
				AudioURL: x.AudioURL})
		}
		for _, x := range s.Subtitles {
			st.Subtitles = append(st.Subtitles, catalog.Subtitle(x))
		}
		for _, x := range s.Skips {
			st.Skips = append(st.Skips, catalog.Skip(x))
		}
		p.Stream = st
	}
	if h := v.Handoff; h != nil {
		p.Handoff = &catalog.Handoff{Provider: h.Provider, Target: h.Target}
	}
	if v.MatchedBy != "" {
		if k, err := a.s.Lookup(ctx, catalog.Household, v.MatchedBy); err == nil {
			p.MatchedBy = &k
		}
	}
	return p, nil
}

func (a agentCatalog) Act(ctx context.Context, ref catalog.Ref, action string, on bool, value string) ([]catalog.Action, error) {
	vs, err := a.s.Act(ctx, catalog.Household, catalog.ActCall{Ref: ref, Action: action, On: on, Value: value})
	if err != nil {
		return nil, err
	}
	return actionsOf(vs), nil
}

func (a agentCatalog) Alternatives(ctx context.Context, ref catalog.Ref) (catalog.AlternativesView, error) {
	return a.s.Alternatives(ctx, catalog.Household, ref)
}

// youtubeVotes reads videos' votes from Return YouTube Dislike while the
// global youtubeVotes setting is on, and nothing otherwise: the third party
// learns no id the owner did not allow
func youtubeVotes(global func(ctx context.Context) (settings.Values, error), c *ryd.Client) tools.VotesFunc {
	return func(ctx context.Context, _ string, ids []string) map[string]tools.Votes {
		g, err := global(ctx)
		if err != nil || !g.YouTubeVotes {
			return nil
		}
		out := map[string]tools.Votes{}
		for id, v := range c.Votes(ctx, ids) {
			out[id] = tools.Votes{Likes: v.Likes, Dislikes: v.Dislikes}
		}
		return out
	}
}
