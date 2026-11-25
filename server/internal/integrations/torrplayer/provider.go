package torrplayer

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
)

const (
	// myListSource keys a Shikimori my-list entry, resolved to a variant on read
	myListSource catalog.SourceID = "shikimori"
	// episodes per child group once a title has more than this many
	groupSize   = 50
	feedLimit   = 20
	titleMemory = 2048
)

// sourceNames are the display names of torrplayer's sources; one it adds
// later shows its id
var sourceNames = map[string]string{
	"aniliberty": "AniLiberty", "anime365": "anime365", "animego": "AnimeGo", "yummyanime": "YummyAnime",
	"animevost": "AnimeVost", "sameband": "Sameband", "nyaa": "Nyaa", "animebytes": "AnimeBytes",
	"torznab": "Torznab", "torrent": "Torrents",
}

// torrentSources are indexers: their sources are torrents
var torrentSources = []string{"nyaa", "animebytes", "torznab", "torrent"}

// my-list statuses torrplayer takes, in the picker's order
var myListStatuses = []string{"watching", "planned", "completed", "on_hold", "dropped", "rewatching"}

// Provider is the torrplayer catalog provider; it is also the
// catalog.ChoiceMemory over torrplayer's sticky per-title prefs.
type Provider struct {
	c *Client

	mu     sync.Mutex
	titles map[string]titleDTO
	order  []string

	rates rateCache
}

var (
	_ catalog.Provider     = (*Provider)(nil)
	_ catalog.ChoiceMemory = (*Provider)(nil)
	_ catalog.UserRates    = (*Provider)(nil)
)

// New is a provider over c.
func New(c *Client) *Provider { return &Provider{c: c, titles: map[string]titleDTO{}} }

func (p *Provider) ID() catalog.ProviderID { return catalog.ProviderTorrplayer }

func (p *Provider) Capabilities() catalog.Capabilities {
	return catalog.Capabilities{catalog.CapSearch, catalog.CapBrowse, catalog.CapItem, catalog.CapOptions,
		catalog.CapResolve, catalog.CapAct, catalog.CapPanel, catalog.CapReport}
}

// OwnsArtwork says a poster is torrplayer's own (its signed /image proxy on
// the configured host), which the image proxy fetches from the LAN.
func (p *Provider) OwnsArtwork(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host == p.c.Host() && (u.Scheme == "http" || u.Scheme == "https")
}

// poster moves torrplayer's signed artwork url (/api/v1/image?u=&s=) onto
// the configured base. torrplayer builds it on its stream server's origin,
// which is another port than the control API when the two are split (its
// [http] and [control] ports), and that port serves no /api/v1/image; the
// signature covers the upstream url alone, so the control API answers it.
// any other url is kept as it is
func (p *Provider) poster(raw string) string {
	u, err := url.Parse(raw)
	if raw == "" || err != nil || !strings.HasSuffix(u.Path, "/api/v1/image") {
		return raw
	}
	q := u.Query()
	if q.Get("u") == "" || q.Get("s") == "" {
		return raw
	}
	return p.c.endpoint("/image", url.Values{"u": {q.Get("u")}, "s": {q.Get("s")}})
}

func (p *Provider) Sources(ctx context.Context) ([]catalog.SourceInfo, error) {
	st, err := p.c.status(ctx)
	if err != nil {
		return nil, p.fail(err, "")
	}
	out := make([]catalog.SourceInfo, 0, len(st.Providers))
	for _, id := range st.Providers {
		out = append(out, sourceInfo(id))
	}
	return out, nil
}

func sourceInfo(id string) catalog.SourceInfo {
	name := sourceNames[id]
	if name == "" {
		name = id
	}
	return catalog.SourceInfo{ID: catalog.SourceID(id), Name: name, Enabled: true,
		Torrents: slices.Contains(torrentSources, id)}
}

// fail maps a client error to the catalog's: a code, never upstream text
func (p *Provider) fail(err error, source catalog.SourceID) error {
	f := &catalog.Failure{Provider: catalog.ProviderTorrplayer, Source: source, Code: catalog.FailFailed}
	var he *httpError
	var ne net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return err
	case errors.Is(err, context.DeadlineExceeded):
		f.Code = catalog.FailTimeout
	case errors.As(err, &he):
		f.HTTPStatus = he.status
		switch he.status {
		case http.StatusNotFound:
			return catalog.ErrNotFound
		case http.StatusBadRequest:
			return catalog.ErrRefused
		case http.StatusUnauthorized, http.StatusForbidden:
			f.Code = catalog.FailAuth
		case http.StatusTooManyRequests:
			f.Code = catalog.FailRateLimited
		case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
			f.Code = catalog.FailUnavailable
		}
	case errors.As(err, &ne):
		f.Code = catalog.FailUnavailable
	}
	return f
}

func titleKey(source, id string) string { return source + "\x1f" + id }

// remember keeps a title's search fields, which Item, Panel and my list read
// again (torrplayer has no title read)
func (p *Provider) remember(t titleDTO) {
	p.mu.Lock()
	defer p.mu.Unlock()
	k := titleKey(t.Provider, t.ID)
	if _, ok := p.titles[k]; !ok {
		p.order = append(p.order, k)
		if len(p.order) > titleMemory {
			delete(p.titles, p.order[0])
			p.order = p.order[1:]
		}
	}
	p.titles[k] = t
}

func (p *Provider) title(source catalog.SourceID, id string) (titleDTO, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.titles[titleKey(string(source), id)]
	return t, ok
}

