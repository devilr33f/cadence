// Package torrplayertest is a fake torrplayer control API over a scripted
// library: the routes the catalog provider calls, with torrplayer's shapes
// (per-source search, episodes, sources ranked by the sticky prefs, resolve
// to URLs built on the request's host, progress, prefs, my list and
// subscriptions). Tests and server/tools/faketorrplayer serve it.
package torrplayertest

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Episode is one episode of a fake title.
type Episode struct {
	Label    string
	Ordinal  float64
	Name     string
	Type     string
	Duration int
}

// Source is one way to play an episode.
type Source struct {
	ID         string
	Kind       string // stream, hls or torrent
	Translator string
	TransType  string
	Lang       string
	Quality    int
	Episodes   string
	SizeBytes  int64
	Seeders    int
	Codec      string
	Hardsub    bool
	InfoHash   string
}

// SkipSeg is one skippable range in seconds.
type SkipSeg struct {
	Kind  string // opening, ending, recap, preview
	Start float64
	Stop  float64
}

// Title is one title of one source.
type Title struct {
	Source      string
	ID          string
	NameRu      string
	NameEn      string
	Year        int
	Kind        string
	Shikimori   int
	MAL         int
	Genres      []string
	Description string
	Status      string
	Score       float64
	Poster      string
	Episodes    []Episode
	// Sources play every episode
	Sources []Source
	// Skips are the provider's own markers, carried by the resolve answer
	Skips []SkipSeg
}

// Library is what the fake serves.
type Library struct {
	// Sources are the enabled sources, in torrplayer's order
	Sources []string
	Titles  []Title
	// Fail answers a source's search with this status
	Fail map[string]int
	// Slow delays a source's search
	Slow map[string]time.Duration
	// Token is the bearer token every /api/v1 route but /discover wants
	Token string
	// MyList are Shikimori entries: id → status
	MyList map[int]string
	// MyScore and MyWatched are an entry's own score and episodes watched
	MyScore, MyWatched map[int]int
	// StreamOrigin is the origin torrplayer builds its signed artwork urls
	// on, its stream server's: another port than the control API when its
	// [http] and [control] ports differ, which serves no /api/v1/image (only
	// the control API does); empty is the caller's own origin (one port)
	StreamOrigin string
	// Schedule is the airing feed: ongoing rows, a Source naming the release
	// a provider plays them from and "" for one nobody hosts. Nil answers
	// 404, as a torrplayer without the endpoint does.
	Schedule []Title
	// Skips are the ranges the skips endpoint answers beyond the provider's
	// own markers (the AniSkip-style external source), keyed
	// "<title id>:<episode label>". Absent means nobody knows: an empty
	// list, never an error.
	Skips map[string][]SkipSeg
}

type progress struct {
	prefs    map[string]any
	watched  map[string]bool
	position map[string]float64
}

// Fake serves a Library.
type Fake struct {
	lib Library

	once sync.Once
	h    http.Handler

	mu      sync.Mutex
	prog    map[string]*progress
	subs    map[string]bool
	reports []Report
	resolve map[string]string
	// skipsCalls counts the reads of the skips endpoint
	skipsCalls int
}

// Report is one progress report the fake took.
type Report struct {
	Source, Title, Episode, State string
	Position                      float64
}

// New serves lib.
func New(lib Library) *Fake {
	if lib.MyList == nil {
		lib.MyList = map[int]string{}
	}
	return &Fake{lib: lib, prog: map[string]*progress{}, subs: map[string]bool{}, resolve: map[string]string{}}
}

// Reports answers the progress reports so far.
func (f *Fake) Reports() []Report {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reports)
}

// Prefs answers a title's stored prefs.
func (f *Fake) Prefs(source, title string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.prog[source+"/"+title]; p != nil {
		return p.prefs
	}
	return nil
}

// MyListStatus answers a Shikimori entry's status.
func (f *Fake) MyListStatus(id int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lib.MyList[id]
}

// SkipsCalls answers how often the skips endpoint was read.
func (f *Fake) SkipsCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.skipsCalls
}

