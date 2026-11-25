package music_test

import (
	"context"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/music"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func waveSong(id string) providers.Track {
	t := native(id)
	t.Source = providers.SourceYandex
	t.Station = "user:onyourwave"
	t.StationBatch = "batch-1"
	return t
}

// a wave queue reports its station's start, each song's start, a natural end
// with the seconds played, and a skip with where it was left
func TestAStationHearsStartsFinishesAndSkips(t *testing.T) {
	r := newRig(t)
	r.nativeRadio = []providers.Track{native(n2), native(n3)}
	if err := r.svc.Play(context.Background(), "d1", []providers.Track{waveSong(n1)}, 0, music.Options{Radio: true}); err != nil {
		t.Fatal(err)
	}
	starts := r.waitHeard(t, 2)
	if starts[0].Kind != providers.ListeningRadioStarted || starts[0].Track.StationBatch != "batch-1" ||
		starts[1].Kind != providers.ListeningTrackStarted || starts[1].Track.ID != n1 {
		t.Fatalf("starts %+v", starts)
	}
	// the song plays 30 s of its 180 s and the owner skips it
	r.advance(30 * time.Second)
	if err := r.svc.Skip(context.Background(), "d1", 1); err != nil {
		t.Fatal(err)
	}
	events := r.waitHeard(t, 3)
	if events[2].Kind != providers.ListeningTrackSkipped || events[2].Track.ID != n1 || events[2].PlayedS != 30 {
		t.Fatalf("skip %+v", events[2])
	}
	// the next song plays to its end by itself
	starts = r.waitHeard(t, 4)
	if starts[3].Kind != providers.ListeningTrackStarted || starts[3].Track.ID != n2 {
		t.Fatalf("second song's start %+v", starts[3])
	}
	r.advance(185 * time.Second)
	r.mu.Lock()
	r.sess = nil
	r.mu.Unlock()
	r.svc.SessionChanged("d1")
	finished := r.waitHeard(t, 5)
	if finished[4].Kind != providers.ListeningTrackFinished || finished[4].Track.ID != n2 || finished[4].PlayedS != 180 {
		t.Fatalf("finish %+v", finished[4])
	}
	for r.playedIDs() != n1+","+n2+","+n3 && time.Now().Before(time.Now().Add(2*time.Second)) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := r.playedIDs(); got != n1+","+n2+","+n3 {
		t.Fatalf("played %s", got)
	}
}

// the first seed has no batch yet but still belongs to its track station
func TestATrackRadioSeedReportsItsDerivedStation(t *testing.T) {
	r := newRig(t)
	seed := native(n1)
	seed.Source = providers.SourceYandex
	if err := r.svc.Play(context.Background(), "d1", []providers.Track{seed}, 0, music.Options{Radio: true}); err != nil {
		t.Fatal(err)
	}
	events := r.waitHeard(t, 2)
	for _, event := range events {
		if event.Track.Station != "track:"+n1 {
			t.Fatalf("seed station %+v", event)
		}
	}
}

func TestAReplacementSkipsTheOldSongBeforeTheNewStart(t *testing.T) {
	r := newRig(t)
	if err := r.svc.Play(context.Background(), "d1", []providers.Track{waveSong(n1)}, 0, music.Options{Radio: true}); err != nil {
		t.Fatal(err)
	}
	r.waitHeard(t, 2)
	r.advance(12 * time.Second)
	if err := r.svc.Play(context.Background(), "d1", []providers.Track{waveSong(n2)}, 0, music.Options{Radio: true}); err != nil {
		t.Fatal(err)
	}
	events := r.waitHeard(t, 5)
	if events[2].Kind != providers.ListeningTrackSkipped || events[2].Track.ID != n1 || events[2].PlayedS != 12 ||
		events[3].Kind != providers.ListeningRadioStarted || events[3].Track.ID != n2 ||
		events[4].Kind != providers.ListeningTrackStarted || events[4].Track.ID != n2 {
		t.Fatalf("replacement events %+v", events[2:])
	}
}

// a stop is not a finish: nothing reaches the station, and the wave never turns
// into youtube radio (station_test holds that part)
func TestAStoppedSongReportsNothing(t *testing.T) {
	r := newRig(t)
	if err := r.svc.Play(context.Background(), "d1", []providers.Track{waveSong(n1)}, 0, music.Options{Radio: true}); err != nil {
		t.Fatal(err)
	}
	r.waitHeard(t, 2)
	r.advance(10 * time.Second)
	r.sess = nil
	r.svc.SessionChanged("d1")
	time.Sleep(20 * time.Millisecond)
	r.mu.Lock()
	heard := len(r.heard)
	r.mu.Unlock()
	if heard != 2 {
		t.Fatalf("a stop reported %d events", heard-2)
	}
}