func (p *Provider) titleItem(t titleDTO) catalog.Item {
	kind := catalog.KindSeries
	if strings.EqualFold(t.Kind, "movie") {
		kind = catalog.KindMovie
	}
	k := catalog.Key{Provider: catalog.ProviderTorrplayer, Source: catalog.SourceID(t.Provider), Kind: kind, ID: t.ID}
	title := t.NameRu
	if title == "" {
		title = t.Name
	}
	it := catalog.Item{
		Key: k, Provider: k.Provider, Source: k.Source, Kind: kind, Title: title, Subtitle: t.NameEn,
		Artwork:  catalog.Artwork{URL: p.poster(t.Poster), Aspect: "poster"},
		Playable: true, HasChildren: true, Unavailable: !t.Available && t.Provider == "",
		Show: &catalog.ShowInfo{
			Names: catalog.Names{RU: t.NameRu, EN: t.NameEn}, Year: t.Year, Format: t.Kind,
			EpisodesAired: t.EpisodesAired, EpisodesTotal: t.EpisodesTotal, Status: t.Status, Season: t.Season,
			Genres: t.Genres, Studios: t.Studios, Score: t.Score, AgeRating: t.AgeRating, Description: t.Description,
		},
	}
	if t.ShikimoriID > 0 {
		it.Show.IDs.Shikimori = strconv.Itoa(t.ShikimoriID)
	}
	if t.MALID > 0 {
		it.Show.IDs.MAL = strconv.Itoa(t.MALID)
	}
	if t.Ongoing || t.Status == "ongoing" {
		it.Badges = append(it.Badges, catalog.Badge{Kind: catalog.BadgeOngoing})
	}
	if slices.Contains(torrentSources, t.Provider) {
		it.Badges = append(it.Badges, catalog.Badge{Kind: catalog.BadgeTorrent})
	}
	return it
}

// filterParams are the catalog's filter names as torrplayer's query takes them
var filterParams = map[string]string{
	"genre": "genre", "studio": "studio", "kind": "kind", "status": "status", "season": "season",
	"year": "year", "year_from": "year_from", "year_to": "year_to", "min_score": "min_score", "sort": "sort",
	"age_rating": "age_rating",
}