func (f *Fake) progressOf(source, title string) *progress {
	k := source + "/" + title
	p := f.prog[k]
	if p == nil {
		p = &progress{prefs: map[string]any{}, watched: map[string]bool{}, position: map[string]float64{}}
		f.prog[k] = p
	}
	return p
}

func (f *Fake) title(source, id string) (Title, bool) {
	for _, t := range f.lib.Titles {
		if t.Source == source && t.ID == id {
			return t, true
		}
	}
	return Title{}, false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": code}})
}

// ServeHTTP is the control API and the media routes.
func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.once.Do(f.build)
	f.h.ServeHTTP(w, r)
}

func (f *Fake) build() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /play/{hash}/{index}", f.media)
	mux.HandleFunc("GET /resolve/{id}", f.media)
	// public, as torrplayer's: the signature is the capability
	mux.HandleFunc("GET /api/v1/image", f.image)
	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/status", f.status)
	api.HandleFunc("GET /api/v1/search", f.search)
	api.HandleFunc("GET /api/v1/providers/{provider}/titles/{id}/episodes", f.episodes)
	api.HandleFunc("GET /api/v1/providers/{provider}/titles/{id}/sources", f.sources)
	api.HandleFunc("GET /api/v1/providers/{provider}/titles/{id}/skips", f.titleSkips)
	api.HandleFunc("POST /api/v1/resolve", f.resolveMedia)
	api.HandleFunc("GET /api/v1/progress", f.getProgress)
	api.HandleFunc("PUT /api/v1/progress/{provider}/{title}/prefs", f.setPrefs)
	api.HandleFunc("POST /api/v1/progress/{provider}/{title}/report", f.report)
	api.HandleFunc("PUT /api/v1/progress/{provider}/{title}/watched", f.setWatched)
	api.HandleFunc("GET /api/v1/watching", f.watching)
	api.HandleFunc("GET /api/v1/schedule", f.schedule)
	api.HandleFunc("GET /api/v1/mylist", f.myList)
	api.HandleFunc("GET /api/v1/mylist/{id}/resolve", f.myListResolve)
	api.HandleFunc("PATCH /api/v1/mylist/{id}", f.myListSet)
	api.HandleFunc("GET /api/v1/subscriptions", f.subscriptions)
	api.HandleFunc("POST /api/v1/subscriptions", f.subscribe)
	api.HandleFunc("DELETE /api/v1/subscriptions/{provider}/{title}", f.unsubscribe)
	mux.HandleFunc("/api/v1/", func(w http.ResponseWriter, r *http.Request) {
		if f.lib.Token != "" && r.Header.Get("Authorization") != "Bearer "+f.lib.Token {
			fail(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		api.ServeHTTP(w, r)
	})
	f.h = mux
}

// base is the origin the caller reached the fake by, as torrplayer builds
// its URLs
func base(r *http.Request) string { return "http://" + r.Host }

func (f *Fake) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"version": "fake", "providers": f.lib.Sources, "features": []string{}})
}

// sign is torrplayer's artwork signature over the upstream url
func sign(raw string) string {
	m := hmac.New(sha256.New, []byte("faketorrplayer"))
	m.Write([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:16])
}

// poster is a title's artwork as torrplayer hands it out with proxy_images
// on: its cdn url signed, on the stream server's origin
func (f *Fake) poster(r *http.Request, t Title) string {
	if t.Poster != "" {
		return t.Poster
	}
	origin := f.lib.StreamOrigin
	if origin == "" {
		origin = base(r)
	}
	raw := "https://cdn.invalid/posters/" + url.PathEscape(t.ID) + ".webp"
	return origin + "/api/v1/image?" + url.Values{"u": {raw}, "s": {sign(raw)}}.Encode()
}

