package torrserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
)

// sources: the owner's library, and TorrServer's two searches
const (
	SourceLibrary catalog.SourceID = "library"
	SourceRutor   catalog.SourceID = "rutor"
	SourceTorznab catalog.SourceID = "torznab"
)

const (
	// infoWait bounds the wait for an added torrent's metadata: TorrServer's
	// own is 60 s plus its disconnect timeout
	infoWait     = 95 * time.Second
	pollInterval = time.Second
	searchLimit  = 50
	// subsVersion is the first MatriX with /subs
	subsVersion = 148
)

var hashRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

var videoExt = []string{".mkv", ".mp4", ".avi", ".mov", ".m4v", ".webm", ".ts", ".m2ts", ".mpg", ".mpeg", ".wmv", ".flv"}
var audioExt = []string{".mp3", ".flac", ".m4a", ".ogg", ".opus", ".wav", ".aac"}
var subExt = []string{".srt", ".ass", ".ssa", ".vtt"}

// Provider is the TorrServer catalog provider.
type Provider struct {
	catalog.Unsupported
	c *Client
	// Poll is the metadata poll's interval; zero is a second
	Poll time.Duration

	mu  sync.Mutex
	ver int
}

var _ catalog.Provider = (*Provider)(nil)

// New is a provider over c.
func New(c *Client) *Provider { return &Provider{c: c} }

func (p *Provider) ID() catalog.ProviderID { return catalog.ProviderTorrServer }

func (p *Provider) Capabilities() catalog.Capabilities {
	return catalog.Capabilities{catalog.CapSearch, catalog.CapBrowse, catalog.CapItem, catalog.CapOptions,
		catalog.CapResolve, catalog.CapReport}
}

// OwnsArtwork: a poster on TorrServer's own host is fetched on the LAN.
func (p *Provider) OwnsArtwork(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Host == p.c.base.Host || u.Host == p.c.public.Host)
}

func (p *Provider) Sources(context.Context) ([]catalog.SourceInfo, error) {
	return []catalog.SourceInfo{
		{ID: SourceLibrary, Name: "Библиотека", Enabled: true},
		{ID: SourceRutor, Name: "rutor", Enabled: true, Torrents: true},
		{ID: SourceTorznab, Name: "Torznab", Enabled: true, Torrents: true},
	}, nil
}

func (p *Provider) fail(err error, source catalog.SourceID) error {
	f := &catalog.Failure{Provider: catalog.ProviderTorrServer, Source: source, Code: catalog.FailFailed}
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
		case http.StatusUnauthorized, http.StatusForbidden:
			f.Code = catalog.FailAuth
		case http.StatusServiceUnavailable:
			f.Code = catalog.FailUnavailable
		}
	case errors.As(err, &ne):
		f.Code = catalog.FailUnavailable
	}
	return f
}

// version is MatriX's number, read once
func (p *Provider) version(ctx context.Context) int {
	p.mu.Lock()
	v := p.ver
	p.mu.Unlock()
	if v > 0 {
		return v
	}
	n, err := p.c.version(ctx)
	if err != nil || n == 0 {
		return 0
	}
	p.mu.Lock()
	p.ver = n
	p.mu.Unlock()
	return n
}

func torrentKey(source catalog.SourceID, id string) catalog.Key {
	return catalog.Key{Provider: catalog.ProviderTorrServer, Source: source, Kind: catalog.KindTorrent, ID: id}
}

func fileKey(hash string, id int) catalog.Key {
	return catalog.Key{Provider: catalog.ProviderTorrServer, Source: SourceLibrary, Kind: catalog.KindFile,
		ID: hash + "/" + strconv.Itoa(id)}
}

