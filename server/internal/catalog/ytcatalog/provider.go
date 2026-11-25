// Package ytcatalog is YouTube Music, with YouTube videos, as a catalog
// provider over today's code: the music instance's search (providers.Music,
// with providers.Collections for albums, playlists and artists) and the
// player instance (yt-dlp) that resolves a video's audio, or its renditions
// for a screen. Two sources stream apart: music and video. Streams bound to
// the server's address go out behind the server's relay, bound to the device
// the call is for. music_search and music_play keep their own path through
// the music service; this provider serves the catalog's screens and tools.
package ytcatalog

import (
	"context"
	"errors"
	"strings"
	"sync"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytdlp"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytmusic"
	"git.shino.boo/devilreef/cadence/server/internal/streams"
)

const (
	SourceMusic catalog.SourceID = "music"
	SourceVideo catalog.SourceID = "video"

	searchLimit = 20
	songsLimit  = 50
)

// Screen is a player that resolves a video for a screen and searches videos
// (ytdlp.Player).
type Screen interface {
	ResolveScreen(ctx context.Context, videoID string, p catalog.DeviceProfile) (ytdlp.Screen, error)
	SearchVideos(ctx context.Context, query string, limit int) ([]ytdlp.Video, error)
}

// Relay puts upstreams behind the server's relay for a device and answers
// their urls in order; nil hands the upstream urls out themselves.
type Relay func(ctx context.Context, deviceID string, ups []streams.Upstream) ([]string, error)

// Config are the provider's parts.
type Config struct {
	Music  providers.Music
	Player providers.Player
	Relay  Relay
	// Web reads YouTube itself: the video source's search with its filters,
	// channels, playlists and a video's counts; nil leaves the video source
	// to the player's search alone
	Web Web
}

// Provider is the ytmusic catalog provider.
type Provider struct {
	catalog.Unsupported
	c Config

	mu    sync.Mutex
	known map[string]catalog.Item
}

var _ catalog.Provider = (*Provider)(nil)

// New is a provider over c; Music is required.
func New(c Config) *Provider { return &Provider{c: c, known: map[string]catalog.Item{}} }

func (p *Provider) ID() catalog.ProviderID { return catalog.ProviderYouTubeMusic }

func (p *Provider) Capabilities() catalog.Capabilities {
	caps := catalog.Capabilities{catalog.CapSearch, catalog.CapItem}
	if p.c.Web != nil {
		caps = append(caps, catalog.CapPanel)
	}
	if p.c.Player != nil {
		caps = append(caps, catalog.CapResolve)
	}
	return caps
}

func (p *Provider) screen() (Screen, bool) {
	s, ok := p.c.Player.(Screen)
	return s, ok
}

func (p *Provider) Sources(context.Context) ([]catalog.SourceInfo, error) {
	out := []catalog.SourceInfo{{ID: SourceMusic, Name: "YouTube Music", Enabled: true}}
	if _, ok := p.screen(); ok || p.c.Web != nil {
		out = append(out, catalog.SourceInfo{ID: SourceVideo, Name: "YouTube", Enabled: true})
	}
	return out, nil
}

func (p *Provider) remember(it catalog.Item) catalog.Item {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.known) > 4096 {
		p.known = map[string]catalog.Item{}
	}
	p.known[it.Key.String()] = it
	return it
}

func (p *Provider) knownItem(k catalog.Key) (catalog.Item, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	it, ok := p.known[k.String()]
	return it, ok
}

func key(src catalog.SourceID, kind catalog.Kind, id string) catalog.Key {
	return catalog.Key{Provider: catalog.ProviderYouTubeMusic, Source: src, Kind: kind, ID: id}
}

func trackItem(t providers.Track) (catalog.Item, bool) {
	id := t.VideoID
	if id == "" && t.Source == providers.SourceYouTube {
		id = t.ID
	}
	if !ytmusic.ValidVideoID(id) {
		return catalog.Item{}, false
	}
	k := key(SourceMusic, catalog.KindTrack, id)
	return catalog.Item{Key: k, Provider: k.Provider, Source: k.Source, Kind: k.Kind, Title: t.Title, Subtitle: t.Artist,
		DurationS: t.DurationS, Playable: true, Artwork: catalog.Artwork{URL: t.ArtworkURL, Aspect: "square"},
		Music: &catalog.MusicInfo{Artist: t.Artist, Album: t.Album}, Stats: statsOf(t.Stats)}, true
}

var collectionKinds = map[string]catalog.Kind{
	providers.KindAlbum: catalog.KindAlbum, providers.KindPlaylist: catalog.KindPlaylist, providers.KindArtist: catalog.KindArtist,
}

func collectionItem(c providers.Collection) catalog.Item {
	k := key(SourceMusic, collectionKinds[c.Kind], c.ID+"\x1f"+c.Radio)
	return catalog.Item{Key: k, Provider: k.Provider, Source: k.Source, Kind: k.Kind, Title: c.Title, Subtitle: c.Owner,
		HasChildren: true, Artwork: catalog.Artwork{URL: c.ArtworkURL, Aspect: "square"},
		Music: &catalog.MusicInfo{Owner: c.Owner, Tracks: c.Tracks}, Stats: statsOf(c.Stats)}
}

