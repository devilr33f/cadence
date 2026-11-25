// Package titleinfo reads an anime title's public facts: Shikimori's (Russian name and description, status,
// dates, genres, studios, episodes, its related titles) and MyAnimeList's through Jikan (rank, popularity, score),
// by their ids, anonymously: no account, no token. They are cached in memory for a day (titles change rarely), a
// failed read for ten minutes, and read at the pace each site allows. Texts are third parties': never logged.
package titleinfo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
)

const (
	DefaultShikimori = "https://shikimori.io"
	DefaultJikan     = "https://api.jikan.moe/v4"

	ttl       = 24 * time.Hour
	failTTL   = 10 * time.Minute
	maxCached = 4096
	// Shikimori allows 5 a second and 90 a minute; Jikan 3 and 60
	shikimoriPace = 700 * time.Millisecond
	jikanPace     = 1100 * time.Millisecond
	readTimeout   = 10 * time.Second
	maxBody       = 2 << 20
	maxRelated    = 12
	warmQueue     = 64
)

// Config names the sites; empty is their public address.
type Config struct {
	Shikimori, Jikan string
	// UserAgent names the server to Shikimori, which refuses a request without one
	UserAgent string
	Log       *slog.Logger
	Now       func() time.Time
}

// Service answers catalog.TitleInfo.
type Service struct {
	c    Config
	http *http.Client

	mu     sync.Mutex
	cache  map[string]entry
	order  []string
	flight map[string]chan struct{}

	shiki, jikan pacer
	warm         chan catalog.ExternalIDs
	stop         chan struct{}
}

type entry struct {
	info catalog.Info
	ok   bool
	at   time.Time
}

var _ catalog.TitleInfo = (*Service)(nil)

