package ytmusic_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytmusic"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/songs.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// the fixture is a trimmed live answer to "linkin park numb" on the songs tab
func TestParseSongsReadsRowsInOrder(t *testing.T) {
	got, ok := ytmusic.ParseSongs(fixture(t))
	if !ok || len(got) < 4 {
		t.Fatalf("ok %v, %d songs", ok, len(got))
	}
	want := providers.Track{
		Source: "youtube", ID: "5qZQEq_C3vc", VideoID: "5qZQEq_C3vc", Title: "Numb", Artist: "Linkin Park",
		Album: "Meteora (Bonus Edition)", DurationS: 188, WebURL: "https://music.youtube.com/watch?v=5qZQEq_C3vc",
		// its "3.8B plays" is a count, never an artist
		Stats: providers.Stats{Plays: 3800000000},
	}
	first := got[0]
	art := first.ArtworkURL
	first.ArtworkURL = ""
	if first != want {
		t.Fatalf("first %+v, want %+v", first, want)
	}
	if !strings.HasPrefix(art, "https://") || !strings.HasSuffix(art, "=w544-h544") {
		t.Fatalf("artwork %q", art)
	}
	if got[1].Title != "In the End" || got[1].DurationS != 217 {
		t.Fatalf("second %+v", got[1])
	}
	// a song by two artists names both
	var two bool
	for _, s := range got {
		two = two || s.Artist == "Sinny, 7vvch"
	}
	if !two {
		t.Fatal("no row joins its two artists")
	}
}

func TestParseSongsSkipsRowsWithoutVideo(t *testing.T) {
	body := `{"a":[{"musicResponsiveListItemRenderer":{"flexColumns":[
		{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"An artist page"}]}}},
		{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"Artist"}]}}}]}}]}`
	got, ok := ytmusic.ParseSongs([]byte(body))
	if !ok || len(got) != 0 {
		t.Fatalf("ok %v, %+v", ok, got)
	}
	if _, ok := ytmusic.ParseSongs([]byte("<html>")); ok {
		t.Fatal("html read as an answer")
	}
}

func TestSearchSendsTheMusicClientRequest(t *testing.T) {
	body := fixture(t)
	var got struct {
		Context struct {
			Client struct{ ClientName, ClientVersion string }
		}
		Query, Params string
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/youtubei/v1/search" || r.Header.Get("Origin") != "https://music.youtube.com" {
			t.Errorf("%s %s origin %q", r.Method, r.URL.Path, r.Header.Get("Origin"))
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	now := func() time.Time { return time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC) }
	c := ytmusic.New(ytmusic.Config{BaseURL: srv.URL, Now: now}, probetest.Client(srv))
	res, err := c.SearchMusic(context.Background(), "linkin park numb", 2)
	if err != nil || len(res) != 2 {
		t.Fatalf("%v, %d results", err, len(res))
	}
	if got.Context.Client.ClientName != "WEB_REMIX" || got.Context.Client.ClientVersion != "1.20261113.01.00" ||
		got.Query != "linkin park numb" || got.Params == "" {
		t.Fatalf("request %+v", got)
	}
}

func TestSearchFailsWithTheCodeAlone(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"status": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, probetest.Leak)
		},
		"garbage": func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>"+probetest.Leak) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewTLSServer(h)
			defer srv.Close()
			c := ytmusic.New(ytmusic.Config{BaseURL: srv.URL}, probetest.Client(srv))
			_, err := c.SearchMusic(context.Background(), "q", 3)
			if err == nil || err.Error() != providers.CodeMusicFailed {
				t.Fatalf("err %v", err)
			}
		})
	}
}

func TestProbe(t *testing.T) {
	body := fixture(t)
	probetest.HTTP(t, probetest.Upstream{
		Probe: ytmusic.Module.Probe,
		Build: func(base string, client *http.Client) any {
			return ytmusic.New(ytmusic.Config{BaseURL: base}, client)
		},
		OK: func(*testing.T) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }
		},
	})
}