func (f *Fake) titleJSON(r *http.Request, t Title) map[string]any {
	poster := f.poster(r, t)
	return map[string]any{
		"provider": t.Source, "id": t.ID, "name": t.NameEn, "name_ru": t.NameRu, "name_en": t.NameEn, "year": t.Year,
		"kind": t.Kind, "episodes_total": len(t.Episodes), "episodes_aired": len(t.Episodes), "poster": poster,
		"shikimori_id": t.Shikimori, "mal_id": t.MAL, "genres": t.Genres, "description": t.Description,
		"status": t.Status, "score": t.Score, "available": true,
	}
}

func (f *Fake) search(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(r.URL.Query().Get("q"))
	want := r.URL.Query().Get("provider")
	var sources []string
	for _, s := range f.lib.Sources {
		if want == "" || slices.Contains(strings.Split(want, ","), s) {
			sources = append(sources, s)
		}
	}
	out := map[string]any{"page": 1, "source": "providers"}
	titles := []map[string]any{}
	var errs []string
	for _, s := range sources {
		if d := f.lib.Slow[s]; d > 0 {
			select {
			case <-time.After(d):
			case <-r.Context().Done():
				return
			}
		}
		if st := f.lib.Fail[s]; st != 0 {
			errs = append(errs, s+": failed")
			continue
		}
		for _, t := range f.lib.Titles {
			if t.Source == s && (strings.Contains(strings.ToLower(t.NameRu), q) || strings.Contains(strings.ToLower(t.NameEn), q)) {
				titles = append(titles, f.titleJSON(r, t))
			}
		}
	}
	if len(titles) == 0 && len(errs) == len(sources) && len(errs) > 0 {
		st := f.lib.Fail[sources[0]]
		fail(w, st, "internal")
		return
	}
	if r.URL.Query().Get("genre") != "" {
		out["unenforced"] = []string{"genre"}
	}
	out["titles"], out["count"], out["errors"] = titles, len(titles), errs
	writeJSON(w, http.StatusOK, out)
}

func (f *Fake) episodes(w http.ResponseWriter, r *http.Request) {
	t, ok := f.title(r.PathValue("provider"), r.PathValue("id"))
	if !ok {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	f.mu.Lock()
	p := f.progressOf(t.Source, t.ID)
	next := ""
	eps := []map[string]any{}
	for _, e := range t.Episodes {
		eps = append(eps, map[string]any{"ordinal": e.Ordinal, "label": e.Label, "name": e.Name, "type": e.Type,
			"duration": e.Duration, "watched": p.watched[e.Label], "resume_sec": p.position[e.Label]})
		if next == "" && !p.watched[e.Label] {
			next = e.Label
		}
	}
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"provider": t.Source, "title_id": t.ID, "episodes": eps,
		"count": len(eps), "next_episode": next})
}

// ranked puts the source the title's prefs name first, as torrplayer's Rank
func (f *Fake) ranked(t Title) []Source {
	f.mu.Lock()
	prefs := f.progressOf(t.Source, t.ID).prefs
	f.mu.Unlock()
	tr, _ := prefs["translator"].(string)
	q, _ := prefs["quality"].(float64)
	out := slices.Clone(t.Sources)
	score := func(s Source) int {
		n := 0
		if tr != "" && s.Translator == tr {
			n += 2
		}
		if q > 0 && float64(s.Quality) == q {
			n++
		}
		return n
	}
	slices.SortStableFunc(out, func(a, b Source) int { return score(b) - score(a) })
	return out
}

