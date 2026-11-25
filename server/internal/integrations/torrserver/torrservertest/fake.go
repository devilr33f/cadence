// Package torrservertest is a fake TorrServer (MatriX.150's shapes, per the
// research's client checklist): /echo; POST /torrents add, get, set, rem,
// list and drop with {"error"} bodies, add answering before the metadata;
// /stream with stat and play; /play/{hash}/{id} served by ranges with
// Connection: close; /playlist with host-built URLs; /viewed set, rem, list;
// /search and /torznab/search with Go-name keys (400 [] when off); /settings
// get and set of the search and TrackTimecode flags; /subs/{hash}/{id}.
// Basic auth, when set, guards what TorrServer guards. Tests and
// server/tools/faketorrserver serve it.
package torrservertest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// File is one file of a fake torrent.
type File struct {
	Path   string
	Length int64
}

// Torrent is one torrent the fake's swarm knows.
type Torrent struct {
	Hash   string
	Name   string
	Title  string
	Poster string
	Files  []File
	// Subs are external subtitle files (by 1-based file id) for a video id
	Subs map[int][]int
}

// Found is one search result.
type Found struct {
	Title, Size, Tracker string
	Seed, Peer           int
	Hash                 string
}

// Library is what the fake serves.
type Library struct {
	Version string
	// Swarm is every torrent an add can fetch metadata for
	Swarm []Torrent
	// Added are the hashes in the library from the start
	Added []string
	Rutor []Found
	// Torznab nil switches its search off
	Torznab []Found
	// GetsBeforeInfo is how many gets after an add answer no files yet
	GetsBeforeInfo int
	User, Password string
	TrackTimecode  bool
}

type entry struct {
	t       Torrent
	pending int
	added   int64
}

type view struct {
	Hash      string  `json:"hash"`
	FileIndex int     `json:"file_index"`
	TimeCode  float64 `json:"timecode"`
}

// Fake serves a Library.
type Fake struct {
	lib  Library
	once sync.Once
	h    http.Handler

	mu     sync.Mutex
	known  map[string]*entry
	viewed []view
	plays  int
}

// New serves lib.
func New(lib Library) *Fake {
	if lib.Version == "" {
		lib.Version = "MatriX.150"
	}
	f := &Fake{lib: lib, known: map[string]*entry{}}
	for i, h := range lib.Added {
		if t, ok := f.swarm(h); ok {
			f.known[h] = &entry{t: t, added: int64(1000 + i)}
		}
	}
	return f
}

// Plays counts media requests served.
func (f *Fake) Plays() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.plays
}

// Known says the hash is in the library.
func (f *Fake) Known(hash string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.known[hash] != nil
}

func (f *Fake) swarm(hash string) (Torrent, bool) {
	for _, t := range f.lib.Swarm {
		if t.Hash == hash {
			return t, true
		}
	}
	return Torrent{}, false
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.once.Do(f.build)
	f.h.ServeHTTP(w, r)
}

