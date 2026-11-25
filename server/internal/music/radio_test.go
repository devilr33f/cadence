package music_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/music"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func titles(ss []music.Song) string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strings.TrimPrefix(s.Title, "Song ")
	}
	return strings.Join(out, ",")
}

func TestStartRadioQueuesTheSeedAndItsRadio(t *testing.T) {
	r := newRig(t)
	r.radio = ids(a, x, y)
	n, err := r.svc.StartRadio(context.Background(), "d1", track(a))
	if err != nil || n != 3 {
		t.Fatalf("%d, %v", n, err)
	}
	v := r.svc.State("d1")
	if r.playedIDs() != a || !v.Radio || titles(v.Upcoming) != x+","+y || strings.Join(r.seeds, ",") != a {
		t.Fatalf("played %s, view %+v, seeds %v", r.playedIDs(), v, r.seeds)
	}
}

func TestStartRadioMatchesASpotifySeed(t *testing.T) {
	r := newRig(t)
	r.search = []providers.Track{{VideoID: b, Title: "Numb", DurationS: 188}}
	r.radio = ids(x)
	seed := providers.Track{Source: providers.SourceSpotify, ID: "2nLt", Title: "Numb", Artist: "Linkin Park", DurationS: 186}
	if _, err := r.svc.StartRadio(context.Background(), "d1", seed); err != nil {
		t.Fatal(err)
	}
	if r.playedIDs() != b || strings.Join(r.seeds, ",") != b {
		t.Fatalf("played %s, seeds %v", r.playedIDs(), r.seeds)
	}
	r.search = nil
	if _, err := r.svc.StartRadio(context.Background(), "d1", seed); !errors.Is(err, music.ErrNotOnYouTube) {
		t.Fatalf("%v", err)
	}
}

// a radio that cannot be read leaves the seed playing, topped up at its end
func TestStartRadioWithoutItsRadio(t *testing.T) {
	r := newRig(t)
	r.radioErr = errors.New(providers.CodeMusicFailed)
	n, err := r.svc.StartRadio(context.Background(), "d1", track(a))
	if err != nil || n != 1 || r.playedIDs() != a || !r.svc.State("d1").Radio {
		t.Fatalf("%d, %v, played %s", n, err, r.playedIDs())
	}
}

func TestRadioFromCurrentKeepsThePlayingSong(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if _, err := r.svc.RadioFromCurrent(ctx, "d1"); !errors.Is(err, music.ErrNoQueue) {
		t.Fatalf("without a queue: %v", err)
	}
	_ = r.svc.Play(ctx, "d1", ids(a, b, c), 0, music.Options{})
	_ = r.svc.Skip(ctx, "d1", 1)
	r.radio = ids(a, b, x, y)
	n, err := r.svc.RadioFromCurrent(ctx, "d1")
	if err != nil || n != 2 {
		t.Fatalf("%d, %v", n, err)
	}
	// b plays on: nothing was sent again
	v := r.svc.State("d1")
	if r.playedIDs() != a+","+b || v.Current.Title != "Song "+b || titles(v.Upcoming) != x+","+y ||
		titles(v.History) != a || !v.Radio || r.seeds[len(r.seeds)-1] != b {
		t.Fatalf("played %s, view %+v", r.playedIDs(), v)
	}
	// once it no longer plays, its radio starts it again
	r.mu.Lock()
	r.sess = nil
	r.mu.Unlock()
	if _, err := r.svc.RadioFromCurrent(ctx, "d1"); err != nil {
		t.Fatal(err)
	}
	if r.playedIDs() != a+","+b+","+b {
		t.Fatalf("played %s", r.playedIDs())
	}
	r.radioErr = errors.New(providers.CodeMusicFailed)
	if _, err := r.svc.RadioFromCurrent(ctx, "d1"); err == nil || err.Error() != providers.CodeMusicFailed {
		t.Fatalf("%v", err)
	}
}

// the queue stays within its cap: the oldest songs go first
func TestRadioFromCurrentKeepsTheCap(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	var long []providers.Track
	for i := range music.MaxQueue {
		long = append(long, track(strings.Repeat("0", 8)+string(rune('a'+i/26%26))+string(rune('a'+i%26))+"q"))
	}
	_ = r.svc.Play(ctx, "d1", long, music.MaxQueue-1, music.Options{})
	r.radio = ids(x, y)
	n, err := r.svc.RadioFromCurrent(ctx, "d1")
	v := r.svc.State("d1")
	if err != nil || n != 2 || titles(v.Upcoming) != x+","+y || v.Current.Title != long[music.MaxQueue-1].Title {
		t.Fatalf("%d, %v, view %+v", n, err, v.Upcoming)
	}
	// full: nothing more fits
	if n, err := r.svc.Add(ctx, "d1", ids(c), false); err != nil || n != 0 {
		t.Fatalf("added %d, %v", n, err)
	}
}