func videoItem(v ytdlp.Video) catalog.Item {
	k := key(SourceVideo, catalog.KindVideo, v.ID)
	it := catalog.Item{Key: k, Provider: k.Provider, Source: k.Source, Kind: k.Kind, Title: v.Title, Subtitle: v.Channel,
		DurationS: v.DurationS, Playable: true, Artwork: catalog.Artwork{URL: v.ThumbnailURL(), Aspect: "wide"}}
	if v.Live {
		it.Kind, it.Key.Kind = catalog.KindLive, catalog.KindLive
		it.Badges = []catalog.Badge{{Kind: catalog.BadgeLive}}
	}
	return it
}

func failure(src catalog.SourceID, err error) *catalog.Failure {
	code := catalog.FailFailed
	if errors.Is(err, context.DeadlineExceeded) {
		code = catalog.FailTimeout
	}
	return &catalog.Failure{Provider: catalog.ProviderYouTubeMusic, Source: src, Code: code}
}

func wants(kinds []catalog.Kind, k ...catalog.Kind) bool {
	if len(kinds) == 0 {
		return true
	}
	for _, x := range k {
		for _, y := range kinds {
			if x == y {
				return true
			}
		}
	}
	return false
}

func (p *Provider) Search(ctx context.Context, q catalog.SearchQuery, emit func(catalog.SearchBatch) error) error {
	srcs, _ := p.Sources(ctx)
	limit := q.Limit
	if limit <= 0 || limit > searchLimit {
		limit = searchLimit
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := make(chan catalog.SearchBatch, len(srcs))
	var wg sync.WaitGroup
	for _, s := range srcs {
		if len(q.Sources) > 0 && !containsSource(q.Sources, s.ID) {
			continue
		}
		wg.Go(func() { out <- p.searchSource(ctx, s.ID, q, limit) })
	}
	go func() { wg.Wait(); close(out) }()
	for b := range out {
		if err := emit(b); err != nil {
			cancel()
			for range out { //nolint:revive // drain: every source's goroutine ends
			}
			return err
		}
	}
	return ctx.Err()
}

func containsSource(list []catalog.SourceID, s catalog.SourceID) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (p *Provider) searchSource(ctx context.Context, src catalog.SourceID, q catalog.SearchQuery, limit int) catalog.SearchBatch {
	b := catalog.SearchBatch{Provider: catalog.ProviderYouTubeMusic, Source: src, Items: []catalog.Item{}}
	query := strings.TrimSpace(q.Query)
	if src == SourceVideo && p.c.Web != nil && (query != "" || q.Cursor != "") {
		return p.searchWeb(ctx, q, limit)
	}
	for k := range q.Filters {
		b.Unenforced = append(b.Unenforced, k)
	}
	if q.Sort != "" && q.Sort != catalog.SortRelevance {
		b.Unenforced = append(b.Unenforced, "sort")
	}
	if query == "" {
		return b
	}
	if src == SourceVideo {
		if !wants(q.Kinds, catalog.KindVideo, catalog.KindLive) {
			return b
		}
		sc, _ := p.screen()
		vs, err := sc.SearchVideos(ctx, query, limit)
		if err != nil {
			b.Items, b.Failure = nil, failure(src, err)
			return b
		}
		for _, v := range vs {
			b.Items = append(b.Items, p.remember(videoItem(v)))
		}
		return b
	}
	if wants(q.Kinds, catalog.KindTrack) {
		ts, err := p.c.Music.SearchMusic(ctx, query, limit)
		if err != nil {
			b.Items, b.Failure = nil, failure(src, err)
			return b
		}
		for _, t := range ts {
			if it, ok := trackItem(t); ok {
				b.Items = append(b.Items, p.remember(it))
			}
		}
	}
	if cols, ok := p.c.Music.(providers.Collections); ok {
		for _, kind := range []string{providers.KindAlbum, providers.KindPlaylist, providers.KindArtist} {
			if !wants(q.Kinds, collectionKinds[kind]) {
				continue
			}
			cs, err := cols.SearchCollections(ctx, kind, query, min(limit, 6))
			if err != nil {
				continue
			}
			for _, c := range cs {
				b.Items = append(b.Items, p.remember(collectionItem(c)))
			}
		}
	}
	return b
}

func (p *Provider) Item(ctx context.Context, r catalog.ItemRequest) (catalog.ItemDetail, error) {
	if r.Key.Source == SourceVideo && p.c.Web != nil {
		switch r.Key.Kind {
		case catalog.KindChannel:
			return p.channelDetail(ctx, r)
		case catalog.KindPlaylist:
			return p.playlistDetail(ctx, r)
		}
	}
	switch r.Key.Kind {
	case catalog.KindAlbum, catalog.KindPlaylist, catalog.KindArtist:
	default:
		return p.videoDetail(ctx, r)
	}
	cols, ok := p.c.Music.(providers.Collections)
	id, radio, found := strings.Cut(r.Key.ID, "\x1f")
	if !ok || !found || id == "" {
		return catalog.ItemDetail{}, catalog.ErrNotFound
	}
	kind := ""
	for k, v := range collectionKinds {
		if v == r.Key.Kind {
			kind = k
		}
	}
	col := providers.Collection{Source: providers.SourceYouTube, Kind: kind, ID: id, Radio: radio}
	songs, err := cols.CollectionSongs(ctx, col, songsLimit)
	switch {
	case errors.Is(err, providers.ErrMusicRefused):
		return catalog.ItemDetail{}, catalog.ErrRefused
	case err != nil:
		return catalog.ItemDetail{}, failure("", err)
	}
	it, ok := p.knownItem(r.Key)
	if !ok {
		it = catalog.Item{Key: r.Key, Provider: r.Key.Provider, Source: r.Key.Source, Kind: r.Key.Kind, HasChildren: true}
	}
	d := catalog.ItemDetail{Item: it}
	for _, s := range songs {
		if t, ok := trackItem(s); ok {
			d.Children = append(d.Children, p.remember(t))
		}
	}
	return d, nil
}

func (p *Provider) Resolve(ctx context.Context, r catalog.ResolveRequest) (catalog.Playable, error) {
	if p.c.Player == nil {
		return catalog.Playable{}, catalog.ErrUnsupported
	}
	id := r.Key.ID
	if !ytmusic.ValidVideoID(id) || (r.Key.Kind != catalog.KindTrack && r.Key.Kind != catalog.KindVideo && r.Key.Kind != catalog.KindLive) {
		return catalog.Playable{}, catalog.ErrNotFound
	}
	dev := catalog.DeviceFrom(ctx)
	if sc, ok := p.screen(); ok && r.Profile.Screen && r.Key.Kind != catalog.KindTrack {
		if r.OnStage != nil && !ytdlp.HasProgress(ctx) {
			r.OnStage(catalog.StageReadingStream)
		}
		s, err := sc.ResolveScreen(ctx, id, r.Profile)
		if err != nil {
			return catalog.Playable{}, playerFail(err)
		}
		st, err := p.relayScreen(ctx, dev, s)
		if err != nil {
			return catalog.Playable{}, err
		}
		return catalog.Playable{Kind: catalog.PlayStream, Stream: st, Selected: catalog.Selection{}}, nil
	}
	if r.OnStage != nil {
		r.OnStage(catalog.StageReadingStream)
	}
	s, err := p.c.Player.Resolve(ctx, id)
	if err != nil {
		return catalog.Playable{}, playerFail(err)
	}
	u := s.URL
	if p.c.Relay != nil {
		urls, err := p.c.Relay(ctx, dev, []streams.Upstream{{URL: s.URL}})
		if err != nil || len(urls) != 1 {
			return catalog.Playable{}, &catalog.Failure{Provider: catalog.ProviderYouTubeMusic, Code: catalog.FailFailed}
		}
		u = urls[0]
	}
	return catalog.Playable{Kind: catalog.PlayStream, Selected: catalog.Selection{},
		Stream: &catalog.Stream{URL: u, DurationMS: int64(s.DurationS) * 1000}}, nil
}

// relayScreen puts a screen's audio and renditions behind one relay token
func (p *Provider) relayScreen(ctx context.Context, dev string, s ytdlp.Screen) (*catalog.Stream, error) {
	st := s.Stream
	if p.c.Relay == nil {
		return &st, nil
	}
	var ups []streams.Upstream
	if st.AudioURL != "" {
		ups = append(ups, streams.Upstream{URL: st.AudioURL})
	}
	for _, r := range st.Renditions {
		ups = append(ups, streams.Upstream{URL: r.URL, HLS: s.HLS})
	}
	if len(ups) == 0 {
		ups = append(ups, streams.Upstream{URL: st.URL, HLS: s.HLS})
	}
	urls, err := p.c.Relay(ctx, dev, ups)
	if err != nil || len(urls) != len(ups) {
		return nil, &catalog.Failure{Provider: catalog.ProviderYouTubeMusic, Code: catalog.FailFailed}
	}
	i := 0
	if st.AudioURL != "" {
		st.AudioURL = urls[0]
		i = 1
	}
	rs := make([]catalog.Rendition, len(st.Renditions))
	for j, r := range st.Renditions {
		r.URL = urls[i+j]
		if r.AudioURL != "" {
			r.AudioURL = st.AudioURL
		}
		rs[j] = r
	}
	st.Renditions = rs
	if len(rs) > 0 {
		st.URL = rs[0].URL
	} else {
		st.URL = urls[i]
	}
	return &st, nil
}

func playerFail(err error) error {
	switch {
	case errors.Is(err, providers.ErrSignInRequired):
		return catalog.ErrSignInRequired
	case errors.Is(err, providers.ErrUnplayable):
		return &catalog.Failure{Provider: catalog.ProviderYouTubeMusic, Code: catalog.FailRemoved}
	case errors.Is(err, context.DeadlineExceeded):
		return &catalog.Failure{Provider: catalog.ProviderYouTubeMusic, Code: catalog.FailTimeout}
	}
	return &catalog.Failure{Provider: catalog.ProviderYouTubeMusic, Code: catalog.FailFailed}
}
