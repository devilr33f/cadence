package titleinfo_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/titleinfo"
)

const anime = `{"id":52299,"name":"Ore dake Level Up na Ken","russian":"Поднятие уровня в одиночку","kind":"tv",
"score":"8.25","status":"released","episodes":12,"episodes_aired":0,"aired_on":"2024-01-07","released_on":"2024-03-31",
"myanimelist_id":52299,"description":"Десять лет назад [character=1]Сон Джин Ву[/character] стал охотником.\r\n\r\n\r\n[i]Ранг E.[/i]",
"genres":[{"name":"Action","russian":"Экшен"},{"name":"Fantasy","russian":"Фэнтези"}],"studios":[{"name":"A-1 Pictures"}]}`

const relatedJSON = `[{"relation":"Sequel","relation_russian":"Продолжение","anime":{"id":58567,"name":"Ore dake Level Up na Ken Season 2",
"russian":"Поднятие уровня в одиночку 2","kind":"tv","aired_on":"2025-01-05"}},
{"relation":"Adaptation","relation_russian":"Адаптация","anime":null,"manga":{"id":1}}]`

const jikan = `{"data":{"mal_id":52299,"score":8.27,"rank":148,"popularity":120,"status":"Finished Airing","episodes":12}}`

type sites struct {
	srv        *httptest.Server
	shiki, mal atomic.Int32
	agent      atomic.Value
	fail       atomic.Bool
}

func newSites(t *testing.T) *sites {
	s := &sites{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/animes/52299", func(w http.ResponseWriter, r *http.Request) {
		s.shiki.Add(1)
		s.agent.Store(r.Header.Get("User-Agent"))
		if s.fail.Load() {
			http.Error(w, "busy", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(anime))
	})
	mux.HandleFunc("GET /api/animes/52299/related", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(relatedJSON)) })
	mux.HandleFunc("GET /v4/anime/52299", func(w http.ResponseWriter, _ *http.Request) {
		s.mal.Add(1)
		_, _ = w.Write([]byte(jikan))
	})
	mux.HandleFunc("GET /v4/anime/1", func(w http.ResponseWriter, _ *http.Request) {
		s.mal.Add(1)
		_, _ = w.Write([]byte(`{"data":{"score":7.1,"rank":900,"popularity":50,"status":"Currently Airing","episodes":24,
"aired":{"from":"2025-04-01T00:00:00+00:00"},"genres":[{"name":"Drama"}],"studios":[{"name":"Bones"}]}}`))
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func newService(t *testing.T, s *sites, now func() time.Time) *titleinfo.Service {
	svc := titleinfo.New(titleinfo.Config{Shikimori: s.srv.URL, Jikan: s.srv.URL + "/v4", Now: now}, s.srv.Client())
	titleinfo.Unpaced(svc)
	t.Cleanup(svc.Close)
	return svc
}

// a title's facts: Shikimori's Russian ones (its markup dropped), its related anime (never a manga), MyAnimeList's
// rank and popularity; read once, then from the cache, by every caller at once
func TestInfoJoinsShikimoriAndMyAnimeList(t *testing.T) {
	s := newSites(t)
	svc := newService(t, s, time.Now)
	ids := catalog.ExternalIDs{Shikimori: "52299"}
	var wg sync.WaitGroup
	got := make([]catalog.Info, 4)
	for i := range got {
		wg.Go(func() { got[i], _ = svc.Info(context.Background(), ids) })
	}
	wg.Wait()
	info := got[0]
	if info.Score != 8.25 || info.Rank != 148 || info.Popularity != 120 || info.Status != "released" ||
		info.AiredOn != "2024-01-07" || info.ReleasedOn != "2024-03-31" || info.EpisodesTotal != 12 || info.EpisodesAired != 12 ||
		!slices.Equal(info.Genres, []string{"экшен", "фэнтези"}) || !slices.Equal(info.Studios, []string{"A-1 Pictures"}) {
		t.Fatalf("info %+v", info)
	}
	if info.Description != "Десять лет назад Сон Джин Ву стал охотником.\n\nРанг E." {
		t.Fatalf("description %q", info.Description)
	}
	if len(info.Related) != 1 || info.Related[0] != (catalog.RelatedTitle{Relation: "Продолжение", Title: "Поднятие уровня в одиночку 2",
		Kind: "tv", Year: 2025, Shikimori: "58567"}) {
		t.Fatalf("related %+v", info.Related)
	}
	for _, g := range got[1:] {
		if g.Rank != 148 {
			t.Fatalf("a caller got %+v", g)
		}
	}
	if _, err := svc.Info(context.Background(), ids); !err || s.shiki.Load() != 1 || s.mal.Load() != 1 {
		t.Fatalf("reads: shikimori %d, jikan %d", s.shiki.Load(), s.mal.Load())
	}
	if a, _ := s.agent.Load().(string); a == "" {
		t.Fatal("no user agent")
	}
	if c, ok := svc.Cached(ids); !ok || c.Rank != 148 {
		t.Fatal("not cached")
	}
}

// a title MyAnimeList alone knows reads Jikan's facts; a failed read is not asked again for a while, then is
func TestInfoFallsBackAndRemembersFailures(t *testing.T) {
	s := newSites(t)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	svc := newService(t, s, clock)
	info, ok := svc.Info(context.Background(), catalog.ExternalIDs{MAL: "1"})
	if !ok || info.Status != "ongoing" || info.AiredOn != "2025-04-01" || info.Rank != 900 || info.Score != 7.1 ||
		!slices.Equal(info.Genres, []string{"drama"}) {
		t.Fatalf("jikan alone %+v %v", info, ok)
	}
	s.fail.Store(true)
	if _, ok := svc.Info(context.Background(), catalog.ExternalIDs{Shikimori: "52299", MAL: "52299"}); !ok {
		// shikimori failed, jikan answered: still something
		t.Fatal("jikan's facts dropped")
	}
	if _, ok := svc.Info(context.Background(), catalog.ExternalIDs{Shikimori: "77"}); ok {
		t.Fatal("an unknown title")
	}
	n := s.shiki.Load()
	_, _ = svc.Info(context.Background(), catalog.ExternalIDs{Shikimori: "52299", MAL: "52299"})
	if s.shiki.Load() != n {
		t.Fatal("asked again at once")
	}
	if _, ok := svc.Cached(catalog.ExternalIDs{}); ok {
		t.Fatal("no ids")
	}
	mu.Lock()
	now = now.Add(25 * time.Hour)
	mu.Unlock()
	s.fail.Store(false)
	if info, ok := svc.Info(context.Background(), catalog.ExternalIDs{Shikimori: "52299", MAL: "52299"}); !ok || info.Description == "" || s.shiki.Load() == n {
		t.Fatalf("after a day %+v", info)
	}
}

// Warm reads in the background
func TestWarm(t *testing.T) {
	s := newSites(t)
	svc := newService(t, s, time.Now)
	svc.Warm(catalog.ExternalIDs{Shikimori: "52299"})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, ok := svc.Cached(catalog.ExternalIDs{Shikimori: "52299"}); ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("never warmed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