func searchQuery(q catalog.SearchQuery, source string, browse bool) url.Values {
	v := url.Values{}
	if q.Query != "" {
		v.Set("q", q.Query)
	}
	if source != "" {
		v.Set("provider", source)
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if n, err := strconv.Atoi(q.Cursor); err == nil && n > 1 {
		v.Set("page", strconv.Itoa(n))
	}
	if browse {
		v.Set("browse", "true")
	}
	for k, val := range q.Filters {
		if param, ok := filterParams[k]; ok && val != "" {
			v.Set(param, val)
		}
	}
	return v
}

// unknownFilters are filters torrplayer has no name for: unenforced everywhere
func unknownFilters(f map[string]string) []string {
	var out []string
	for k := range f {
		if _, ok := filterParams[k]; !ok {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func wantsShows(kinds []catalog.Kind) bool {
	return len(kinds) == 0 || slices.ContainsFunc(kinds, func(k catalog.Kind) bool {
		return k == catalog.KindSeries || k == catalog.KindMovie || k == catalog.KindTitleGroup
	})
}

// Search asks each source on its own, all at once, so each batch goes out
// as its source answers.
func (p *Provider) Search(ctx context.Context, q catalog.SearchQuery, emit func(catalog.SearchBatch) error) error {
	st, err := p.c.status(ctx)
	if err != nil {
		return p.fail(err, "")
	}
	var sources []string
	for _, s := range st.Providers {
		if len(q.Sources) == 0 || slices.Contains(q.Sources, catalog.SourceID(s)) {
			sources = append(sources, s)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	batches := make(chan catalog.SearchBatch, len(sources))
	var wg sync.WaitGroup
	for _, src := range sources {
		wg.Go(func() { batches <- p.searchSource(ctx, q, src) })
	}
	go func() { wg.Wait(); close(batches) }()
	for b := range batches {
		if err := emit(b); err != nil {
			cancel()
			for range batches { //nolint:revive // drain so every source's goroutine ends
			}
			return err
		}
	}
	return ctx.Err()
}

func (p *Provider) searchSource(ctx context.Context, q catalog.SearchQuery, src string) catalog.SearchBatch {
	b := catalog.SearchBatch{Provider: catalog.ProviderTorrplayer, Source: catalog.SourceID(src), Items: []catalog.Item{},
		Unenforced: unknownFilters(q.Filters)}
	if !wantsShows(q.Kinds) {
		return b
	}
	out, err := p.c.search(ctx, searchQuery(q, src, false))
	if err != nil {
		code := catalog.FailFailed
		var f *catalog.Failure
		if errors.As(p.fail(err, b.Source), &f) {
			code = f.Code
		}
		b.Items, b.Failure = nil, &catalog.Failure{Provider: catalog.ProviderTorrplayer, Source: b.Source, Code: code}
		return b
	}
	if len(out.Titles) == 0 && len(out.Errors) > 0 {
		b.Items, b.Failure = nil, &catalog.Failure{Provider: catalog.ProviderTorrplayer, Source: b.Source, Code: catalog.FailFailed}
		return b
	}
	for _, t := range out.Titles {
		if t.Provider != src {
			continue
		}
		p.remember(t)
		b.Items = append(b.Items, p.titleItem(t))
	}
	b.Unenforced = append(b.Unenforced, out.Unenforced...)
	if out.HasMore {
		b.Next = strconv.Itoa(max(out.Page, 1) + 1)
	}
	return b
}

func (p *Provider) Browse(ctx context.Context, r catalog.BrowseRequest) (catalog.BrowsePage, error) {
	switch r.Path {
	case catalog.FeedRoot:
		var page catalog.BrowsePage
		for _, path := range []string{catalog.FeedContinue, catalog.FeedNewEpisodes, catalog.FeedMyList, catalog.FeedSubscriptions} {
			sub, err := p.Browse(ctx, catalog.BrowseRequest{Path: path, Limit: feedLimit})
			if err != nil || len(sub.Sections) == 0 || len(sub.Sections[0].Items) == 0 {
				continue
			}
			page.Sections = append(page.Sections, sub.Sections[0])
		}
		return page, nil
	case catalog.FeedContinue:
		out, err := p.c.watching(ctx, limitOr(r.Limit))
		if err != nil {
			return catalog.BrowsePage{}, p.fail(err, "")
		}
		items := make([]catalog.Item, 0, len(out.Items))
		for _, w := range out.Items {
			it := p.watchingItem(w)
			items = append(items, it)
		}
		return section(r.Path, "Продолжить", items), nil
	case catalog.FeedMyList:
		status := r.Filters["status"]
		if status == "" {
			status = "watching"
		}
		out, err := p.c.myList(ctx, status, limitOr(r.Limit))
		if err != nil {
			return catalog.BrowsePage{}, p.fail(err, "")
		}
		items := make([]catalog.Item, 0, len(out.Items))
		for _, m := range out.Items {
			items = append(items, p.myListItem(m))
		}
		return section(r.Path, "Мой список", items), nil
	case catalog.FeedSubscriptions:
		out, err := p.c.subscriptions(ctx)
		if err != nil {
			return catalog.BrowsePage{}, p.fail(err, "")
		}
		items := make([]catalog.Item, 0, len(out.Subscriptions))
		for _, s := range out.Subscriptions {
			it := p.variantItem(s.Provider, s.TitleID, s.Name, "")
			if s.ShikimoriID > 0 {
				it.Show.IDs.Shikimori = strconv.Itoa(s.ShikimoriID)
			}
			items = append(items, it)
		}
		return section(r.Path, "Подписки", items), nil
	case catalog.FeedNewEpisodes:
		// A torrplayer without the endpoint answers 404, which the root
		// browse takes as "no row" rather than a failure: the home screen
		// simply shows the feed of every instance that has one.
		pageNo := 1
		if n, err := strconv.Atoi(r.Cursor); err == nil && n > 1 {
			pageNo = n
		}
		out, err := p.c.schedule(ctx, pageNo, limitOr(r.Limit))
		if err != nil {
			return catalog.BrowsePage{}, p.fail(err, "")
		}
		page := section(r.Path, "Сейчас на экранах", p.feedItems(out.Titles))
		if out.HasMore {
			page.Next = strconv.Itoa(max(out.Page, 1) + 1)
		}
		return page, nil
	case catalog.FeedCatalogue:
		out, err := p.c.search(ctx, searchQuery(catalog.SearchQuery{Filters: r.Filters, Limit: r.Limit, Cursor: r.Cursor}, "", true))
		if err != nil {
			return catalog.BrowsePage{}, p.fail(err, "")
		}
		page := section(r.Path, "Каталог", p.feedItems(out.Titles))
		if out.HasMore {
			page.Next = strconv.Itoa(max(out.Page, 1) + 1)
		}
		return page, nil
	}
	return catalog.BrowsePage{}, catalog.ErrNotFound
}

// feedItems maps browse rows onto items: a linked row opens by its provider
// ids, an unlinked one by its Shikimori id, which my-list resolve maps to a
// variant when a provider has it
func (p *Provider) feedItems(titles []titleDTO) []catalog.Item {
	items := make([]catalog.Item, 0, len(titles))
	for _, t := range titles {
		if t.Provider == "" {
			it := p.titleItem(t)
			if it.Show.IDs.Shikimori == "" {
				continue
			}
			it.Key.Source, it.Source, it.Key.ID = myListSource, myListSource, it.Show.IDs.Shikimori
			items = append(items, it)
			continue
		}
		p.remember(t)
		items = append(items, p.titleItem(t))
	}
	return items
}

func limitOr(n int) int {
	if n <= 0 {
		return feedLimit
	}
	return n
}

func section(path, title string, items []catalog.Item) catalog.BrowsePage {
	return catalog.BrowsePage{Sections: []catalog.Section{{ID: path, Title: title, Path: path, Items: items}}}
}

// variantItem is a variant known by ids and a name alone, its search fields
// filled in when this provider saw it
func (p *Provider) variantItem(source, id, name, poster string) catalog.Item {
	if t, ok := p.title(catalog.SourceID(source), id); ok {
		return p.titleItem(t)
	}
	it := p.titleItem(titleDTO{Provider: source, ID: id, Name: name, Poster: poster, Available: true})
	return it
}

// shikimoriPoster is Shikimori's own cover of an anime: the one torrplayer draws
// in its feeds in place of a provider's (its enrich.PosterURL), as provider
// covers vary in aspect and size while Shikimori's are all 2:3
func shikimoriPoster(id int) string {
	return "https://shikimori.one/system/animes/original/" + strconv.Itoa(id) + ".jpg"
}

func (p *Provider) watchingItem(w watchingItemDTO) catalog.Item {
	it := p.variantItem(w.Provider, w.TitleID, w.Name, w.Poster)
	if w.ShikimoriID > 0 && it.Show.IDs.Shikimori == "" {
		it.Show.IDs.Shikimori = strconv.Itoa(w.ShikimoriID)
	}
	// continue watching draws the uniform 2:3 cover, as torrplayer's own feed
	// does; a title Shikimori does not know keeps its provider's poster
	if w.ShikimoriID > 0 {
		it.Artwork = catalog.Artwork{URL: shikimoriPoster(w.ShikimoriID), Aspect: "poster"}
	}
	it.Progress = &catalog.Progress{Count: w.WatchedCount, Total: w.EpisodesTotal, PositionS: w.ResumeSec,
		UpdatedAt: w.LastWatchedAt}
	return it
}

func (p *Provider) myListItem(m myListItemDTO) catalog.Item {
	id := strconv.Itoa(m.ShikimoriID)
	k := catalog.Key{Provider: catalog.ProviderTorrplayer, Source: myListSource, Kind: catalog.KindSeries, ID: id}
	it := catalog.Item{
		Key: k, Provider: k.Provider, Source: k.Source, Kind: k.Kind, Title: m.Name,
		Artwork: catalog.Artwork{URL: p.poster(m.Poster), Aspect: "poster"}, Playable: true, HasChildren: true,
		Show: &catalog.ShowInfo{IDs: catalog.ExternalIDs{Shikimori: id}, EpisodesTotal: m.EpisodesTotal,
			EpisodesAired: m.EpisodesAired, Format: m.Kind},
		Progress: &catalog.Progress{Count: m.Watched, Total: m.EpisodesTotal},
	}
	if m.MALID > 0 {
		it.Show.IDs.MAL = strconv.Itoa(m.MALID)
	}
	return it
}

// variant answers the key of a variant: a my-list entry is resolved to the
// variant torrplayer plays it from
func (p *Provider) variant(ctx context.Context, k catalog.Key) (catalog.Key, error) {
	switch {
	case k.ID == "" || k.Source == "":
		return k, catalog.ErrNotFound
	case k.Source != myListSource:
		return k, nil
	}
	out, err := p.c.myListResolve(ctx, k.ID)
	if err != nil {
		return k, p.fail(err, "")
	}
	if out.Provider == "" || out.TitleID == "" {
		return k, catalog.ErrUnplayable
	}
	v := catalog.Key{Provider: catalog.ProviderTorrplayer, Source: catalog.SourceID(out.Provider),
		Kind: catalog.KindSeries, ID: out.TitleID, Episode: k.Episode}
	if k.Kind == catalog.KindEpisode {
		v.Kind = catalog.KindEpisode
	}
	return v, nil
}

func episodeItem(variant catalog.Key, e episodeDTO) catalog.Item {
	k := variant
	k.Kind, k.Episode = catalog.KindEpisode, e.Label
	it := catalog.Item{
		Key: k, Provider: k.Provider, Source: k.Source, Kind: k.Kind, Title: e.Name, DurationS: e.Duration,
		Playable: true, Episode: &catalog.EpisodeInfo{Label: e.Label, Ordinal: e.Ordinal, Name: e.Name, Type: e.Type},
	}
	if it.Title == "" {
		it.Title = e.Label
	}
	if it.DurationS == 0 && e.DurationSec > 0 {
		it.DurationS = int(e.DurationSec)
	}
	if e.Watched || e.ResumeSec > 0 {
		it.Progress = &catalog.Progress{Watched: e.Watched, PositionS: int(e.ResumeSec)}
	}
	return it
}

func (p *Provider) Item(ctx context.Context, r catalog.ItemRequest) (catalog.ItemDetail, error) {
	k, err := p.variant(ctx, r.Key)
	if err != nil {
		return catalog.ItemDetail{}, err
	}
	eps, err := p.c.episodes(ctx, string(k.Source), k.ID)
	if err != nil {
		return catalog.ItemDetail{}, p.fail(err, k.Source)
	}
	series := k
	series.Episode = ""
	if series.Kind == catalog.KindEpisode {
		series.Kind = catalog.KindSeries
		if t, ok := p.title(k.Source, k.ID); ok && strings.EqualFold(t.Kind, "movie") {
			series.Kind = catalog.KindMovie
		}
	}
	if k.Kind == catalog.KindEpisode {
		for _, e := range eps.Episodes {
			if e.Label == k.Episode {
				it := episodeItem(series, e)
				return catalog.ItemDetail{Item: it, Actions: []catalog.Action{watchedAction(e.Watched)}}, nil
			}
		}
		return catalog.ItemDetail{}, catalog.ErrNotFound
	}
	d := catalog.ItemDetail{Item: p.variantItem(string(series.Source), series.ID, "", "")}
	d.Item.Key = series
	d.Item.Kind = series.Kind
	if _, ok := p.title(series.Source, series.ID); !ok {
		if pr, err := p.c.progress(ctx, string(series.Source), series.ID); err == nil && pr.Found {
			d.Item.Title, d.Item.Artwork.URL = pr.Name, p.poster(pr.Poster)
			if pr.ShikimoriID > 0 {
				d.Item.Show.IDs.Shikimori = strconv.Itoa(pr.ShikimoriID)
			}
			d.Item.Show.EpisodesTotal = pr.EpisodesTotal
		}
	}
	all := make([]catalog.Item, 0, len(eps.Episodes))
	watched := 0
	for _, e := range eps.Episodes {
		all = append(all, episodeItem(series, e))
		if e.Watched {
			watched++
		}
	}
	d.Item.Progress = &catalog.Progress{Count: watched, Total: max(len(all), d.Item.Show.EpisodesTotal)}
	d.Children, d.ChildGroups = groupChildren(all, r.Group, eps.NextEpisode)
	if eps.NextEpisode != "" {
		at := catalog.ContinueAt{Episode: eps.NextEpisode}
		for _, e := range eps.Episodes {
			if e.Label == eps.NextEpisode {
				at.PositionS = int(e.ResumeSec)
			}
		}
		d.Continue = &at
	}
	d.Actions = p.actions(ctx, series, d.Item)
	if d.Item.Show.Description != "" || len(d.Item.Show.Genres) > 0 {
		d.Panels = []catalog.Panel{{ID: "description", Kind: catalog.PanelDescription, Title: "Описание"}}
	}
	return d, nil
}

// groupChildren splits a long list into ranges of groupSize: the group asked
// for, else the one holding the next episode
func groupChildren(all []catalog.Item, group, next string) ([]catalog.Item, []catalog.ChildGroup) {
	if len(all) <= groupSize {
		return all, nil
	}
	var groups []catalog.ChildGroup
	pick := -1
	for i := 0; i < len(all); i += groupSize {
		end := min(i+groupSize, len(all))
		id := all[i].Episode.Label + "-" + all[end-1].Episode.Label
		groups = append(groups, catalog.ChildGroup{ID: id, Label: id, Count: end - i})
		if id == group {
			pick = len(groups) - 1
		}
		if group == "" && pick < 0 && slices.ContainsFunc(all[i:end], func(it catalog.Item) bool { return it.Episode.Label == next }) {
			pick = len(groups) - 1
		}
	}
	pick = max(pick, 0)
	start := pick * groupSize
	return all[start:min(start+groupSize, len(all))], groups
}

func watchedAction(on bool) catalog.Action {
	return catalog.Action{ID: "watched", Kind: catalog.ActionWatched, Label: "Просмотрено", Icon: "check",
		Toggled: on, Enabled: true}
}

// actions are a variant's: my list (by its Shikimori id) and subscribe
func (p *Provider) actions(ctx context.Context, k catalog.Key, it catalog.Item) []catalog.Action {
	my := catalog.Action{ID: "my_list", Kind: catalog.ActionMyList, Label: "Мой список", Icon: "bookmark",
		Values: myListStatuses, Enabled: it.Show != nil && it.Show.IDs.Shikimori != ""}
	sub := catalog.Action{ID: "subscribe", Kind: catalog.ActionSubscribe, Label: "Подписаться", Icon: "bell", Enabled: true}
	if out, err := p.c.subscriptions(ctx); err == nil {
		sub.Toggled = slices.ContainsFunc(out.Subscriptions, func(s subscriptionDTO) bool {
			return s.Provider == string(k.Source) && s.TitleID == k.ID
		})
	}
	return []catalog.Action{my, sub}
}

func episodeOf(k catalog.Key, episode string) string {
	if episode != "" {
		return episode
	}
	return k.Episode
}

// offer is an episode's sources as choice groups
type offer struct {
	streams  []sourceDTO
	torrents []sourceDTO
	hidden   int
	// translators in rank order, each one's sources best quality first
	order []string
	by    map[string][]sourceDTO
}

func voiceID(s sourceDTO) string {
	if s.Translator != "" {
		return s.Translator
	}
	if s.TransType != "" {
		return s.TransType
	}
	return "default"
}

func newOffer(out sourcesOut) offer {
	o := offer{hidden: out.HiddenCount, by: map[string][]sourceDTO{}}
	for _, s := range out.Sources {
		if s.Hidden {
			continue
		}
		if s.Kind == kindTorrent {
			o.torrents = append(o.torrents, s)
			continue
		}
		o.streams = append(o.streams, s)
		id := voiceID(s)
		if _, ok := o.by[id]; !ok {
			o.order = append(o.order, id)
		}
		o.by[id] = append(o.by[id], s)
	}
	for _, id := range o.order {
		slices.SortStableFunc(o.by[id], func(a, b sourceDTO) int { return b.Quality - a.Quality })
	}
	return o
}

// defaults is torrplayer's own pick: its first source, ranked by the owner's
// filters and the title's sticky prefs
func (o offer) defaults(out sourcesOut) catalog.Selection {
	for _, s := range out.Sources {
		if s.Hidden {
			continue
		}
		if s.Kind == kindTorrent {
			return catalog.Selection{catalog.GroupTorrent: s.ID}
		}
		return catalog.Selection{catalog.GroupVoiceover: voiceID(s), catalog.GroupQuality: strconv.Itoa(s.Quality)}
	}
	return catalog.Selection{}
}

// layer puts over on base: a voiceover chosen clears a torrent, a torrent
// chosen clears the voiceover and quality
func layer(base, over catalog.Selection) catalog.Selection {
	out := catalog.Selection{}
	for k, v := range base {
		out[k] = v
	}
	if over[catalog.GroupVoiceover] != "" {
		delete(out, catalog.GroupTorrent)
	}
	if over[catalog.GroupTorrent] != "" {
		delete(out, catalog.GroupVoiceover)
		delete(out, catalog.GroupQuality)
	}
	for k, v := range over {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// settle keeps a selection to what the episode offers: an unknown voiceover
// takes the default, a quality the voiceover lacks its best
func (o offer) settle(s, defaults catalog.Selection) catalog.Selection {
	if t := s[catalog.GroupTorrent]; t != "" {
		if slices.ContainsFunc(o.torrents, func(x sourceDTO) bool { return x.ID == t }) {
			return catalog.Selection{catalog.GroupTorrent: t}
		}
		s = layer(s, defaults)
		if s[catalog.GroupTorrent] != "" {
			return catalog.Selection{catalog.GroupTorrent: s[catalog.GroupTorrent]}
		}
	}
	v := s[catalog.GroupVoiceover]
	if _, ok := o.by[v]; !ok {
		v = defaults[catalog.GroupVoiceover]
		if _, ok := o.by[v]; !ok {
			if len(o.order) == 0 {
				return catalog.Selection{}
			}
			v = o.order[0]
		}
	}
	q := s[catalog.GroupQuality]
	if !slices.ContainsFunc(o.by[v], func(x sourceDTO) bool { return strconv.Itoa(x.Quality) == q }) {
		q = strconv.Itoa(o.by[v][0].Quality)
	}
	return catalog.Selection{catalog.GroupVoiceover: v, catalog.GroupQuality: q}
}

func transBadge(t string) []catalog.Badge {
	switch t {
	case "voice":
		return []catalog.Badge{{Kind: catalog.BadgeVoice}}
	case "sub":
		return []catalog.Badge{{Kind: catalog.BadgeSub}}
	case "raw":
		return []catalog.Badge{{Kind: catalog.BadgeRaw}}
	}
	return nil
}

func (o offer) groups(sel catalog.Selection) []catalog.ChoiceGroup {
	var out []catalog.ChoiceGroup
	if len(o.order) > 0 {
		g := catalog.ChoiceGroup{Kind: catalog.GroupVoiceover, Label: "Озвучка", Hidden: o.hidden}
		for _, id := range o.order {
			best := o.by[id][0]
			g.Choices = append(g.Choices, catalog.Choice{
				ID: id, Label: id, Detail: best.Lang, Badges: transBadge(best.TransType),
				Stream: &catalog.StreamSource{Translator: best.Translator, TransType: catalog.TransType(best.TransType),
					Language: best.Lang, Quality: best.Quality},
			})
		}
		out = append(out, g)
		if v := sel[catalog.GroupVoiceover]; v != "" {
			q := catalog.ChoiceGroup{Kind: catalog.GroupQuality, Label: "Качество"}
			for _, s := range o.by[v] {
				id := strconv.Itoa(s.Quality)
				if slices.ContainsFunc(q.Choices, func(c catalog.Choice) bool { return c.ID == id }) {
					continue
				}
				label := id + "p"
				if s.Quality == 0 {
					label = "Авто"
				}
				q.Choices = append(q.Choices, catalog.Choice{ID: id, Label: label})
			}
			out = append(out, q)
		}
	}
	if len(o.torrents) > 0 {
		g := catalog.ChoiceGroup{Kind: catalog.GroupTorrent, Label: "Торренты"}
		if len(o.order) == 0 {
			g.Hidden = o.hidden
		}
		for _, s := range o.torrents {
			c := catalog.Choice{
				ID: s.ID, Label: s.Label, Badges: []catalog.Badge{{Kind: catalog.BadgeTorrent}},
				Torrent: &catalog.TorrentSource{Episodes: s.Episodes, SizeBytes: s.SizeBytes, Seeders: s.Seeders,
					Leechers: s.Leechers, Quality: s.Quality, Codec: s.Codec, Hardsub: s.Hardsub},
			}
			if c.Label == "" {
				c.Label = s.Episodes
			}
			if s.Hardsub {
				c.Badges = append(c.Badges, catalog.Badge{Kind: catalog.BadgeHardsub})
			}
			if s.Seeders == 0 {
				c.Unavailable, c.Reason = true, catalog.FailNoSeeders
			}
			g.Choices = append(g.Choices, c)
		}
		out = append(out, g)
	}
	return out
}

// episodeSources reads the sources and the selection they settle to: the
// request's, then the remembered prefs, then torrplayer's ranking
func (p *Provider) episodeSources(ctx context.Context, k catalog.Key, episode string, sel catalog.Selection) (offer, catalog.Selection, error) {
	out, err := p.c.sources(ctx, string(k.Source), k.ID, episode)
	if err != nil {
		return offer{}, nil, p.fail(err, k.Source)
	}
	o := newOffer(out)
	remembered, _ := p.Recall(ctx, k)
	def := o.defaults(out)
	return o, o.settle(layer(layer(def, remembered), sel), def), nil
}

func (p *Provider) Options(ctx context.Context, r catalog.OptionsRequest) (catalog.Options, error) {
	k, err := p.variant(ctx, r.Key)
	if err != nil {
		return catalog.Options{}, err
	}
	episode := episodeOf(k, r.Episode)
	o, sel, err := p.episodeSources(ctx, k, episode, r.Selection)
	if err != nil {
		return catalog.Options{}, err
	}
	return catalog.Options{Groups: o.groups(sel), Selected: sel}, nil
}

func (p *Provider) Resolve(ctx context.Context, r catalog.ResolveRequest) (catalog.Playable, error) {
	k, err := p.variant(ctx, r.Key)
	if err != nil {
		return catalog.Playable{}, err
	}
	eps, err := p.c.episodes(ctx, string(k.Source), k.ID)
	if err != nil {
		return catalog.Playable{}, p.fail(err, k.Source)
	}
	episode := episodeOf(k, r.Episode)
	if episode == "" {
		episode = eps.NextEpisode
	}
	if episode == "" && len(eps.Episodes) > 0 {
		episode = eps.Episodes[0].Label
	}
	var ep *episodeDTO
	for i := range eps.Episodes {
		if eps.Episodes[i].Label == episode {
			ep = &eps.Episodes[i]
		}
	}
	if ep == nil {
		return catalog.Playable{}, catalog.ErrNotFound
	}
	o, sel, err := p.episodeSources(ctx, k, episode, r.Selection)
	if err != nil {
		return catalog.Playable{}, err
	}
	src, ok := o.pick(sel)
	if !ok {
		return catalog.Playable{}, &catalog.Failure{Provider: catalog.ProviderTorrplayer, Source: k.Source, Code: catalog.FailRemoved}
	}
	if src.Kind == kindTorrent && src.Seeders == 0 {
		return catalog.Playable{}, &catalog.Failure{Provider: catalog.ProviderTorrplayer, Source: k.Source, Code: catalog.FailNoSeeders}
	}
	stage(r.OnStage, src.Kind == kindTorrent)
	m, err := p.c.resolve(ctx, resolveIn{Provider: string(k.Source), TitleID: k.ID, SourceID: src.ID, Episode: episode,
		Quality: src.Quality})
	if err != nil {
		return catalog.Playable{}, p.resolveFail(err, k.Source, src.Kind == kindTorrent)
	}
	if m.URL == "" {
		return catalog.Playable{}, &catalog.Failure{Provider: catalog.ProviderTorrplayer, Source: k.Source, Code: catalog.FailFailed}
	}
	if r.OnStage != nil {
		r.OnStage(catalog.StageStarting)
	}
	s := &catalog.Stream{URL: m.URL, InfoHash: m.InfoHash, FileIndex: m.Index}
	for _, sub := range m.Subtitles {
		s.Subtitles = append(s.Subtitles, catalog.Subtitle{URL: sub.URL, Language: sub.Lang, Label: sub.Label, Format: sub.Format})
	}
	s.Skips = skipsOf(m.Skips)
	if len(s.Skips) == 0 {
		// resolve carries the provider's own markers only; beyond them
		// torrplayer's skips endpoint knows the manual overrides and
		// AniSkip by MAL id, which the tv's player turns into its
		// «Пропустить опенинг/эндинг» pill. A miss — an older torrplayer, a
		// title nobody has ranges for — leaves the stream without ranges,
		// never without playback.
		hint := ep.Duration
		if hint == 0 && ep.DurationSec > 0 {
			hint = int(ep.DurationSec)
		}
		if out, err := p.c.skips(ctx, string(k.Source), k.ID, episode, hint); err == nil {
			s.Skips = skipsOf(out.Skips)
		}
	}
	if ep.Duration > 0 {
		s.DurationMS = int64(ep.Duration) * 1000
	} else if ep.DurationSec > 0 {
		s.DurationMS = int64(ep.DurationSec * 1000)
	}
	if r.From == catalog.FromResume && !ep.Watched {
		s.ResumeMS = int64(ep.ResumeSec * 1000)
	}
	return catalog.Playable{Kind: catalog.PlayStream, Stream: s, Selected: sel, Episode: episode}, nil
}

func stage(on func(catalog.Stage), torrent bool) {
	if on == nil {
		return
	}
	if torrent {
		on(catalog.StageConnectingPeers)
		return
	}
	on(catalog.StageReadingStream)
}

// skipsOf converts torrplayer's ranges (seconds) into the catalog's (ms)
func skipsOf(sk []skipDTO) []catalog.Skip {
	if len(sk) == 0 {
		return nil
	}
	out := make([]catalog.Skip, 0, len(sk))
	for _, s := range sk {
		out = append(out, catalog.Skip{Kind: catalog.SkipKind(s.Kind), StartMS: int64(s.Start * 1000), EndMS: int64(s.Stop * 1000)})
	}
	return out
}

// resolveFail: a source that answers nothing playable is unplayable, not a
// failure of torrplayer
func (p *Provider) resolveFail(err error, source catalog.SourceID, torrent bool) error {
	var he *httpError
	if errors.As(err, &he) {
		switch {
		case he.status == http.StatusNotFound:
			return &catalog.Failure{Provider: catalog.ProviderTorrplayer, Source: source, Code: catalog.FailRemoved}
		case he.status == http.StatusServiceUnavailable && torrent:
			return &catalog.Failure{Provider: catalog.ProviderTorrplayer, Source: source, Code: catalog.FailNoSeeders}
		}
	}
	return p.fail(err, source)
}

// pick is the source a selection plays
func (o offer) pick(sel catalog.Selection) (sourceDTO, bool) {
	if t := sel[catalog.GroupTorrent]; t != "" {
		for _, s := range o.torrents {
			if s.ID == t {
				return s, true
			}
		}
		return sourceDTO{}, false
	}
	list := o.by[sel[catalog.GroupVoiceover]]
	for _, s := range list {
		if strconv.Itoa(s.Quality) == sel[catalog.GroupQuality] {
			return s, true
		}
	}
	if len(list) > 0 {
		return list[0], true
	}
	return sourceDTO{}, false
}

func (p *Provider) Act(ctx context.Context, r catalog.ActRequest) ([]catalog.Action, error) {
	k, err := p.variant(ctx, r.Key)
	if err != nil {
		return nil, err
	}
	switch r.Action {
	case "watched":
		if k.Kind != catalog.KindEpisode {
			return nil, catalog.ErrRefused
		}
		if err := p.c.setWatched(ctx, string(k.Source), k.ID, watchedIn{Episode: k.Episode, Watched: r.On}); err != nil {
			return nil, p.fail(err, k.Source)
		}
		return []catalog.Action{watchedAction(r.On)}, nil
	case "subscribe":
		if r.On {
			err = p.c.subscribe(ctx, string(k.Source), k.ID)
		} else {
			err = p.c.unsubscribe(ctx, string(k.Source), k.ID)
		}
		if err != nil {
			return nil, p.fail(err, k.Source)
		}
	case "my_list":
		shiki := ""
		if r.Key.Source == myListSource {
			shiki = r.Key.ID
		} else if t, ok := p.title(k.Source, k.ID); ok && t.ShikimoriID > 0 {
			shiki = strconv.Itoa(t.ShikimoriID)
		} else if pr, err := p.c.progress(ctx, string(k.Source), k.ID); err == nil && pr.ShikimoriID > 0 {
			shiki = strconv.Itoa(pr.ShikimoriID)
		}
		if shiki == "" || r.Value == "" || !slices.Contains(myListStatuses, r.Value) {
			return nil, catalog.ErrRefused
		}
		if err := p.c.myListSet(ctx, shiki, r.Value); err != nil {
			return nil, p.fail(err, k.Source)
		}
	default:
		return nil, catalog.ErrNotFound
	}
	base := k
	base.Kind, base.Episode = catalog.KindSeries, ""
	it := p.variantItem(string(k.Source), k.ID, "", "")
	acts := p.actions(ctx, base, it)
	for i := range acts {
		if acts[i].ID == "my_list" && r.Action == "my_list" {
			acts[i].Enabled, acts[i].Value, acts[i].Toggled = true, r.Value, true
		}
	}
	return acts, nil
}

func (p *Provider) Panel(ctx context.Context, r catalog.PanelRequest) (catalog.PanelPage, error) {
	if r.Panel != "description" {
		return catalog.PanelPage{}, catalog.ErrNotFound
	}
	k, err := p.variant(ctx, r.Key)
	if err != nil {
		return catalog.PanelPage{}, err
	}
	t, ok := p.title(k.Source, k.ID)
	if !ok {
		return catalog.PanelPage{}, catalog.ErrNotFound
	}
	page := catalog.PanelPage{Text: t.Description}
	add := func(key, v string) {
		if v != "" {
			page.Facts = append(page.Facts, catalog.Fact{Key: key, Value: v})
		}
	}
	if t.Year > 0 {
		add("Год", strconv.Itoa(t.Year))
	}
	add("Формат", t.Kind)
	add("Жанры", strings.Join(t.Genres, ", "))
	add("Студии", strings.Join(t.Studios, ", "))
	if t.Score > 0 {
		add("Оценка", strconv.FormatFloat(t.Score, 'f', 1, 64))
	}
	add("Возраст", t.AgeRating)
	return page, nil
}

func (p *Provider) Report(ctx context.Context, r catalog.ProgressReport) error {
	k, err := p.variant(ctx, r.Key)
	if err != nil {
		return err
	}
	episode := episodeOf(k, r.Episode)
	if episode == "" {
		return catalog.ErrRefused
	}
	in := progressReportIn{Episode: episode, PositionSec: float64(r.PositionS), DurationSec: float64(r.DurationS)}
	if r.Watched {
		in.State = "ended"
	}
	if err := p.c.report(ctx, string(k.Source), k.ID, in); err != nil {
		return p.fail(err, k.Source)
	}
	return nil
}

// Recall answers torrplayer's sticky prefs of a variant as a selection.
func (p *Provider) Recall(ctx context.Context, title catalog.Key) (catalog.Selection, error) {
	if title.Source == "" || title.Source == myListSource {
		return catalog.Selection{}, nil
	}
	pr, err := p.c.progress(ctx, string(title.Source), title.ID)
	if err != nil {
		return catalog.Selection{}, p.fail(err, title.Source)
	}
	s := catalog.Selection{}
	if pr.Prefs.Translator != "" {
		s[catalog.GroupVoiceover] = pr.Prefs.Translator
	}
	if pr.Prefs.Quality > 0 {
		s[catalog.GroupQuality] = strconv.Itoa(pr.Prefs.Quality)
	}
	return s, nil
}

// Remember stores a voiceover and quality as torrplayer's sticky prefs; a
// torrent choice is not one torrplayer keeps.
func (p *Provider) Remember(ctx context.Context, title catalog.Key, s catalog.Selection) error {
	if title.Source == "" || title.Source == myListSource || s[catalog.GroupVoiceover] == "" {
		return nil
	}
	in := titlePrefsIn{Translator: s[catalog.GroupVoiceover]}
	if q, err := strconv.Atoi(s[catalog.GroupQuality]); err == nil {
		in.Quality = q
	}
	if in.Translator == "voice" || in.Translator == "sub" || in.Translator == "raw" {
		in.Kind, in.Translator = in.Translator, ""
	}
	if err := p.c.setPrefs(ctx, string(title.Source), title.ID, in); err != nil {
		return p.fail(err, title.Source)
	}
	return nil
}