func (p *Provider) libraryItem(t torrentStatus) catalog.Item {
	k := torrentKey(SourceLibrary, t.Hash)
	title := t.Title
	if title == "" {
		title = t.Name
	}
	it := catalog.Item{Key: k, Provider: k.Provider, Source: k.Source, Kind: k.Kind, Title: title, HasChildren: true,
		Playable: true, Artwork: catalog.Artwork{URL: t.Poster, Aspect: "poster"},
		Torrent: &catalog.TorrentInfo{SizeBytes: t.TorrentSize, Files: len(t.FileStats)}}
	if t.Name != "" && t.Name != title {
		it.Subtitle = t.Name
	}
	return it
}

func (p *Provider) Search(ctx context.Context, q catalog.SearchQuery, emit func(catalog.SearchBatch) error) error {
	all, _ := p.Sources(ctx)
	var sources []catalog.SourceID
	for _, s := range all {
		if len(q.Sources) == 0 || slices.Contains(q.Sources, s.ID) {
			sources = append(sources, s.ID)
		}
	}
	torrents := len(q.Kinds) == 0 || slices.ContainsFunc(q.Kinds, func(k catalog.Kind) bool {
		return k == catalog.KindTorrent || k == catalog.KindMovie || k == catalog.KindSeries
	})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	batches := make(chan catalog.SearchBatch, len(sources))
	var wg sync.WaitGroup
	for _, src := range sources {
		wg.Go(func() {
			b := catalog.SearchBatch{Provider: catalog.ProviderTorrServer, Source: src, Items: []catalog.Item{}}
			if torrents && strings.TrimSpace(q.Query) != "" {
				p.searchSource(ctx, q, &b)
			}
			batches <- b
		})
	}
	go func() { wg.Wait(); close(batches) }()
	for b := range batches {
		if len(q.Filters) > 0 {
			for k := range q.Filters {
				b.Unenforced = append(b.Unenforced, k)
			}
			slices.Sort(b.Unenforced)
		}
		if err := emit(b); err != nil {
			cancel()
			for range batches { //nolint:revive // drain so every source's goroutine ends
			}
			return err
		}
	}
	return ctx.Err()
}

func (p *Provider) searchSource(ctx context.Context, q catalog.SearchQuery, b *catalog.SearchBatch) {
	limit := q.Limit
	if limit <= 0 || limit > searchLimit {
		limit = searchLimit
	}
	failed := func(err error) {
		var f *catalog.Failure
		code := catalog.FailFailed
		if errors.As(p.fail(err, b.Source), &f) {
			code = f.Code
		}
		b.Items, b.Failure = nil, &catalog.Failure{Provider: catalog.ProviderTorrServer, Source: b.Source, Code: code}
	}
	if b.Source == SourceLibrary {
		list, err := p.c.list(ctx)
		if err != nil {
			failed(err)
			return
		}
		needle := strings.ToLower(q.Query)
		for _, t := range list {
			if strings.Contains(strings.ToLower(t.Title), needle) || strings.Contains(strings.ToLower(t.Name), needle) {
				b.Items = append(b.Items, p.libraryItem(t))
			}
			if len(b.Items) == limit {
				break
			}
		}
		return
	}
	route := "/search/"
	if b.Source == SourceTorznab {
		route = "/torznab/search/"
	}
	found, err := p.c.search(ctx, route, q.Query)
	switch {
	case errors.Is(err, errSearchOff):
		// switched off on this TorrServer: nothing found, no failure
		return
	case err != nil:
		failed(err)
		return
	}
	for _, d := range found {
		link := d.Magnet
		if link == "" && hashRe.MatchString(strings.ToLower(d.Hash)) {
			link = strings.ToLower(d.Hash)
		}
		if link == "" {
			continue
		}
		k := torrentKey(b.Source, link)
		title := d.Title
		if title == "" {
			title = d.Name
		}
		it := catalog.Item{Key: k, Provider: k.Provider, Source: k.Source, Kind: k.Kind, Title: title,
			Subtitle: d.Tracker, HasChildren: true, Playable: true,
			Torrent: &catalog.TorrentInfo{SizeBytes: parseSize(d.Size), Seeders: d.Seed, Leechers: d.Peer},
			Badges:  []catalog.Badge{{Kind: catalog.BadgeTorrent}}}
		b.Items = append(b.Items, it)
		if len(b.Items) == limit {
			break
		}
	}
}