// New builds the service; its warm-up worker runs until Close.
func New(c Config, client *http.Client) *Service {
	if c.Shikimori == "" {
		c.Shikimori = DefaultShikimori
	}
	if c.Jikan == "" {
		c.Jikan = DefaultJikan
	}
	if c.UserAgent == "" {
		c.UserAgent = "Cadence"
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	s := &Service{c: c, http: client, cache: map[string]entry{}, flight: map[string]chan struct{}{},
		shiki: pacer{gap: shikimoriPace}, jikan: pacer{gap: jikanPace},
		warm: make(chan catalog.ExternalIDs, warmQueue), stop: make(chan struct{})}
	go s.warmer()
	return s
}

// Close stops the warm-up worker.
func (s *Service) Close() { close(s.stop) }

func keyOf(ids catalog.ExternalIDs) string {
	switch {
	case ids.Shikimori != "":
		return "shikimori:" + ids.Shikimori
	case ids.MAL != "":
		return "mal:" + ids.MAL
	}
	return ""
}

// Cached answers from the cache alone.
func (s *Service) Cached(ids catalog.ExternalIDs) (catalog.Info, bool) {
	e, ok := s.held(keyOf(ids))
	return e.info, ok && e.ok
}

func (s *Service) held(k string) (entry, bool) {
	if k == "" {
		return entry{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.cache[k]
	if !ok {
		return entry{}, false
	}
	life := ttl
	if !e.ok {
		life = failTTL
	}
	if s.c.Now().Sub(e.at) > life {
		return entry{}, false
	}
	return e, true
}

// Warm queues a read; a full queue drops it (the next page asks again).
func (s *Service) Warm(ids catalog.ExternalIDs) {
	if _, ok := s.held(keyOf(ids)); ok || keyOf(ids) == "" {
		return
	}
	select {
	case s.warm <- ids:
	default:
	}
}

func (s *Service) warmer() {
	for {
		select {
		case <-s.stop:
			return
		case ids := <-s.warm:
			ctx, cancel := context.WithTimeout(context.Background(), 3*readTimeout)
			_, _ = s.Info(ctx, ids)
			cancel()
		}
	}
}

// Info reads a title's facts, once at a time per title, from the cache when it holds them.
func (s *Service) Info(ctx context.Context, ids catalog.ExternalIDs) (catalog.Info, bool) {
	k := keyOf(ids)
	if k == "" {
		return catalog.Info{}, false
	}
	for {
		if e, ok := s.held(k); ok {
			return e.info, e.ok
		}
		s.mu.Lock()
		wait, busy := s.flight[k]
		if !busy {
			done := make(chan struct{})
			s.flight[k] = done
			s.mu.Unlock()
			info, err := s.read(ctx, ids)
			s.mu.Lock()
			delete(s.flight, k)
			close(done)
			if err == nil || !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				s.put(k, entry{info: info, ok: err == nil, at: s.c.Now()})
			}
			s.mu.Unlock()
			if err != nil {
				s.c.Log.Info("titleinfo_failed", "code", codeOf(err))
				return catalog.Info{}, false
			}
			return info, true
		}
		s.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return catalog.Info{}, false
		}
	}
}

// put keeps an entry; the caller holds mu
func (s *Service) put(k string, e entry) {
	if _, ok := s.cache[k]; !ok {
		s.order = append(s.order, k)
		if len(s.order) > maxCached {
			delete(s.cache, s.order[0])
			s.order = s.order[1:]
		}
	}
	s.cache[k] = e
}

// read asks Shikimori (by its id, which also names the MyAnimeList one), then Jikan for the rank
func (s *Service) read(ctx context.Context, ids catalog.ExternalIDs) (catalog.Info, error) {
	var info catalog.Info
	mal := ids.MAL
	var shikiErr error
	if ids.Shikimori != "" {
		var a shikiAnime
		if shikiErr = s.get(ctx, &s.shiki, s.c.Shikimori+"/api/animes/"+url.PathEscape(ids.Shikimori), &a); shikiErr == nil {
			info = a.info()
			if mal == "" && a.MALID > 0 {
				mal = strconv.Itoa(a.MALID)
			}
			var rel []shikiRelated
			if err := s.get(ctx, &s.shiki, s.c.Shikimori+"/api/animes/"+url.PathEscape(ids.Shikimori)+"/related", &rel); err == nil {
				info.Related = related(rel)
			}
		}
	}
	if mal == "" {
		return info, shikiErr
	}
	var j jikanOut
	if err := s.get(ctx, &s.jikan, s.c.Jikan+"/anime/"+url.PathEscape(mal), &j); err != nil {
		if shikiErr != nil || ids.Shikimori == "" {
			return info, err
		}
		return info, nil
	}
	info.Rank, info.Popularity = j.Data.Rank, j.Data.Popularity
	if info.Score == 0 {
		info.Score = j.Data.Score
	}
	if ids.Shikimori == "" || shikiErr != nil {
		jikanFill(&info, j)
	}
	return info, nil
}

type httpError int

func (e httpError) Error() string { return "titleinfo: http " + strconv.Itoa(int(e)) }

func codeOf(err error) string {
	var h httpError
	switch {
	case errors.As(err, &h):
		return "http_" + strconv.Itoa(int(h))
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "failed"
}

func (s *Service) get(ctx context.Context, p *pacer, u string, out any) error {
	if err := p.wait(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", s.c.UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return httpError(resp.StatusCode)
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, maxBody))
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("titleinfo: decode: %w", err)
	}
	return nil
}

// pacer spaces one site's requests
type pacer struct {
	mu   sync.Mutex
	gap  time.Duration
	next time.Time
}

