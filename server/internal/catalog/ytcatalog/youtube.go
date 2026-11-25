package ytcatalog

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytweb"
)

// Web reads YouTube itself (ytweb.Client): search with filters, channels and
// their tabs, playlists, a video's counts.
type Web interface {
	Search(ctx context.Context, o ytweb.SearchOptions) (ytweb.Page, error)
	More(ctx context.Context, next string) (ytweb.Page, error)
	ChannelTab(ctx context.Context, idOrHandle string, tab ytweb.Tab, sort ytweb.Sort) (ytweb.ChannelPage, error)
	ChannelSearch(ctx context.Context, idOrHandle, query string) (ytweb.ChannelPage, error)
	Playlist(ctx context.Context, id string) (ytweb.PlaylistPage, error)
	Video(ctx context.Context, id string) (ytweb.Video, error)
	Stats(ctx context.Context, ids []string) map[string]providers.Stats
}

const (
	// a re-sorted playlist reads this many pages (100 rows each) at most
	sortPages = 5
	// "latest" asks the exact time of at most this many rows tied on their relative one
	exactDates = 12
)

// channelTabs are a channel's lists, in the order its page shows them
var channelTabs = []catalog.Tab{
	{ID: string(ytweb.TabVideos), Label: "Videos"}, {ID: string(ytweb.TabShorts), Label: "Shorts"},
	{ID: string(ytweb.TabStreams), Label: "Live"}, {ID: string(ytweb.TabPlaylists), Label: "Playlists"},
}

// statsOf is a provider's counts as the catalog's, nil when it showed none
func statsOf(s providers.Stats) *catalog.Stats {
	if s == (providers.Stats{}) {
		return nil
	}
	return &catalog.Stats{Views: s.Views, Likes: s.Likes, Plays: s.Plays, Subscribers: s.Subscribers,
		MonthlyAudience: s.MonthlyAudience, VideoCount: s.VideoCount, PublishedAt: s.Published,
		PublishedApprox: s.PublishedApprox}
}

func channelKey(id string) catalog.Key { return key(SourceVideo, catalog.KindChannel, id) }

// webItem is a row YouTube listed as the catalog's item
func webItem(w ytweb.Item) catalog.Item {
	var it catalog.Item
	switch w.Kind {
	case ytweb.KindChannel:
		k := channelKey(w.ID)
		it = catalog.Item{Key: k, Title: w.Title, Subtitle: w.Handle, HasChildren: true,
			Artwork: catalog.Artwork{URL: w.Thumbnail, Aspect: "square"}}
	case ytweb.KindPlaylist:
		k := key(SourceVideo, catalog.KindPlaylist, w.ID)
		it = catalog.Item{Key: k, Title: w.Title, Subtitle: w.Channel, HasChildren: true,
			Artwork: catalog.Artwork{URL: w.Thumbnail, Aspect: "wide"}}
	default:
		kind := catalog.KindVideo
		var badges []catalog.Badge
		if w.Kind == ytweb.KindLive {
			kind = catalog.KindLive
			badges = []catalog.Badge{{Kind: catalog.BadgeLive}}
		}
		if w.Upcoming {
			badges = append(badges, catalog.Badge{Kind: catalog.BadgePremiere})
		}
		aspect := "wide"
		if w.Kind == ytweb.KindShort {
			aspect = "tall"
		}
		k := key(SourceVideo, kind, w.ID)
		it = catalog.Item{Key: k, Title: w.Title, Subtitle: w.Channel, DurationS: w.DurationS, Badges: badges,
			Playable: !w.Upcoming, Artwork: catalog.Artwork{URL: cmp.Or(w.Thumbnail, ytweb.VideoThumbnail(w.ID)), Aspect: aspect}}
	}
	it.Provider, it.Source, it.Kind = it.Key.Provider, it.Key.Source, it.Key.Kind
	it.Stats = statsOf(w.Stats)
	if w.Kind != ytweb.KindChannel && (w.ChannelID != "" || w.Channel != "") {
		o := catalog.Owner{Title: w.Channel}
		if w.ChannelID != "" {
			o.Key = channelKey(w.ChannelID)
		}
		it.Owner = &o
	}
	return it
}

