package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/playprogress"
)

type fakeSessions struct {
	mu  sync.Mutex
	s   map[string]media.MediaSession
	sig *sessionSignal
}

func (f *fakeSessions) set(dev string, s *media.MediaSession) {
	f.mu.Lock()
	if s == nil {
		delete(f.s, dev)
	} else {
		f.s[dev] = *s
	}
	f.mu.Unlock()
	f.sig.Changed(dev)
}

func (f *fakeSessions) raw(dev string) (media.MediaSession, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.s[dev]
	return s, ok
}

func awaitState(t *testing.T, p *cards.CatalogPlay, want cards.PlayState) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for p.State() != want {
		if time.Now().After(deadline) {
			t.Fatalf("state %d, want %d", p.State(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// a play's card follows the session the screen made for it: loading, then playing; a session that ends (or is
// replaced) before it played fails the card, and so does one that never comes
func TestFollowPlayFollowsTheScreensSession(t *testing.T) {
	f := &fakeSessions{s: map[string]media.MediaSession{}, sig: newSessionSignal()}
	run := func(p *cards.CatalogPlay, now func() time.Time) chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			followPlay(context.Background(), p, "tv", "sess-1", f.raw, f.sig.wait, now)
		}()
		return done
	}

	ok := cards.NewCatalogPlay("k1", cards.CatalogPlaying{Title: "Тьма"})
	done := run(ok, time.Now)
	f.set("tv", &media.MediaSession{SessionID: "sess-1", State: "buffering"})
	awaitState(t, ok, cards.PlayBuffering)
	f.set("tv", &media.MediaSession{SessionID: "sess-1", State: "playing"})
	<-done
	if ok.State() != cards.PlayPlaying {
		t.Fatalf("state %d", ok.State())
	}

	f.set("tv", nil)
	ended := cards.NewCatalogPlay("k2", cards.CatalogPlaying{Title: "Тьма"})
	done = run(ended, time.Now)
	f.set("tv", &media.MediaSession{SessionID: "sess-1", State: "buffering"})
	awaitState(t, ended, cards.PlayBuffering)
	f.set("tv", &media.MediaSession{SessionID: "sess-2", State: "playing"})
	<-done
	if ended.State() != cards.PlayFailed {
		t.Fatalf("replaced: %d", ended.State())
	}

	f.set("tv", nil)
	never := cards.NewCatalogPlay("k3", cards.CatalogPlaying{Title: "Тьма"})
	start := time.Now()
	late := func() time.Time { return time.Now().Add(time.Since(start)*1000 + playUnseen/2) }
	<-run(never, late)
	if never.State() != cards.PlayFailed {
		t.Fatalf("never seen: %d", never.State())
	}
}

// a found item's tile takes the name the screen's cards show and the source's format
func TestCatalogTile(t *testing.T) {
	it := catalog.Item{Ref: "r1", Title: "Solo Leveling", Kind: catalog.KindTitleGroup, Artwork: catalog.Artwork{Hash: "h"},
		Show: &catalog.ShowInfo{Names: catalog.Names{RU: "Поднятие уровня в одиночку"}, Format: "TV", Year: 2024,
			EpisodesAired: 12, EpisodesTotal: 12, Score: 8.3}}
	got := catalogTile(it)
	if got.Title != "Поднятие уровня в одиночку" || got.Kind != "tv" || got.Year != 2024 || got.Episodes != 12 ||
		got.Score != 8.3 || got.Artwork != "h" || got.Ref != "r1" {
		t.Fatalf("tile %+v", got)
	}
}

// a fetch's handle carries the play's card from its call on: its resolve stages while it
// fetches, what started (the screen's reports then carrying it on), or the call's failure
func TestFetchCardCarriesThePlaysCard(t *testing.T) {
	f := &fakeSessions{s: map[string]media.MediaSession{}, sig: newSessionSignal()}
	pc := playCards{session: f.raw, signal: f.sig, now: time.Now}
	subj := cards.NewCatalogFetch("k4", cards.CatalogPlaying{Title: "Тьма", Screen: "Bedroom TV"})
	h := fetchCard{subj: subj, pc: pc, call: agent.Call{}}
	h.Progress(playprogress.Event{Stage: playprogress.StageFormats})
	if subj.State() != cards.PlayFetching {
		t.Fatalf("a stage does not end the fetch: %d", subj.State())
	}
	h.Started(tools.StartedPlay{DeviceID: "tv", DeviceName: "Bedroom TV", SessionID: "sess-1", Title: "Тьма"})
	awaitState(t, subj, cards.PlayStarting)
	f.set("tv", &media.MediaSession{SessionID: "sess-1", State: "playing"})
	awaitState(t, subj, cards.PlayPlaying)

	lost := fetchCard{subj: cards.NewCatalogFetch("k5", cards.CatalogPlaying{Title: "Тьма"}), pc: pc}
	lost.Fail("unknown_ref")
	awaitState(t, lost.subj, cards.PlayFailed)
}
