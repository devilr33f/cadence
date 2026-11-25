package yandexmusic

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
)

func TestMyWaveSearchNeedsNoUpstream(t *testing.T) {
	hits := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer srv.Close()
	got, err := newClient(srv, Config{}).SearchCollections(context.Background(), providers.KindStation, "my vibe", 5)
	want := providers.Collection{
		Source: providers.SourceYandex, Kind: providers.KindStation, ID: myWaveStation,
		Title: "Моя волна", Owner: "Yandex Music",
	}
	if err != nil || len(got) != 1 || got[0] != want || hits != 0 {
		t.Fatalf("stations %+v, err %v, upstream hits %d", got, err, hits)
	}
}

func TestMyWavePlaysAndContinuesItsOwnStation(t *testing.T) {
	var queues, settings []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rotor/station/user:onyourwave/tracks" || r.Header.Get("Authorization") != "OAuth "+probetest.Key {
			t.Errorf("station request %s, auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		queues = append(queues, r.URL.Query().Get("queue"))
		settings = append(settings, r.URL.Query().Get("settings2"))
		_, _ = io.WriteString(w, `{"result":{"batchId":"batch-1","sequence":[{"track":`+song("40", "1")+`},{"track":`+song("41", "1")+`},{"track":`+song("42", "1")+`}]}}`)
	}))
	defer srv.Close()
	c := newClient(srv, Config{Token: probetest.Key})
	col := providers.Collection{Source: providers.SourceYandex, Kind: providers.KindStation, ID: myWaveStation}
	got, err := c.CollectionSongs(context.Background(), col, 3)
	if err != nil || ids(got) != "40,41,42," {
		t.Fatalf("wave songs %s, err %v", ids(got), err)
	}
	for _, track := range got {
		if track.Station != myWaveStation || track.StationBatch != "batch-1" {
			t.Fatalf("song lost its station or batch: %+v", track)
		}
	}
	// a song matched on YouTube still carries the station its queue continues
	got, err = c.TrackRadio(context.Background(), providers.Track{
		Source: providers.SourceYandex, ID: "40", VideoID: "5qZQEq_C3vc", Station: myWaveStation,
	}, 2)
	if err != nil || ids(got) != "41,42," {
		t.Fatalf("next wave batch %s, err %v", ids(got), err)
	}
	for _, track := range got {
		if track.Station != myWaveStation {
			t.Fatalf("next batch lost its station: %+v", track)
		}
	}
	if len(queues) != 2 || queues[0] != "" || queues[1] != "40" {
		t.Fatalf("station continuation queues %v", queues)
	}
	if len(settings) != 2 || settings[0] != "true" || settings[1] != "" {
		t.Fatalf("station settings2 values %v", settings)
	}
}

func TestMyWaveNeedsAnAccountWithoutTouchingTheAPI(t *testing.T) {
	hits := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer srv.Close()
	c := newClient(srv, Config{})
	col := providers.Collection{Source: providers.SourceYandex, Kind: providers.KindStation, ID: myWaveStation}
	if _, err := c.CollectionSongs(context.Background(), col, 5); !errors.Is(err, providers.ErrSignInRequired) {
		t.Fatalf("wave without an account: %v", err)
	}
	seed := providers.Track{Source: providers.SourceYandex, ID: "40", Station: myWaveStation}
	if _, err := c.TrackRadio(context.Background(), seed, 5); !errors.Is(err, providers.ErrSignInRequired) {
		t.Fatalf("next wave batch without an account: %v", err)
	}
	for _, bad := range []providers.Collection{
		{Source: providers.SourceYandex, Kind: providers.KindStation, ID: "user:other"},
		{Source: providers.SourceYandex, Kind: providers.KindStation, ID: "../track:40"},
	} {
		if _, err := c.CollectionSongs(context.Background(), bad, 5); err == nil {
			t.Fatalf("unknown station %+v read", bad)
		}
	}
	if hits != 0 {
		t.Fatalf("unauthenticated or invalid station made %d upstream calls", hits)
	}
}