// searchWeb is the video source's search on YouTube itself: one type when
// the query asks for one, YouTube's mix otherwise
func (p *Provider) searchWeb(ctx context.Context, q catalog.SearchQuery, limit int) catalog.SearchBatch {
	b := catalog.SearchBatch{Provider: catalog.ProviderYouTubeMusic, Source: SourceVideo, Items: []catalog.Item{}}
	o := ytweb.SearchOptions{Query: q.Query}
	switch q.Sort {
	case "", catalog.SortRelevance:
	case catalog.SortPopular, catalog.SortViews:
		o.Sort = "popular"
	default:
		b.Unenforced = append(b.Unenforced, "sort")
	}
	for k, v := range q.Filters {
		switch {
		case k == catalog.FilterUploaded && slices.Contains([]string{"today", "week", "month", "year"}, v):
			o.Uploaded = v
		case k == catalog.FilterDuration && slices.Contains([]string{"short", "medium", "long"}, v):
			o.Duration = v
		case k == catalog.FilterLive && v == "true":
			o.Live = true
		default:
			b.Unenforced = append(b.Unenforced, k)
		}
	}
	slices.Sort(b.Unenforced)
	if t, ok := oneWebKind(q.Kinds); ok {
		o.Type = t
		if t == "live" {
			o.Type, o.Live = "video", true
		}
	}
	var page ytweb.Page
	var err error
	if q.Cursor != "" {
		page, err = p.c.Web.More(ctx, q.Cursor)
	} else {
		page, err = p.c.Web.Search(ctx, o)
	}
	if err != nil {
		return p.searchFallback(ctx, q, limit, err)
	}
	for _, w := range page.Items {
		it := webItem(w)
		if wants(q.Kinds, it.Kind) {
			b.Items = append(b.Items, p.remember(it))
		}
		if len(b.Items) == limit {
			break
		}
	}
	b.Next = page.Next
	return b
}

// oneWebKind is the YouTube search type of a query asking for one kind
func oneWebKind(kinds []catalog.Kind) (string, bool) {
	var types []string
	for _, k := range kinds {
		switch k {
		case catalog.KindVideo:
			types = append(types, "video")
		case catalog.KindLive:
			types = append(types, "live")
		case catalog.KindChannel:
			types = append(types, "channel")
		case catalog.KindPlaylist:
			types = append(types, "playlist")
		}
	}
	if len(types) != 1 {
		return "", false
	}
	return types[0], true
}

// searchFallback searches videos through yt-dlp when YouTube's own API
// fails: no channels, playlists, sorts or filters there
func (p *Provider) searchFallback(ctx context.Context, q catalog.SearchQuery, limit int, webErr error) catalog.SearchBatch {
	b := catalog.SearchBatch{Provider: catalog.ProviderYouTubeMusic, Source: SourceVideo, Items: []catalog.Item{}}
	sc, ok := p.screen()
	if !ok || q.Cursor != "" || !wants(q.Kinds, catalog.KindVideo, catalog.KindLive) {
		b.Items, b.Failure = nil, failure(SourceVideo, webErr)
		return b
	}
	vs, err := sc.SearchVideos(ctx, strings.TrimSpace(q.Query), limit)
	if err != nil {
		b.Items, b.Failure = nil, failure(SourceVideo, err)
		return b
	}
	for _, v := range vs {
		b.Items = append(b.Items, p.remember(videoItem(v)))
	}
	if q.Sort != "" && q.Sort != catalog.SortRelevance {
		b.Unenforced = append(b.Unenforced, "sort")
	}
	for k := range q.Filters {
		b.Unenforced = append(b.Unenforced, k)
	}
	slices.Sort(b.Unenforced)
	return b
}

func webSort(s catalog.Sort) ytweb.Sort {
	switch s {
	case catalog.SortLatest:
		return ytweb.SortLatest
	case catalog.SortPopular, catalog.SortViews:
		return ytweb.SortPopular
	case catalog.SortOldest:
		return ytweb.SortOldest
	case catalog.SortUpdated:
		return ytweb.SortUpdated
	}
	return ""
}

func catalogSort(s ytweb.Sort) catalog.Sort {
	switch s {
	case ytweb.SortLatest:
		return catalog.SortLatest
	case ytweb.SortPopular:
		return catalog.SortPopular
	case ytweb.SortOldest:
		return catalog.SortOldest
	case ytweb.SortUpdated:
		return catalog.SortUpdated
	}
	return ""
}