var sizeRe = regexp.MustCompile(`^\s*([0-9]+(?:[.,][0-9]+)?)\s*([KMGT]?)I?B\s*$`)

// parseSize reads TorrServer's "1.4 GiB", 0 when it cannot
func parseSize(s string) int64 {
	m := sizeRe.FindStringSubmatch(strings.ToUpper(s))
	if m == nil {
		return 0
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64)
	if err != nil {
		return 0
	}
	mult := map[string]float64{"": 1, "K": 1 << 10, "M": 1 << 20, "G": 1 << 30, "T": 1 << 40}[m[2]]
	return int64(f * mult)
}

func (p *Provider) Browse(ctx context.Context, r catalog.BrowseRequest) (catalog.BrowsePage, error) {
	switch {
	case r.Path == catalog.FeedRoot || r.Path == catalog.FeedLibrary:
		list, err := p.c.list(ctx)
		if err != nil {
			return catalog.BrowsePage{}, p.fail(err, SourceLibrary)
		}
		items := make([]catalog.Item, 0, len(list))
		for _, t := range list {
			items = append(items, p.libraryItem(t))
		}
		if r.Limit > 0 && len(items) > r.Limit {
			items = items[:r.Limit]
		}
		return catalog.BrowsePage{Sections: []catalog.Section{{ID: catalog.FeedLibrary, Title: "Торренты",
			Path: catalog.FeedLibrary, Items: items}}}, nil
	case strings.HasPrefix(r.Path, "tree/"):
		hash := strings.TrimPrefix(r.Path, "tree/")
		d, err := p.Item(ctx, catalog.ItemRequest{Key: torrentKey(SourceLibrary, hash)})
		if err != nil {
			return catalog.BrowsePage{}, err
		}
		return catalog.BrowsePage{Sections: []catalog.Section{{ID: r.Path, Title: d.Item.Title, Path: r.Path,
			Items: d.Children}}}, nil
	}
	return catalog.BrowsePage{}, catalog.ErrNotFound
}

// torrent answers a torrent's status with its files: a library hash read,
// a search result added (kept in TorrServer's library) and polled until its
// metadata arrives
func (p *Provider) torrent(ctx context.Context, k catalog.Key, onStage func(catalog.Stage)) (torrentStatus, error) {
	if k.Kind != catalog.KindTorrent || k.ID == "" {
		return torrentStatus{}, catalog.ErrNotFound
	}
	hash := k.ID
	switch k.Source {
	case SourceLibrary:
		if !hashRe.MatchString(hash) {
			return torrentStatus{}, catalog.ErrNotFound
		}
	case SourceRutor, SourceTorznab:
		if onStage != nil {
			onStage(catalog.StageConnectingPeers)
		}
		t, err := p.c.add(ctx, k.ID, "", "")
		if err != nil {
			return torrentStatus{}, p.fail(err, k.Source)
		}
		if hash = t.Hash; !hashRe.MatchString(hash) {
			return torrentStatus{}, &catalog.Failure{Provider: catalog.ProviderTorrServer, Source: k.Source, Code: catalog.FailFailed}
		}
		if len(t.FileStats) > 0 {
			return t, nil
		}
	default:
		return torrentStatus{}, catalog.ErrNotFound
	}
	return p.waitInfo(ctx, hash, onStage)
}

func (p *Provider) waitInfo(ctx context.Context, hash string, onStage func(catalog.Stage)) (torrentStatus, error) {
	interval := p.Poll
	if interval <= 0 {
		interval = pollInterval
	}
	ctx, cancel := context.WithTimeout(ctx, infoWait)
	defer cancel()
	staged := false
	for {
		t, err := p.c.get(ctx, hash)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
				return torrentStatus{}, &catalog.Failure{Provider: catalog.ProviderTorrServer, Source: SourceLibrary, Code: catalog.FailNoSeeders}
			}
			return torrentStatus{}, p.fail(err, SourceLibrary)
		}
		if len(t.FileStats) > 0 {
			return t, nil
		}
		if !staged && onStage != nil {
			onStage(catalog.StageConnectingPeers)
			staged = true
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return torrentStatus{}, &catalog.Failure{Provider: catalog.ProviderTorrServer, Source: SourceLibrary, Code: catalog.FailNoSeeders}
			}
			return torrentStatus{}, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func ext(p string) string { return strings.ToLower(path.Ext(p)) }

