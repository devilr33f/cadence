package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/ambient"
	"git.shino.boo/devilreef/cadence/server/internal/config"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

func TestScreenAmbientReadsProfileConfigAndLanguage(t *testing.T) {
	state := func(_ context.Context, id, c string) (json.RawMessage, bool) {
		if id != "tv" || c != capschema.DisplayScreenID {
			return nil, false
		}
		return json.RawMessage(`{"power":"on","screen":true,"maxHeight":2160,"codecs":["hevc","h264"]}`), true
	}
	desired := func(_ context.Context, id, _ string) (json.RawMessage, error) {
		if id != "tv" {
			return nil, errors.New("none")
		}
		return json.RawMessage(`{"ambient":{"source":"images","categories":["space"],"aerialQuality":"4k"}}`), nil
	}
	lang := func(context.Context, string) string { return "ru" }
	d := screenAmbient(state, desired, lang)(context.Background(), "tv")
	if d.ID != "tv" || d.Language != "ru" || d.Profile.MaxHeight != 2160 || len(d.Profile.Codecs) != 2 ||
		d.Profile.Quality != "4k" || d.Source != ambient.SourceImages || len(d.Categories) != 1 {
		t.Fatalf("%+v", d)
	}
	bare := screenAmbient(state, desired, lang)(context.Background(), "speaker")
	if bare.ID != "speaker" || bare.Profile.MaxHeight != 0 || bare.Source != "" {
		t.Fatalf("%+v", bare)
	}
}

func TestAmbientMethodAnswersWithoutAManifest(t *testing.T) {
	m := ambientMethod(ambientService(nil, &lateArtwork{}, nil), func(_ context.Context, id string) ambient.Device { return ambient.Device{ID: id} })
	r := m(context.Background(), calls.Call{DeviceID: "tv", Method: rpc.AmbientNext})
	if r.Status != rpc.StatusUnavailable {
		t.Fatalf("%+v", r)
	}
	r = m(context.Background(), calls.Call{DeviceID: "tv", Method: rpc.AmbientNext, Args: json.RawMessage(`{"kind":"nope"}`)})
	if r.Status != rpc.StatusInvalid {
		t.Fatalf("%+v", r)
	}
}

func TestAerialFetcherFollowsTheConfig(t *testing.T) {
	if aerialFetcher(config.Config{AmbientManifest: "off"}, slog.New(slog.DiscardHandler)) != nil {
		t.Fatal("off fetches")
	}
	if aerialFetcher(config.Config{DevFakes: true}, slog.New(slog.DiscardHandler)) != nil {
		t.Fatal("the dev stack reaches apple")
	}
	if aerialFetcher(config.Config{}, slog.New(slog.DiscardHandler)) == nil {
		t.Fatal("no default fetcher")
	}
	if aerialFetcher(config.Config{DevFakes: true, AmbientManifest: "https://fake-apple.test/manifest.tar"}, slog.New(slog.DiscardHandler)) == nil {
		t.Fatal("no fetcher for the fake host")
	}
}

type fixedArtwork []ambient.Image

func (f fixedArtwork) Backdrops(context.Context, string, int) ([]ambient.Image, error) { return f, nil }

// the screens' ambient.next is built before the catalog: its backdrops are none until the catalog sets them
func TestLateArtworkFollowsTheCatalogOnceSet(t *testing.T) {
	var l lateArtwork
	if got, err := l.Backdrops(context.Background(), "tv", 4); err != nil || got != nil {
		t.Fatalf("before set: %v, %v", got, err)
	}
	l.set(fixedArtwork{{URL: "https://example.test/i/1"}})
	got, err := l.Backdrops(context.Background(), "tv", 4)
	if err != nil || len(got) != 1 || got[0].URL != "https://example.test/i/1" {
		t.Fatalf("after set: %v, %v", got, err)
	}
}