func (p *pacer) wait(ctx context.Context) error {
	p.mu.Lock()
	now := time.Now()
	at := p.next
	if at.Before(now) {
		at = now
	}
	p.next = at.Add(p.gap)
	p.mu.Unlock()
	if d := time.Until(at); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Shikimori's shapes (/api/animes/{id} and its related)

type named struct {
	Name    string `json:"name"`
	Russian string `json:"russian"`
}

type shikiAnime struct {
	ID            int             `json:"id"`
	Kind          string          `json:"kind"`
	Score         json.RawMessage `json:"score"`
	Status        string          `json:"status"`
	Episodes      int             `json:"episodes"`
	EpisodesAired int             `json:"episodes_aired"`
	AiredOn       string          `json:"aired_on"`
	ReleasedOn    string          `json:"released_on"`
	Description   string          `json:"description"`
	MALID         int             `json:"myanimelist_id"`
	Genres        []named         `json:"genres"`
	Studios       []named         `json:"studios"`
}

func (a shikiAnime) info() catalog.Info {
	info := catalog.Info{Score: number(a.Score), Status: status(a.Status), AiredOn: date(a.AiredOn),
		ReleasedOn: date(a.ReleasedOn), EpisodesTotal: a.Episodes, EpisodesAired: a.EpisodesAired,
		Description: clean(a.Description)}
	if info.Status == "released" && info.EpisodesAired == 0 {
		info.EpisodesAired = a.Episodes
	}
	for _, g := range a.Genres {
		if n := first(g.Russian, g.Name); n != "" {
			info.Genres = append(info.Genres, strings.ToLower(n))
		}
	}
	for _, st := range a.Studios {
		if st.Name != "" {
			info.Studios = append(info.Studios, st.Name)
		}
	}
	return info
}

type shikiRelated struct {
	Relation        string `json:"relation"`
	RelationRussian string `json:"relation_russian"`
	Anime           *struct {
		ID      int    `json:"id"`
		Name    string `json:"name"`
		Russian string `json:"russian"`
		Kind    string `json:"kind"`
		AiredOn string `json:"aired_on"`
	} `json:"anime"`
}

func related(rs []shikiRelated) []catalog.RelatedTitle {
	var out []catalog.RelatedTitle
	for _, r := range rs {
		if r.Anime == nil || len(out) == maxRelated {
			continue
		}
		t := catalog.RelatedTitle{Relation: first(r.RelationRussian, r.Relation), Title: first(r.Anime.Russian, r.Anime.Name),
			Kind: r.Anime.Kind, Shikimori: strconv.Itoa(r.Anime.ID)}
		if d := date(r.Anime.AiredOn); len(d) >= 4 {
			t.Year, _ = strconv.Atoi(d[:4])
		}
		if t.Title != "" {
			out = append(out, t)
		}
	}
	return out
}

// Jikan's /anime/{id}

type jikanOut struct {
	Data struct {
		Score      float64 `json:"score"`
		Rank       int     `json:"rank"`
		Popularity int     `json:"popularity"`
		Status     string  `json:"status"`
		Episodes   int     `json:"episodes"`
		Aired      struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"aired"`
		Genres  []named `json:"genres"`
		Studios []named `json:"studios"`
	} `json:"data"`
}

func jikanFill(info *catalog.Info, j jikanOut) {
	d := j.Data
	switch d.Status {
	case "Currently Airing":
		info.Status = "ongoing"
	case "Finished Airing":
		info.Status = "released"
	case "Not yet aired":
		info.Status = "announced"
	}
	info.EpisodesTotal = max(info.EpisodesTotal, d.Episodes)
	info.AiredOn, info.ReleasedOn = date(d.Aired.From), date(d.Aired.To)
	for _, g := range d.Genres {
		info.Genres = append(info.Genres, strings.ToLower(g.Name))
	}
	for _, st := range d.Studios {
		info.Studios = append(info.Studios, st.Name)
	}
}

func status(s string) string {
	switch s {
	case "anons":
		return "announced"
	case "ongoing", "released":
		return s
	}
	return ""
}

// date is a yyyy-mm-dd day, "" for anything else
func date(s string) string {
	if len(s) < 10 {
		return ""
	}
	if _, err := time.Parse("2006-01-02", s[:10]); err != nil {
		return ""
	}
	return s[:10]
}

// number reads a score Shikimori sends as a string or a number
func number(raw json.RawMessage) float64 {
	t := strings.Trim(string(raw), `"`)
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || f < 0 || f > 10 {
		return 0
	}
	return f
}

func first(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return strings.TrimSpace(b)
}

var (
	bbcode = regexp.MustCompile(`\[/?[a-z_]+(=[^\]]*)?\]`)
	spaces = regexp.MustCompile(`[ \t]+`)
	breaks = regexp.MustCompile(`\n{3,}`)
)

// clean drops Shikimori's markup ([character=1]Name[/character] keeps Name) and the runs of blanks it leaves
func clean(s string) string {
	s = bbcode.ReplaceAllString(strings.ReplaceAll(s, "\r", ""), "")
	s = spaces.ReplaceAllString(s, " ")
	s = breaks.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