func (p *Provider) fileItem(hash string, f fileStat, at float64, seen bool) catalog.Item {
	k := fileKey(hash, f.ID)
	e := ext(f.Path)
	it := catalog.Item{Key: k, Provider: k.Provider, Source: k.Source, Kind: k.Kind, Title: path.Base(f.Path),
		Playable: slices.Contains(videoExt, e) || slices.Contains(audioExt, e),
		File: &catalog.FileInfo{SizeBytes: f.Length, Index: f.ID, Path: f.Path, Video: slices.Contains(videoExt, e),
			Subtitle: slices.Contains(subExt, e)}}
	if seen || at > 0 {
		it.Progress = &catalog.Progress{Watched: seen && at == 0, PositionS: int(at)}
	}
	return it
}

func parseFile(k catalog.Key) (string, int, bool) {
	hash, idx, ok := strings.Cut(k.ID, "/")
	n, err := strconv.Atoi(idx)
	if !ok || err != nil || n < 1 || !hashRe.MatchString(hash) || k.Kind != catalog.KindFile {
		return "", 0, false
	}
	return hash, n, true
}

func (p *Provider) Item(ctx context.Context, r catalog.ItemRequest) (catalog.ItemDetail, error) {
	if r.Key.Kind == catalog.KindFile {
		hash, idx, ok := parseFile(r.Key)
		if !ok {
			return catalog.ItemDetail{}, catalog.ErrNotFound
		}
		t, err := p.c.get(ctx, hash)
		if err != nil {
			return catalog.ItemDetail{}, p.fail(err, SourceLibrary)
		}
		for _, f := range t.FileStats {
			if f.ID == idx {
				return catalog.ItemDetail{Item: p.fileItem(hash, f, 0, false)}, nil
			}
		}
		return catalog.ItemDetail{}, catalog.ErrNotFound
	}
	t, err := p.torrent(ctx, r.Key, nil)
	if err != nil {
		return catalog.ItemDetail{}, err
	}
	seen := map[int]float64{}
	if vs, err := p.c.viewed(ctx, t.Hash); err == nil {
		for _, v := range vs {
			seen[v.FileIndex] = v.TimeCode
		}
	}
	d := catalog.ItemDetail{Item: p.libraryItem(t)}
	files := slices.Clone(t.FileStats)
	slices.SortFunc(files, func(a, b fileStat) int { return a.ID - b.ID })
	for _, f := range files {
		at, ok := seen[f.ID]
		d.Children = append(d.Children, p.fileItem(t.Hash, f, at, ok))
	}
	d.Item.Torrent.Files = len(files)
	return d, nil
}

// playable files of a torrent, video first then audio, in TorrServer's order
func playableFiles(t torrentStatus) []fileStat {
	var video, audio []fileStat
	for _, f := range t.FileStats {
		switch e := ext(f.Path); {
		case slices.Contains(videoExt, e):
			video = append(video, f)
		case slices.Contains(audioExt, e):
			audio = append(audio, f)
		}
	}
	return append(video, audio...)
}

