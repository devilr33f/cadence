package music_test

import (
	"context"
	"errors"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/music"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func TestAStationFailureDoesNotBecomeYouTubeRadio(t *testing.T) {
	r := newRig(t)
	seed := native(n1)
	seed.Source = providers.SourceYandex
	seed.Station = "user:onyourwave"
	seed.VideoID = a
	r.nativeRadioErr = errors.New(providers.CodeMusicFailed)
	r.radio = []providers.Track{track(b)}
	if err := r.svc.Play(context.Background(), "d1", []providers.Track{seed}, 0, music.Options{Radio: true}); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Skip(context.Background(), "d1", 1); !errors.Is(err, music.ErrQueueEnd) {
		t.Fatalf("station refill error %v, want queue end", err)
	}
	if got := r.playedIDs(); got != a || len(r.seeds) != 0 {
		t.Fatalf("failed station played %s and youtube radio seeds %v", got, r.seeds)
	}
}

func TestAStationContinuesAfterItsSongMatchedOnYouTube(t *testing.T) {
	r := newRig(t)
	r.search = []providers.Track{track(a)}
	r.nativeErr[n1] = providers.ErrSignInRequired
	seed := native(n1)
	seed.Source = providers.SourceYandex
	seed.Station = "user:onyourwave"
	r.nativeRadio = []providers.Track{native(n2), native(n3)}
	if err := r.svc.Play(context.Background(), "d1", []providers.Track{seed}, 0, music.Options{Radio: true}); err != nil {
		t.Fatal(err)
	}
	// the first song had no native stream and now carries its YouTube video id
	if got := r.playedIDs(); got != a {
		t.Fatalf("first song %s, want youtube match %s", got, a)
	}
	if err := r.svc.Skip(context.Background(), "d1", 1); err != nil {
		t.Fatal(err)
	}
	if got := r.playedIDs(); got != a+","+n2 {
		t.Fatalf("station continuation played %s", got)
	}
	if len(r.nativeSeeds) != 1 || r.nativeSeeds[0].Station != "user:onyourwave" || r.nativeSeeds[0].VideoID != a {
		t.Fatalf("station radio seeds %+v", r.nativeSeeds)
	}
	if len(r.seeds) != 0 {
		t.Fatalf("the station turned into a youtube radio of %v", r.seeds)
	}
}