func (f *Fake) build() {
	mux := http.NewServeMux()
	guard := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if f.lib.User != "" {
				u, p, ok := r.BasicAuth()
				if !ok || u != f.lib.User || p != f.lib.Password {
					w.Header().Set("WWW-Authenticate", "Basic realm=Authorization Required")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /echo", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(f.lib.Version)) })
	mux.HandleFunc("POST /torrents", guard(f.torrents))
	mux.HandleFunc("POST /viewed", guard(f.view))
	mux.HandleFunc("POST /settings", guard(f.settings))
	mux.HandleFunc("GET /search/", guard(f.search(func() []Found { return f.lib.Rutor })))
	mux.HandleFunc("GET /torznab/search/", guard(f.search(func() []Found { return f.lib.Torznab })))
	mux.HandleFunc("GET /stream", f.stream)
	mux.HandleFunc("GET /play/{hash}/{id}", f.play)
	mux.HandleFunc("GET /playlist", f.playlist)
	mux.HandleFunc("GET /subs/{hash}/{id}", f.subs)
	f.h = mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func errJSON(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// status is a TorrentStatus; files only once the metadata arrived
func (f *Fake) status(e *entry) map[string]any {
	out := map[string]any{"title": e.t.Title, "poster": e.t.Poster, "name": e.t.Name, "hash": e.t.Hash,
		"timestamp": e.added, "stat": 3, "stat_string": "Torrent working"}
	if e.pending > 0 {
		out["stat"], out["stat_string"] = 1, "Torrent getting info"
		return out
	}
	var size int64
	files := []map[string]any{}
	for i, fl := range e.t.Files {
		files = append(files, map[string]any{"id": i + 1, "path": fl.Path, "length": fl.Length})
		size += fl.Length
	}
	out["file_stats"], out["torrent_size"], out["active_peers"], out["connected_seeders"] = files, size, 7, 3
	return out
}

func linkHash(link string) string {
	l := strings.ToLower(link)
	if i := strings.Index(l, "btih:"); i >= 0 {
		l = l[i+5:]
		if j := strings.IndexByte(l, '&'); j >= 0 {
			l = l[:j]
		}
	}
	return l
}

func (f *Fake) torrents(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action, Link, Hash, Title, Poster string
		SaveToDB                          bool `json:"save_to_db"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		errJSON(w, http.StatusBadRequest, "bad request")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch in.Action {
	case "add":
		if in.Link == "" {
			errJSON(w, http.StatusBadRequest, "link is empty")
			return
		}
		h := linkHash(in.Link)
		if e := f.known[h]; e != nil {
			writeJSON(w, http.StatusOK, f.status(e))
			return
		}
		t, ok := f.swarm(h)
		if !ok {
			errJSON(w, http.StatusInternalServerError, "error adding torrent")
			return
		}
		if in.Title != "" {
			t.Title = in.Title
		}
		e := &entry{t: t, pending: f.lib.GetsBeforeInfo + 1, added: time.Now().Unix()}
		f.known[h] = e
		writeJSON(w, http.StatusOK, f.status(e))
		e.pending--
	case "get":
		e := f.known[in.Hash]
		if e == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, f.status(e))
		if e.pending > 0 {
			e.pending--
		}
	case "set":
		if e := f.known[in.Hash]; e != nil {
			e.t.Title, e.t.Poster = in.Title, in.Poster
		}
	case "rem":
		delete(f.known, in.Hash)
	case "drop":
	case "list":
		list := []map[string]any{}
		for _, e := range f.known {
			list = append(list, f.status(e))
		}
		slices.SortFunc(list, func(a, b map[string]any) int { return int(b["timestamp"].(int64) - a["timestamp"].(int64)) })
		writeJSON(w, http.StatusOK, list)
	default:
		errJSON(w, http.StatusBadRequest, "unknown action")
	}
}

func (f *Fake) view(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action string `json:"action"`
		view
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	drop := func(hash string, idx int) {
		f.viewed = slices.DeleteFunc(f.viewed, func(v view) bool { return v.Hash == hash && (idx == -1 || v.FileIndex == idx) })
	}
	switch in.Action {
	case "set":
		drop(in.Hash, in.FileIndex)
		if !f.lib.TrackTimecode {
			in.TimeCode = 0
		}
		f.viewed = append(f.viewed, in.view)
	case "rem":
		drop(in.Hash, in.FileIndex)
	case "list":
		out := []view{}
		for _, v := range f.viewed {
			if in.Hash == "" || v.Hash == in.Hash {
				out = append(out, v)
			}
		}
		writeJSON(w, http.StatusOK, out)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func (f *Fake) settings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action string
		Sets   *struct{ EnableRutorSearch, EnableTorznabSearch, TrackTimecode bool } `json:"sets"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		errJSON(w, http.StatusBadRequest, "bad request")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch in.Action {
	case "get":
		writeJSON(w, http.StatusOK, map[string]any{"EnableRutorSearch": f.lib.Rutor != nil,
			"EnableTorznabSearch": f.lib.Torznab != nil, "TrackTimecode": f.lib.TrackTimecode, "CacheSize": 64 << 20})
	case "set":
		if in.Sets != nil {
			f.lib.TrackTimecode = in.Sets.TrackTimecode
		}
	default:
		errJSON(w, http.StatusBadRequest, "unknown action")
	}
}

func (f *Fake) search(list func() []Found) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		found := list()
		f.mu.Unlock()
		if found == nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("[]"))
			return
		}
		q := strings.ToLower(r.URL.Query().Get("query"))
		out := []map[string]any{}
		for _, d := range found {
			if !strings.Contains(strings.ToLower(d.Title), q) {
				continue
			}
			out = append(out, map[string]any{"Title": d.Title, "Name": d.Title, "Size": d.Size, "Tracker": d.Tracker,
				"Seed": d.Seed, "Peer": d.Peer, "Hash": d.Hash, "Magnet": "magnet:?xt=urn:btih:" + d.Hash,
				"CreateDate": "2024-01-01T00:00:00Z"})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func (f *Fake) stream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	h := linkHash(q.Get("link"))
	f.mu.Lock()
	e := f.known[h]
	f.mu.Unlock()
	if e == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case q.Has("stat"):
		f.mu.Lock()
		st := f.status(e)
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, st)
	case q.Has("play"):
		n, err := strconv.Atoi(q.Get("index"))
		if err != nil || n < 1 || n > len(e.t.Files) {
			errJSON(w, http.StatusBadRequest, `"index" is empty or wrong`)
			return
		}
		f.serve(w, r, e.t.Files[n-1].Path)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func (f *Fake) play(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	e := f.known[r.PathValue("hash")]
	f.mu.Unlock()
	if e == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	n, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || n < 1 || n > len(e.t.Files) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	f.serve(w, r, e.t.Files[n-1].Path)
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request, name string) {
	f.mu.Lock()
	f.plays++
	f.mu.Unlock()
	w.Header().Set("Connection", "close")
	w.Header().Set("Server", "TorrServer (Portable SDK for UPnP devices)")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(bytes.Repeat([]byte{0x1a}, 8192)))
}

func (f *Fake) playlist(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	e := f.known[r.URL.Query().Get("hash")]
	f.mu.Unlock()
	if e == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for i, fl := range e.t.Files {
		b.WriteString("#EXTINF:0," + fl.Path + "\n")
		b.WriteString("http://" + r.Host + "/stream/" + fl.Path + "?link=" + e.t.Hash + "&index=" + strconv.Itoa(i+1) + "&play\n")
	}
	w.Header().Set("Content-Type", "audio/x-mpegurl")
	_, _ = w.Write([]byte(b.String()))
}

func (f *Fake) subs(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	e := f.known[r.PathValue("hash")]
	f.mu.Unlock()
	if e == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	n, _ := strconv.Atoi(r.PathValue("id"))
	if r.URL.Query().Has("track") {
		w.Header().Set("Content-Type", "text/vtt")
		_, _ = w.Write([]byte("WEBVTT\n"))
		return
	}
	out := []map[string]any{}
	for _, s := range e.t.Subs[n] {
		out = append(out, map[string]any{"index": s, "name": e.t.Files[s-1].Path, "title": "Русские", "lang": "ru",
			"format": "srt", "url": "/subs/" + e.t.Hash + "/" + strconv.Itoa(n) + "?track=" + strconv.Itoa(s)})
	}
	writeJSON(w, http.StatusOK, out)
}

// hashes of the scripted library
var (
	MovieHash  = strings.Repeat("a1", 20)
	SeriesHash = strings.Repeat("b2", 20)
	FoundHash  = strings.Repeat("c3", 20)
)

// Scripted is the scripted library: a film (its video and subtitles) and a
// series of three episodes in the library, and a third torrent rutor and
// torznab find, whose metadata arrives after a get.
func Scripted() Library {
	return Library{
		Swarm: []Torrent{
			{Hash: MovieHash, Name: "Arrival.2016.1080p", Title: "Прибытие", Files: []File{
				{Path: "Arrival.2016.1080p/Arrival.2016.1080p.mkv", Length: 8 << 30},
				{Path: "Arrival.2016.1080p/Arrival.2016.rus.srt", Length: 90 << 10},
			}, Subs: map[int][]int{1: {2}}},
			{Hash: SeriesHash, Name: "Dark.S01", Title: "Тьма, сезон 1", Files: []File{
				{Path: "Dark.S01/Dark.S01E01.mkv", Length: 2 << 30}, {Path: "Dark.S01/Dark.S01E02.mkv", Length: 2 << 30},
				{Path: "Dark.S01/Dark.S01E10.mkv", Length: 2 << 30}, {Path: "Dark.S01/cover.jpg", Length: 200 << 10},
			}},
			{Hash: FoundHash, Name: "Dune.Part.Two.2024.2160p", Files: []File{
				{Path: "Dune.Part.Two.2024.2160p.mkv", Length: 30 << 30},
			}},
		},
		Added:          []string{MovieHash, SeriesHash},
		Rutor:          []Found{{Title: "Дюна: Часть вторая / Dune: Part Two (2024) 2160p", Size: "30.1 GiB", Tracker: "rutor", Seed: 120, Peer: 14, Hash: FoundHash}},
		Torznab:        []Found{{Title: "Dune Part Two 2024 2160p", Size: "30 GB", Tracker: "jackett", Seed: 80, Peer: 3, Hash: FoundHash}},
		GetsBeforeInfo: 1,
		TrackTimecode:  true,
	}
}
