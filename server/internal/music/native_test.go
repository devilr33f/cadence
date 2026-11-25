package music_test

import (
	"context"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/music"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// 11-char provider ids of songs the "sc" instance found
const (
	n1 = "11111111111"
	n2 = "22222222222"
	n3 = "33333333333"
)

func native(id string) providers.Track {
	return providers.Track{Source: providers.SourceSoundCloud, Instance: "sc", ID: id, Title: "Native " + id, Artist: "A", DurationS: 180}
}

func TestANativeSongStreamsFromItsOwnProviderThroughTheRelay(t *testing.T) {
	r := newRig(t)
	if err := r.svc.Play(context.Background(), "d1", []providers.Track{native(n1)}, 0, music.Options{}); err != nil {
		t.Fatal(err)
	}
	if got := r.playedIDs(); got != n1 {
		t.Fatalf("played %s", got)
	}
	if len(r.relayedHLS) != 1 || !r.relayedHLS[0] {
		t.Fatalf("relay hls flags %v", r.relayedHLS)
	}
	if v := r.svc.State("d1"); v.Current.Provider != "SoundCloud" {
		t.Fatalf("provider %q", v.Current.Provider)
	}
}

func TestANativePreviewFallsBackToYouTube(t *testing.T) {
	r := newRig(t)
	r.nativeErr[n1] = providers.ErrSignInRequired
	r.search = []providers.Track{track(a)}
	if err := r.svc.Play(context.Background(), "d1", []providers.Track{native(n1)}, 0, music.Options{}); err != nil {
		t.Fatal(err)
	}
	if got := r.playedIDs(); got != a {
		t.Fatalf("played %s, want the youtube match", got)
	}
}

func TestANativeRefusalWithoutAMatchSaysWhy(t *testing.T) {
	r := newRig(t)
	r.nativeErr[n1] = providers.ErrSignInRequired
	err := r.svc.Play(context.Background(), "d1", []providers.Track{native(n1)}, 0, music.Options{})
	if err == nil || err.Error() != providers.ErrSignInRequired.Error() {
		t.Fatalf("err %v, want the provider's own reason over not_on_youtube", err)
	}
}

func TestANativeRadioTopsUpFromItsOwnProvider(t *testing.T) {
	r := newRig(t)
	r.nativeRadio = []providers.Track{native(n1), native(n2), native(n3)}
	n, err := r.svc.StartRadio(context.Background(), "d1", native(n1))
	if err != nil {
		t.Fatal(err)
	}
	// the seed is not repeated, and youtube's radio is never asked
	if n != 3 || len(r.seeds) != 0 {
		t.Fatalf("queue %d, youtube seeds %v", n, r.seeds)
	}
	if err := r.svc.Skip(context.Background(), "d1", 1); err != nil {
		t.Fatal(err)
	}
	if got := r.playedIDs(); got != n1+","+n2 {
		t.Fatalf("played %s", got)
	}
}
