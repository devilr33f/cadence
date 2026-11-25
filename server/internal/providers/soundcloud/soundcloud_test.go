package soundcloud_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
	"git.shino.boo/devilreef/cadence/server/internal/providers/soundcloud"
)

const (
	siteID  = "AbCdEfGhIjKlMnOpQrStUvWxYz012345"
	freshID = "ZyXwVuTsRqPoNmLkJiHgFeDcBa543210"
)

// fake is soundcloud.com and api-v2 on one server: the site names its
// scripts, the last of which holds the client id; the api answers searches,
// a track's transcodings and a transcoding's signed url
type fake struct {
	srv       *httptest.Server
	id        atomic.Value // the client id the api takes
	pages     atomic.Int32
	snipped   bool
	blocked   bool
	apiStatus int
}

func newFake(t *testing.T) *fake {
	f := &fake{}
	f.id.Store(siteID)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := f.srv.URL
		switch r.URL.Path {
		case "/":
			f.pages.Add(1)
			_, _ = io.WriteString(w, `<script crossorigin src="`+base+`/assets/0-lib.js"></script><script crossorigin src="`+base+`/assets/1-app.js"></script>`)
			return
		case "/assets/0-lib.js":
			_, _ = io.WriteString(w, `var x=1;`)
			return
		case "/assets/1-app.js":
			_, _ = io.WriteString(w, `({client_id:"`+f.id.Load().(string)+`",env:"production"})`)
			return
		}
		if r.URL.Query().Get("client_id") != f.id.Load().(string) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.apiStatus != 0 {
			w.WriteHeader(f.apiStatus)
			_, _ = io.WriteString(w, probetest.Leak)
			return
		}
		switch r.URL.Path {
		case "/search/tracks":
			_, _ = io.WriteString(w, `{"collection":[
				{"id":1,"kind":"track","title":"Free","duration":200000,"policy":"ALLOW","permalink_url":"https://soundcloud.com/u/free",
				 "artwork_url":"https://i1.sndcdn.com/artworks-1-large.jpg","user":{"username":"Uploader"},
				 "publisher_metadata":{"artist":"Real Artist","album_title":"LP"}},
				{"id":2,"kind":"track","title":"Blocked","policy":"BLOCK","user":{"username":"x"}},
				{"id":3,"kind":"track","title":"","user":{"username":"x"}},
				{"id":4,"kind":"track","title":"Preview","duration":30000,"full_duration":185000,"policy":"SNIP","user":{"username":"Band"}}]}`)
		case "/tracks/1":
			tc := func(proto, mime, preset string, snipped bool) string {
				s := "false"
				if snipped {
					s = "true"
				}
				return `{"url":"` + base + `/media/1/` + preset + `","preset":"` + preset + `","snipped":` + s +
					`,"format":{"protocol":"` + proto + `","mime_type":"` + mime + `"}}`
			}
			policy := "ALLOW"
			if f.blocked {
				policy = "BLOCK"
			}
			_, _ = io.WriteString(w, `{"id":1,"title":"Free","duration":200000,"policy":"`+policy+`","track_authorization":"auth-1",
				"media":{"transcodings":[`+tc("hls", `audio/mpeg`, "mp3_0_0", f.snipped)+`,`+
				tc("hls", `audio/mp4; codecs=\"mp4a.40.2\"`, "aac_160k", f.snipped)+`,`+
				tc("hls", `audio/mp4; codecs=\"mp4a.40.2\"`, "aac_96k", f.snipped)+`]}}`)
		case "/media/1/aac_160k":
			if r.URL.Query().Get("track_authorization") != "auth-1" {
				t.Errorf("transcoding without its authorization")
			}
			_, _ = io.WriteString(w, `{"url":"https://playback.media-streaming.soundcloud.cloud/x/aac_160k/playlist.m3u8?expires=1"}`)
		case "/tracks/1/related":
			_, _ = io.WriteString(w, `{"collection":[{"id":5,"kind":"track","title":"Like it","duration":1000,"user":{"username":"u"}}]}`)
		default:
			t.Errorf("path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) client(c soundcloud.Config) *soundcloud.Client {
	c.SiteURL, c.APIURL = f.srv.URL, f.srv.URL
	return soundcloud.New(c, f.srv.Client())
}

func TestSearchReadsTheSiteClientIDAndKeepsPlayableSongs(t *testing.T) {
	f := newFake(t)
	c := f.client(soundcloud.Config{})
	got, err := c.SearchMusic(context.Background(), "q", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	free, preview := got[0], got[1]
	if free.Source != providers.SourceSoundCloud || free.ID != "1" || free.Artist != "Real Artist" || free.Album != "LP" ||
		free.DurationS != 200 || free.ArtworkURL != "https://i1.sndcdn.com/artworks-1-t500x500.jpg" ||
		free.WebURL != "https://soundcloud.com/u/free" {
		t.Fatalf("free %+v", free)
	}
	// a preview's length is the whole song's
	if preview.DurationS != 185 || preview.Artist != "Band" {
		t.Fatalf("preview %+v", preview)
	}
	if _, err := c.SearchMusic(context.Background(), "q", 5); err != nil || f.pages.Load() != 1 {
		t.Fatalf("second search %v, site read %d times", err, f.pages.Load())
	}
}

func TestARefusedClientIDIsReadAgain(t *testing.T) {
	f := newFake(t)
	c := f.client(soundcloud.Config{})
	if _, err := c.SearchMusic(context.Background(), "q", 1); err != nil {
		t.Fatal(err)
	}
	f.id.Store(freshID)
	if _, err := c.SearchMusic(context.Background(), "q", 1); err != nil {
		t.Fatalf("after rotation: %v", err)
	}
	if f.pages.Load() != 2 {
		t.Fatalf("site read %d times", f.pages.Load())
	}
}

func TestStreamPicksTheBestWholeTranscoding(t *testing.T) {
	f := newFake(t)
	c := f.client(soundcloud.Config{})
	s, err := c.StreamTrack(context.Background(), providers.Track{Source: providers.SourceSoundCloud, ID: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !s.HLS || !strings.Contains(s.URL, "/aac_160k/") || s.DurationS != 200 {
		t.Fatalf("stream %+v", s)
	}
}

func TestStreamRefusals(t *testing.T) {
	f := newFake(t)
	c := f.client(soundcloud.Config{})
	ctx := context.Background()
	if _, err := c.StreamTrack(ctx, providers.Track{Source: providers.SourceSpotify, ID: "1"}); !errors.Is(err, providers.ErrUnplayable) {
		t.Fatalf("another source: %v", err)
	}
	if _, err := c.StreamTrack(ctx, providers.Track{Source: providers.SourceSoundCloud, ID: "../x"}); !errors.Is(err, providers.ErrUnplayable) {
		t.Fatalf("a bad id: %v", err)
	}
	f.snipped = true
	if _, err := c.StreamTrack(ctx, providers.Track{Source: providers.SourceSoundCloud, ID: "1"}); !errors.Is(err, providers.ErrSignInRequired) {
		t.Fatalf("a preview: %v", err)
	}
	f.snipped, f.blocked = false, true
	if _, err := c.StreamTrack(ctx, providers.Track{Source: providers.SourceSoundCloud, ID: "1"}); !errors.Is(err, providers.ErrUnplayable) {
		t.Fatalf("blocked: %v", err)
	}
	f.blocked, f.apiStatus = false, http.StatusInternalServerError
	_, err := c.StreamTrack(ctx, providers.Track{Source: providers.SourceSoundCloud, ID: "1"})
	if err == nil || err.Error() != providers.CodePlayerFailed || strings.Contains(err.Error(), probetest.Leak) {
		t.Fatalf("upstream failure: %v", err)
	}
}

func TestRadioIsTheRelatedSongs(t *testing.T) {
	f := newFake(t)
	got, err := f.client(soundcloud.Config{}).TrackRadio(context.Background(), providers.Track{Source: providers.SourceSoundCloud, ID: "1"}, 10)
	if err != nil || len(got) != 1 || got[0].ID != "5" {
		t.Fatalf("radio %+v %v", got, err)
	}
}

func TestAFixedClientIDSkipsTheSite(t *testing.T) {
	f := newFake(t)
	f.id.Store(freshID)
	if _, err := f.client(soundcloud.Config{ClientID: freshID}).SearchMusic(context.Background(), "q", 1); err != nil {
		t.Fatal(err)
	}
	if f.pages.Load() != 0 {
		t.Fatal("the site was read")
	}
}

func TestFailuresCarryNoUpstreamText(t *testing.T) {
	f := newFake(t)
	f.apiStatus = http.StatusBadGateway
	_, err := f.client(soundcloud.Config{}).SearchMusic(context.Background(), "q", 1)
	if err == nil || err.Error() != providers.CodeMusicFailed {
		t.Fatalf("err %v", err)
	}
}