func (f *Fake) sources(w http.ResponseWriter, r *http.Request) {
	t, ok := f.title(r.PathValue("provider"), r.PathValue("id"))
	if !ok {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	ep := r.URL.Query().Get("episode")
	if ep != "" && !slices.ContainsFunc(t.Episodes, func(e Episode) bool { return e.Label == ep }) {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	list := []map[string]any{}
	for _, s := range f.ranked(t) {
		list = append(list, map[string]any{"provider": t.Source, "title_id": t.ID, "id": s.ID, "kind": s.Kind,
			"quality": s.Quality, "translator": s.Translator, "trans_type": s.TransType, "lang": s.Lang,
			"episodes": s.Episodes, "size_bytes": s.SizeBytes, "seeders": s.Seeders, "codec": s.Codec,
			"hardsub": s.Hardsub, "info_hash": s.InfoHash})
	}
	writeJSON(w, http.StatusOK, map[string]any{"provider": t.Source, "title_id": t.ID, "episode": ep,
		"sources": list, "count": len(list), "hidden_count": 2})
}

// titleSkips answers the ranges beyond the provider's own markers: what the
// Library scripted, an empty list when it scripted nothing. An unknown title
// is the one real 404.
func (f *Fake) titleSkips(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.skipsCalls++
	known := f.lib.Skips[r.PathValue("id")+":"+r.URL.Query().Get("episode")]
	f.mu.Unlock()
	t, ok := f.title(r.PathValue("provider"), r.PathValue("id"))
	if !ok {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	list := []map[string]any{}
	for _, s := range known {
		list = append(list, map[string]any{"kind": s.Kind, "start": s.Start, "stop": s.Stop, "source": "aniskip"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"provider": t.Source, "title_id": t.ID,
		"episode": r.URL.Query().Get("episode"), "skips": list, "count": len(list)})
}

func (f *Fake) resolveMedia(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Provider string `json:"provider"`
		TitleID  string `json:"title_id"`
		SourceID string `json:"source_id"`
		Episode  string `json:"episode"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		fail(w, http.StatusBadRequest, "invalid_request")
		return
	}
	t, ok := f.title(in.Provider, in.TitleID)
	if !ok {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	i := slices.IndexFunc(t.Sources, func(s Source) bool { return s.ID == in.SourceID })
	if i < 0 {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	s := t.Sources[i]
	if s.Kind == "torrent" && s.Seeders == 0 {
		fail(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	markers := []map[string]any{}
	for _, sk := range t.Skips {
		markers = append(markers, map[string]any{"kind": sk.Kind, "start": sk.Start, "stop": sk.Stop, "source": t.Source})
	}
	out := map[string]any{"kind": s.Kind, "episode": in.Episode, "quality": s.Quality, "skips": markers}
	if s.Kind == "torrent" {
		idx := 1
		if n, err := strconv.Atoi(strings.Split(in.Episode, ".")[0]); err == nil {
			idx = n
		}
		out["url"], out["info_hash"], out["index"] = base(r)+"/play/"+s.InfoHash+"/"+strconv.Itoa(idx), s.InfoHash, idx
	} else {
		id := in.Provider + "-" + in.TitleID + "-" + in.Episode + "-" + s.ID
		f.mu.Lock()
		f.resolve[id] = s.ID
		f.mu.Unlock()
		out["url"] = base(r) + "/resolve/" + id
		out["subtitles"] = []map[string]any{{"url": base(r) + "/resolve/" + id + ".ass", "format": "ass", "lang": "ru"}}
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *Fake) getProgress(w http.ResponseWriter, r *http.Request) {
	src, id := r.URL.Query().Get("provider"), r.URL.Query().Get("title")
	t, ok := f.title(src, id)
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.progressOf(src, id)
	out := map[string]any{"provider": src, "title_id": id, "found": ok, "prefs": p.prefs}
	if ok {
		out["name"], out["shikimori_id"], out["episodes_total"] = t.NameRu, t.Shikimori, len(t.Episodes)
		out["poster"] = f.poster(r, t)
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *Fake) setPrefs(w http.ResponseWriter, r *http.Request) {
	var in map[string]any
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		fail(w, http.StatusBadRequest, "invalid_request")
		return
	}
	f.mu.Lock()
	p := f.progressOf(r.PathValue("provider"), r.PathValue("title"))
	if forget, _ := in["clear"].(bool); forget {
		p.prefs = map[string]any{}
	} else {
		p.prefs = in
	}
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"found": true})
}

func (f *Fake) report(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Episode  string  `json:"episode"`
		Position float64 `json:"position"`
		State    string  `json:"state"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Episode == "" {
		fail(w, http.StatusBadRequest, "invalid_request")
		return
	}
	src, title := r.PathValue("provider"), r.PathValue("title")
	f.mu.Lock()
	p := f.progressOf(src, title)
	p.position[in.Episode] = in.Position
	if in.State == "ended" {
		p.watched[in.Episode] = true
	}
	f.reports = append(f.reports, Report{Source: src, Title: title, Episode: in.Episode, State: in.State, Position: in.Position})
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"found": true})
}

func (f *Fake) setWatched(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Episode string `json:"episode"`
		Watched bool   `json:"watched"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Episode == "" {
		fail(w, http.StatusBadRequest, "invalid_request")
		return
	}
	f.mu.Lock()
	f.progressOf(r.PathValue("provider"), r.PathValue("title")).watched[in.Episode] = in.Watched
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"found": true})
}

func (f *Fake) watching(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	items := []map[string]any{}
	for _, t := range f.lib.Titles {
		p := f.prog[t.Source+"/"+t.ID]
		if p == nil || (len(p.watched) == 0 && len(p.position) == 0) {
			continue
		}
		n, resume := 0, ""
		for _, e := range t.Episodes {
			if p.watched[e.Label] {
				n++
			} else if resume == "" {
				resume = e.Label
			}
		}
		items = append(items, map[string]any{"provider": t.Source, "title_id": t.ID, "name": t.NameRu,
			"shikimori_id": t.Shikimori, "episodes_total": len(t.Episodes), "watched_count": n,
			"resume_episode": resume, "resume_sec": int(p.position[resume]), "poster": f.poster(r, t)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// schedule answers the airing feed in the search's shape: titleJSON per row,
// available only for a row a source hosts.
func (f *Fake) schedule(w http.ResponseWriter, r *http.Request) {
	if f.lib.Schedule == nil {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	rows := f.lib.Schedule
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && len(rows) > n {
		rows = rows[:n]
	}
	page := 1
	if n, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && n > 0 {
		page = n
	}
	titles := []map[string]any{}
	for _, t := range rows {
		row := f.titleJSON(r, t)
		row["available"] = t.Source != ""
		row["ongoing"] = t.Status == "ongoing"
		titles = append(titles, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"titles": titles, "count": len(titles),
		"page": page, "source": "shikimori"})
}

func (f *Fake) myList(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	f.mu.Lock()
	defer f.mu.Unlock()
	items := []map[string]any{}
	ids := make([]int, 0, len(f.lib.MyList))
	for id := range f.lib.MyList {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		st := f.lib.MyList[id]
		if status != "" && status != "all" && st != status {
			continue
		}
		item := map[string]any{"shikimori_id": id, "status": st, "watched": f.lib.MyWatched[id]}
		if sc := f.lib.MyScore[id]; sc > 0 {
			item["score"] = sc
		}
		for _, t := range f.lib.Titles {
			if t.Shikimori == id {
				item["name"], item["poster"] = t.NameRu, f.poster(r, t)
				break
			}
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "items": items, "count": len(items)})
}

func (f *Fake) myListResolve(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_request")
		return
	}
	for _, s := range f.lib.Sources {
		for _, t := range f.lib.Titles {
			if t.Shikimori == id && t.Source == s {
				writeJSON(w, http.StatusOK, map[string]any{"shikimori_id": id, "provider": t.Source, "title_id": t.ID})
				return
			}
		}
	}
	fail(w, http.StatusNotFound, "not_found")
}

func (f *Fake) myListSet(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	var in struct {
		Status string `json:"status"`
	}
	if err != nil || json.NewDecoder(r.Body).Decode(&in) != nil || in.Status == "" {
		fail(w, http.StatusBadRequest, "invalid_request")
		return
	}
	f.mu.Lock()
	f.lib.MyList[id] = in.Status
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (f *Fake) subscriptions(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := []map[string]any{}
	keys := make([]string, 0, len(f.subs))
	for k := range f.subs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		src, id, _ := strings.Cut(k, "/")
		name := ""
		if t, ok := f.title(src, id); ok {
			name = t.NameRu
		}
		list = append(list, map[string]any{"provider": src, "title_id": id, "name": name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": list, "count": len(list)})
}

func (f *Fake) subscribe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Provider string `json:"provider"`
		TitleID  string `json:"title_id"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		fail(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if _, ok := f.title(in.Provider, in.TitleID); !ok {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	f.mu.Lock()
	f.subs[in.Provider+"/"+in.TitleID] = true
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (f *Fake) unsubscribe(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	delete(f.subs, r.PathValue("provider")+"/"+r.PathValue("title"))
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// media serves a few bytes by ranges, as a file the device plays
func (f *Fake) media(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "video/x-matroska")
	http.ServeContent(w, r, "media.mkv", time.Time{}, bytes.NewReader(bytes.Repeat([]byte{0x1a}, 4096)))
}

// pixel is a 1×1 png
var pixel = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4" +
	"\x89\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")

// image is the signed artwork proxy: a url this fake signed, as a png (torrplayer
// transcodes what players may not decode)
func (f *Fake) image(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if raw := q.Get("u"); raw == "" || !hmac.Equal([]byte(q.Get("s")), []byte(sign(raw))) {
		fail(w, http.StatusForbidden, "bad_signature")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(pixel)
}

// Frieren is the scripted library: four sources (animego fails, nyaa holds
// torrents); Frieren on aniliberty and anime365 with one Shikimori id, its
// episodes 1, 5 and a recap 5.5, two voiceovers in two qualities, subtitles,
// a torrent without seeders and one with; and a film only anime365 has.
func Frieren() Library {
	eps := []Episode{{Label: "1", Ordinal: 1, Name: "Конец путешествия", Type: "tv", Duration: 1440},
		{Label: "5", Ordinal: 5, Type: "tv", Duration: 1440}, {Label: "5.5", Ordinal: 5.5, Type: "recap", Duration: 600}}
	srcs := []Source{
		{ID: "s-al-1080", Kind: "hls", Translator: "AniLibria", TransType: "voice", Lang: "ru", Quality: 1080},
		{ID: "s-al-720", Kind: "hls", Translator: "AniLibria", TransType: "voice", Lang: "ru", Quality: 720},
		{ID: "s-cr-720", Kind: "hls", Translator: "Crunchyroll", TransType: "sub", Lang: "ru", Quality: 720},
		{ID: "t-dead", Kind: "torrent", Episodes: "1-28", SizeBytes: 30 << 30, Quality: 1080, Codec: "hevc",
			InfoHash: strings.Repeat("ab", 20)},
		{ID: "t-live", Kind: "torrent", Episodes: "1-12", SizeBytes: 4 << 30, Seeders: 34, Quality: 1080, Codec: "hevc",
			Hardsub: true, InfoHash: strings.Repeat("cd", 20)},
	}
	frieren := func(src, id, en string) Title {
		return Title{Source: src, ID: id, NameRu: "Провожающая в последний путь Фрирен", NameEn: en, Year: 2023,
			Kind: "tv", Shikimori: 52991, MAL: 52991, Genres: []string{"adventure", "fantasy"}, Status: "released",
			Score: 9.3, Description: "Эльфийка-маг Фрирен после победы над Королём демонов.", Episodes: eps, Sources: srcs,
			Skips: []SkipSeg{{Kind: "opening", Start: 90, Stop: 180}}}
	}
	return Library{
		Sources: []string{"aniliberty", "anime365", "animego", "nyaa"},
		Titles: []Title{
			frieren("aniliberty", "t-frieren", "Frieren: Beyond Journey's End"),
			frieren("anime365", "t-9931", "Sousou no Frieren"),
			{Source: "anime365", ID: "t-77", NameRu: "Кольцо Фрирен (фильм)", Kind: "movie",
				Episodes: []Episode{{Label: "1", Ordinal: 1, Duration: 5400}}, Sources: srcs[:1]},
		},
		Fail:   map[string]int{"animego": http.StatusInternalServerError},
		Token:  "fake-token",
		MyList: map[int]string{52991: "watching"}, MyScore: map[int]int{52991: 9}, MyWatched: map[int]int{52991: 5},
	}
}