// channelDetail is a channel's tab (its videos, newest first, unless asked
// otherwise), or what its own search finds for r.Query
func (p *Provider) channelDetail(ctx context.Context, r catalog.ItemRequest) (catalog.ItemDetail, error) {
	tab := ytweb.Tab(cmp.Or(r.Tab, string(ytweb.TabVideos)))
	if !slices.ContainsFunc(channelTabs, func(t catalog.Tab) bool { return t.ID == string(tab) }) {
		return catalog.ItemDetail{}, catalog.ErrNotFound
	}
	var cp ytweb.ChannelPage
	var err error
	switch {
	case r.Cursor != "":
		var page ytweb.Page
		page, err = p.c.Web.More(ctx, r.Cursor)
		cp.Page = page
		cp.Channel.ID = r.Key.ID
	case strings.TrimSpace(r.Query) != "":
		cp, err = p.c.Web.ChannelSearch(ctx, r.Key.ID, r.Query)
	default:
		cp, err = p.c.Web.ChannelTab(ctx, r.Key.ID, tab, webSort(r.Sort))
	}
	if err != nil {
		return catalog.ItemDetail{}, webFail(err)
	}
	it, ok := p.knownItem(r.Key)
	if !ok || cp.Channel.Title != "" {
		it = webItem(ytweb.Item{Kind: ytweb.KindChannel, ID: r.Key.ID, Title: cp.Channel.Title, Handle: cp.Channel.Handle,
			Thumbnail: cp.Channel.Thumbnail, Stats: cp.Channel.Stats})
		it = p.remember(it)
	}
	d := catalog.ItemDetail{Item: it, Next: cp.Page.Next, Sort: catalogSort(cp.Sort)}
	for _, t := range channelTabs {
		t.Selected = t.ID == string(tab) && r.Query == ""
		d.Tabs = append(d.Tabs, t)
	}
	for _, s := range cp.Sorts {
		d.Sorts = append(d.Sorts, catalogSort(s))
	}
	for _, w := range limitRows(cp.Page.Items, r.Limit) {
		d.Children = append(d.Children, p.remember(webItem(w)))
	}
	return d, nil
}

// playlistDetail is a YouTube playlist's rows: in its own order a page at a
// time, or re-sorted (latest, oldest, views) over its first pages, its
// titles filtered by r.Query
func (p *Provider) playlistDetail(ctx context.Context, r catalog.ItemRequest) (catalog.ItemDetail, error) {
	if r.Cursor != "" {
		page, err := p.c.Web.More(ctx, r.Cursor)
		if err != nil {
			return catalog.ItemDetail{}, webFail(err)
		}
		it, ok := p.knownItem(r.Key)
		if !ok {
			it = catalog.Item{Key: r.Key, Provider: r.Key.Provider, Source: r.Key.Source, Kind: r.Key.Kind, HasChildren: true}
		}
		d := catalog.ItemDetail{Item: it, Next: page.Next}
		for _, w := range filterRows(limitRows(page.Items, r.Limit), r.Query) {
			d.Children = append(d.Children, p.remember(webItem(w)))
		}
		return d, nil
	}
	pp, err := p.c.Web.Playlist(ctx, r.Key.ID)
	if err != nil {
		return catalog.ItemDetail{}, webFail(err)
	}
	pl := pp.Playlist
	it := p.remember(webItem(ytweb.Item{Kind: ytweb.KindPlaylist, ID: r.Key.ID, Title: pl.Title, Channel: pl.Channel,
		ChannelID: pl.ChannelID, Stats: pl.Stats, Thumbnail: p.thumbOf(r.Key)}))
	d := catalog.ItemDetail{Item: it, Sorts: []catalog.Sort{catalog.SortLatest, catalog.SortOldest, catalog.SortViews}}
	rows := pp.Page.Items
	next := pp.Page.Next
	if r.Sort == catalog.SortLatest || r.Sort == catalog.SortOldest || r.Sort == catalog.SortViews {
		for pages := 1; next != "" && pages < sortPages; pages++ {
			more, err := p.c.Web.More(ctx, next)
			if err != nil {
				break
			}
			rows, next = append(rows, more.Items...), more.Next
		}
		d.Partial = next != ""
		rows = p.sortRows(ctx, rows, r.Sort)
		d.Sort, next = r.Sort, ""
	}
	d.Next = next
	for _, w := range limitRows(filterRows(rows, r.Query), r.Limit) {
		d.Children = append(d.Children, p.remember(webItem(w)))
	}
	return d, nil
}

func (p *Provider) thumbOf(k catalog.Key) string {
	if it, ok := p.knownItem(k); ok {
		return it.Artwork.URL
	}
	return ""
}

