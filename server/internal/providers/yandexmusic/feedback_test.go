package yandexmusic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
)

type feedbackCall struct {
	path, batch, kind, track, from string
	played                         *float64
}

func feedbackFake(t *testing.T) (http.HandlerFunc, func() []feedbackCall, *int) {
	t.Helper()
	var mu sync.Mutex
	var calls []feedbackCall
	uidHits := new(int)
	return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "OAuth "+probetest.Key {
				t.Errorf("feedback auth %q", r.Header.Get("Authorization"))
			}
			mu.Lock()
			defer mu.Unlock()
			if r.URL.Path == "/account/status" {
				*uidHits++
				_, _ = io.WriteString(w, `{"result":{"account":{"uid":12345}}}`)
				return
			}
			call := feedbackCall{path: r.URL.Path, batch: r.URL.Query().Get("batch-id")}
			if r.Method == http.MethodPost && r.Header.Get("Content-Type") == "application/json" {
				var body struct {
					Type      string   `json:"type"`
					Timestamp string   `json:"timestamp"`
					TrackID   string   `json:"trackId"`
					From      string   `json:"from"`
					Played    *float64 `json:"totalPlayedSeconds"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Timestamp != "2026-11-18T12:00:00Z" {
					t.Errorf("feedback body %+v, err %v", body, err)
				}
				call.kind, call.track, call.from, call.played = body.Type, body.TrackID, body.From, body.Played
			} else {
				_ = r.ParseForm()
				if r.PostForm.Get("track-ids") != "42" {
					t.Errorf("rating form %v", r.PostForm)
				}
			}
			calls = append(calls, call)
			_, _ = io.WriteString(w, `{"result":"ok"}`)
		}, func() []feedbackCall {
			mu.Lock()
			defer mu.Unlock()
			return append([]feedbackCall(nil), calls...)
		}, uidHits
}

func TestRateTrackUpdatesYandexLibrary(t *testing.T) {
	handler, calls, uidHits := feedbackFake(t)
	// library mutations answer a revision, unlike station feedback's "ok"
	base := handler
	handler = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/account/status" && r.Header.Get("Content-Type") != "application/json" {
			_ = r.ParseForm()
			if r.PostForm.Get("track-ids") != "42" || r.Header.Get("Authorization") != "OAuth "+probetest.Key {
				t.Errorf("rating request %s, form %v", r.URL.Path, r.PostForm)
			}
			calls()
			_, _ = io.WriteString(w, `{"result":{"revision":7}}`)
			return
		}
		base(w, r)
	}
	var mu sync.Mutex
	var paths []string
	recorder := handler
	handler = func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		recorder(w, r)
	}
	srv := httptest.NewTLSServer(handler)
	defer srv.Close()
	c := newClient(srv, Config{Token: probetest.Key})
	track := providers.Track{Source: providers.SourceYandex, ID: "42"}
	for _, rating := range []providers.TrackRating{providers.RatingLike, providers.RatingDislike, providers.RatingNone} {
		if err := c.RateTrack(context.Background(), track, rating); err != nil {
			t.Fatalf("rate %s: %v", rating, err)
		}
	}
	want := []string{
		"/account/status",
		"/users/12345/likes/tracks/add-multiple",
		"/users/12345/dislikes/tracks/add-multiple",
		"/users/12345/likes/tracks/remove",
		"/users/12345/dislikes/tracks/remove",
	}
	if len(paths) != len(want) {
		t.Fatalf("rating paths %v", paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("rating paths %v", paths)
		}
	}
	if *uidHits != 1 {
		t.Fatalf("account status read %d times", *uidHits)
	}
}

func TestRateTrackWithoutAnAccountAsksForSignIn(t *testing.T) {
	hits := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer srv.Close()
	err := newClient(srv, Config{}).RateTrack(context.Background(), providers.Track{Source: providers.SourceYandex, ID: "42"}, providers.RatingLike)
	if !errors.Is(err, providers.ErrSignInRequired) || hits != 0 {
		t.Fatalf("rating err %v, upstream hits %d", err, hits)
	}
}

func TestReportListeningSendsOrderedStationEvents(t *testing.T) {
	handler, calls, _ := feedbackFake(t)
	srv := httptest.NewTLSServer(handler)
	defer srv.Close()
	c := newClient(srv, Config{Token: probetest.Key})
	at := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	track := providers.Track{Source: providers.SourceYandex, ID: "42", Station: myWaveStation, StationBatch: "batch-7"}
	events := []providers.ListeningEvent{
		{Track: track, Kind: providers.ListeningRadioStarted, At: at},
		{Track: track, Kind: providers.ListeningTrackStarted, At: at},
		{Track: track, Kind: providers.ListeningTrackFinished, At: at, PlayedS: 180.25},
		{Track: track, Kind: providers.ListeningTrackSkipped, At: at, PlayedS: 12.5},
	}
	for _, event := range events {
		if err := c.ReportListening(context.Background(), event); err != nil {
			t.Fatalf("report %s: %v", event.Kind, err)
		}
	}
	got := calls()
	if len(got) != len(events) {
		t.Fatalf("feedback calls %+v", got)
	}
	for i, call := range got {
		if call.path != "/rotor/station/user:onyourwave/feedback" || call.batch != "batch-7" || call.kind != string(events[i].Kind) {
			t.Fatalf("feedback %d: %+v", i, call)
		}
	}
	if got[0].track != "" || got[0].from != "cadence-server" || got[1].track != "42" {
		t.Fatalf("radio and track starts %+v", got[:2])
	}
	if got[2].played == nil || *got[2].played != 180.25 || got[3].played == nil || *got[3].played != 12.5 {
		t.Fatalf("played seconds %+v", got[2:])
	}
}

func TestReportListeningDerivesATrackStation(t *testing.T) {
	handler, calls, _ := feedbackFake(t)
	srv := httptest.NewTLSServer(handler)
	defer srv.Close()
	event := providers.ListeningEvent{
		Track: providers.Track{Source: providers.SourceYandex, ID: "42"}, Kind: providers.ListeningTrackStarted,
		At: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
	}
	if err := newClient(srv, Config{Token: probetest.Key}).ReportListening(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	got := calls()
	if len(got) != 1 || got[0].path != "/rotor/station/track:42/feedback" {
		t.Fatalf("derived station calls %+v", got)
	}
}