func (p *Provider) Options(ctx context.Context, r catalog.OptionsRequest) (catalog.Options, error) {
	if r.Key.Kind == catalog.KindFile {
		if _, _, ok := parseFile(r.Key); !ok {
			return catalog.Options{}, catalog.ErrNotFound
		}
		return catalog.Options{Selected: catalog.Selection{}}, nil
	}
	t, err := p.torrent(ctx, r.Key, nil)
	if err != nil {
		return catalog.Options{}, err
	}
	g := catalog.ChoiceGroup{Kind: catalog.GroupFile, Label: "Файл"}
	for _, f := range playableFiles(t) {
		g.Choices = append(g.Choices, catalog.Choice{ID: strconv.Itoa(f.ID), Label: path.Base(f.Path)})
	}
	sel := catalog.Selection{}
	if len(g.Choices) > 0 {
		sel[catalog.GroupFile] = g.Choices[0].ID
		if c := r.Selection[catalog.GroupFile]; slices.ContainsFunc(g.Choices, func(x catalog.Choice) bool { return x.ID == c }) {
			sel[catalog.GroupFile] = c
		}
	}
	return catalog.Options{Groups: []catalog.ChoiceGroup{g}, Selected: sel}, nil
}

func (p *Provider) Resolve(ctx context.Context, r catalog.ResolveRequest) (catalog.Playable, error) {
	var t torrentStatus
	var idx int
	var err error
	sel := catalog.Selection{}
	if r.Key.Kind == catalog.KindFile {
		hash, n, ok := parseFile(r.Key)
		if !ok {
			return catalog.Playable{}, catalog.ErrNotFound
		}
		if t, err = p.c.get(ctx, hash); err != nil {
			return catalog.Playable{}, p.fail(err, SourceLibrary)
		}
		if len(t.FileStats) == 0 {
			if t, err = p.waitInfo(ctx, hash, r.OnStage); err != nil {
				return catalog.Playable{}, err
			}
		}
		idx = n
	} else {
		if t, err = p.torrent(ctx, r.Key, r.OnStage); err != nil {
			return catalog.Playable{}, err
		}
		files := playableFiles(t)
		if len(files) == 0 {
			return catalog.Playable{}, &catalog.Failure{Provider: catalog.ProviderTorrServer, Source: r.Key.Source, Code: catalog.FailRemoved}
		}
		idx = files[0].ID
		if c, err := strconv.Atoi(r.Selection[catalog.GroupFile]); err == nil &&
			slices.ContainsFunc(files, func(f fileStat) bool { return f.ID == c }) {
			idx = c
		}
		sel[catalog.GroupFile] = strconv.Itoa(idx)
	}
	var file *fileStat
	for i := range t.FileStats {
		if t.FileStats[i].ID == idx {
			file = &t.FileStats[i]
		}
	}
	if file == nil {
		return catalog.Playable{}, catalog.ErrNotFound
	}
	if r.OnStage != nil {
		r.OnStage(catalog.StageStarting)
	}
	s := &catalog.Stream{URL: p.c.playURL(t.Hash, idx), InfoHash: t.Hash, FileIndex: idx}
	if r.From == catalog.FromResume {
		if vs, err := p.c.viewed(ctx, t.Hash); err == nil {
			for _, v := range vs {
				if v.FileIndex == idx {
					s.ResumeMS = int64(v.TimeCode * 1000)
				}
			}
		}
	}
	if p.version(ctx) >= subsVersion {
		if subs, err := p.c.subs(ctx, t.Hash, idx); err == nil {
			for _, sub := range subs {
				if sub.URL == "" {
					continue
				}
				s.Subtitles = append(s.Subtitles, catalog.Subtitle{URL: p.c.publicURL(sub.URL), Language: sub.Lang,
					Label: sub.Title, Format: "vtt"})
			}
		}
	}
	return catalog.Playable{Kind: catalog.PlayStream, Stream: s, Selected: sel}, nil
}

// Report stores a position in /viewed; TorrServer keeps it only with its
// TrackTimecode setting on, and marks a played file viewed by itself.
func (p *Provider) Report(ctx context.Context, r catalog.ProgressReport) error {
	hash, idx, ok := parseFile(r.Key)
	if !ok {
		return catalog.ErrRefused
	}
	at := float64(r.PositionS)
	if r.Watched {
		at = 0
	}
	if err := p.c.setViewed(ctx, hash, idx, at); err != nil {
		return p.fail(err, SourceLibrary)
	}
	return nil
}