// sortRows orders a playlist's rows; latest settles the rows tied on the
// newest relative time ("2 weeks ago") by their exact time
func (p *Provider) sortRows(ctx context.Context, rows []ytweb.Item, s catalog.Sort) []ytweb.Item {
	rows = slices.Clone(rows)
	switch s {
	case catalog.SortViews:
		slices.SortStableFunc(rows, func(a, b ytweb.Item) int { return cmp.Compare(b.Stats.Views, a.Stats.Views) })
		return rows
	case catalog.SortOldest:
		slices.SortStableFunc(rows, func(a, b ytweb.Item) int { return a.Stats.Published.Compare(b.Stats.Published) })
		return rows
	}
	slices.SortStableFunc(rows, func(a, b ytweb.Item) int { return b.Stats.Published.Compare(a.Stats.Published) })
	if len(rows) == 0 {
		return rows
	}
	var tied []string
	for _, w := range rows {
		if !w.Stats.Published.Equal(rows[0].Stats.Published) || len(tied) == exactDates {
			break
		}
		tied = append(tied, w.ID)
	}
	if len(tied) < 2 {
		return rows
	}
	exact := p.c.Web.Stats(ctx, tied)
	head := rows[:len(tied)]
	for i, w := range head {
		if st, ok := exact[w.ID]; ok && !st.Published.IsZero() {
			head[i].Stats.Published, head[i].Stats.PublishedApprox = st.Published, false
			head[i].Stats.Likes = st.Likes
			head[i].Stats.Views = max(head[i].Stats.Views, st.Views)
		}
	}
	slices.SortStableFunc(head, func(a, b ytweb.Item) int { return b.Stats.Published.Compare(a.Stats.Published) })
	return rows
}

func limitRows(rows []ytweb.Item, n int) []ytweb.Item {
	if n > 0 && len(rows) > n {
		return rows[:n]
	}
	return rows
}

func filterRows(rows []ytweb.Item, q string) []ytweb.Item {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return rows
	}
	var out []ytweb.Item
	for _, w := range rows {
		if strings.Contains(strings.ToLower(w.Title), q) {
			out = append(out, w)
		}
	}
	return out
}

// videoDetail is a video's own counts (likes, exact views and publish time)
// over what the listing showed; the listing's alone when YouTube does not answer
func (p *Provider) videoDetail(ctx context.Context, r catalog.ItemRequest) (catalog.ItemDetail, error) {
	known, ok := p.knownItem(r.Key)
	if p.c.Web == nil || !ytweb.ValidVideoID(r.Key.ID) {
		if !ok {
			return catalog.ItemDetail{}, catalog.ErrNotFound
		}
		return catalog.ItemDetail{Item: known}, nil
	}
	v, err := p.c.Web.Video(ctx, r.Key.ID)
	switch {
	case err != nil && ok:
		return catalog.ItemDetail{Item: known}, nil
	case err != nil:
		return catalog.ItemDetail{}, webFail(err)
	}
	it := known
	if !ok {
		it = webItem(v.Item)
		it.Key = r.Key
		it.Provider, it.Source, it.Kind = r.Key.Provider, r.Key.Source, r.Key.Kind
		if r.Key.Kind == catalog.KindTrack {
			it.Artwork.Aspect = "square"
			it.Music = &catalog.MusicInfo{Artist: v.Channel}
		}
	}
	it.Stats = statsOf(v.Stats)
	if it.Owner == nil && v.ChannelID != "" {
		it.Owner = &catalog.Owner{Key: channelKey(v.ChannelID), Title: v.Channel}
	}
	return catalog.ItemDetail{Item: p.remember(it), Panels: descriptionPanel(v)}, nil
}

func descriptionPanel(v ytweb.Video) []catalog.Panel {
	if v.Description == "" {
		return nil
	}
	return []catalog.Panel{{ID: "description", Kind: catalog.PanelDescription, Title: "Description"}}
}

// webFail is a ytweb failure as the catalog's
func webFail(err error) error {
	var e *ytweb.Error
	if !errors.As(err, &e) {
		return &catalog.Failure{Provider: catalog.ProviderYouTubeMusic, Code: catalog.FailFailed}
	}
	switch e.Code {
	case ytweb.ErrNotFound.Code, ytweb.ErrBadCursor.Code:
		return catalog.ErrNotFound
	case ytweb.ErrTimeout.Code:
		return &catalog.Failure{Provider: catalog.ProviderYouTubeMusic, Code: catalog.FailTimeout}
	case ytweb.ErrRateLimited.Code:
		return &catalog.Failure{Provider: catalog.ProviderYouTubeMusic, Code: catalog.FailRateLimited, HTTPStatus: e.Status}
	}
	return &catalog.Failure{Provider: catalog.ProviderYouTubeMusic, Code: catalog.FailFailed, HTTPStatus: e.Status}
}

// Panel reads a video's description
func (p *Provider) Panel(ctx context.Context, r catalog.PanelRequest) (catalog.PanelPage, error) {
	if p.c.Web == nil {
		return catalog.PanelPage{}, catalog.ErrUnsupported
	}
	if r.Panel != "description" || !ytweb.ValidVideoID(r.Key.ID) {
		return catalog.PanelPage{}, catalog.ErrNotFound
	}
	v, err := p.c.Web.Video(ctx, r.Key.ID)
	if err != nil {
		return catalog.PanelPage{}, webFail(err)
	}
	return catalog.PanelPage{Text: v.Description}, nil
}
