package yandexmusic

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
)

const searchTracks = `{"invocationInfo":{},"result":{"tracks":{"results":[
	{"id":178529,"title":"Numb","durationMs":187520,"available":true,
	 "coverUri":"avatars.yandex.net/get-music-content/142001/a92a262f.a.21940-7/%%",
	 "artists":[{"id":36800,"name":"Linkin Park"}],"albums":[{"id":21940,"title":"Meteora"}]},
	{"id":"43117","title":"Sonne","version":"Remastered","durationMs":272000,
	 "artists":[{"name":"Rammstein"},{"name":"Guest"}],"albums":[]},
	{"id":"9","title":"Gone","available":false},
	{"id":"uuid-not-a-number","title":"User upload"}]}}}`

func newClient(srv *httptest.Server, c Config) *Client {
	c.APIURL, c.PassportURL, c.OAuthURL = srv.URL, srv.URL, srv.URL
	cl := New(c, probetest.Client(srv))
	u, _ := url.Parse(srv.URL)
	cl.testHost = u.Hostname()
	return cl
}

func TestSearchReadsTracks(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/search" || q.Get("text") != "numb" || q.Get("type") != "track" ||
			r.Header.Get("X-Yandex-Music-Client") != musicClient || r.Header.Get("Authorization") != "" {
			t.Errorf("search %s", r.URL)
		}
		_, _ = io.WriteString(w, searchTracks)
	}))
	defer srv.Close()
	got, err := newClient(srv, Config{Token: probetest.Key}).SearchMusic(context.Background(), "numb", 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []providers.Track{
		{Source: "yandex", ID: "178529", Title: "Numb", Artist: "Linkin Park", Album: "Meteora", DurationS: 188,
			ArtworkURL: "https://avatars.yandex.net/get-music-content/142001/a92a262f.a.21940-7/400x400",
			WebURL:     "https://music.yandex.ru/album/21940/track/178529"},
		{Source: "yandex", ID: "43117", Title: "Sonne (Remastered)", Artist: "Rammstein, Guest", DurationS: 272,
			WebURL: "https://music.yandex.ru/track/43117"},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v", got)
	}
}

func TestSearchFailureCarriesTheCodeAlone(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, probetest.Leak)
	}))
	defer srv.Close()
	_, err := newClient(srv, Config{}).SearchMusic(context.Background(), "numb", 5)
	if err == nil || err.Error() != providers.CodeMusicFailed {
		t.Fatalf("err %v", err)
	}
}

func TestSignMatchesAnIndependentVector(t *testing.T) {
	// computed with python's hmac over "1700000000178529hqaac-mp4aacmp3he-aache-aac-mp4raw"
	if got := sign("1700000000", "178529", "hq", codecs["hq"]); got != "Lj6pBNQwU1jMf1vMs7Is1Re5Ihc/qib0lPfd8BSWRT0" {
		t.Fatalf("sign %q", got)
	}
	if got := mp3Sign("/rmusic/U2FsdGVk/abc", "sss"); got != "a14b74391f0b1d284f9ec138a4f51223" {
		t.Fatalf("mp3 sign %q", got)
	}
}

func TestStreamWithoutAnAccountAsksNothing(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	_, err := newClient(srv, Config{}).StreamTrack(context.Background(), providers.Track{Source: "yandex", ID: "178529"})
	if !errors.Is(err, providers.ErrSignInRequired) || hits.Load() != 0 {
		t.Fatalf("err %v hits %d", err, hits.Load())
	}
}

func TestStreamRefusesForeignTracks(t *testing.T) {
	c := New(Config{Token: probetest.Key}, http.DefaultClient)
	for _, tr := range []providers.Track{{Source: "spotify", ID: "1"}, {Source: "yandex", ID: "1/../2"}} {
		if _, err := c.StreamTrack(context.Background(), tr); !errors.Is(err, providers.ErrUnplayable) {
			t.Fatalf("%+v: %v", tr, err)
		}
	}
}

// fileInfo answers get-file-info with body, checking the request's signature and token
func fileInfo(t *testing.T, srv **httptest.Server, status int, info string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/get-file-info" {
			t.Errorf("path %s", r.URL.Path)
			return
		}
		q := r.URL.Query()
		if q.Get("sign") != sign(q.Get("ts"), q.Get("trackId"), q.Get("quality"), q.Get("codecs")) ||
			q.Get("trackId") != "178529" || q.Get("quality") != "hq" || q.Get("transports") != "raw" ||
			r.Header.Get("Authorization") != "OAuth "+probetest.Key {
			t.Errorf("file info %s", r.URL)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, strings.ReplaceAll(info, "BASE", (*srv).URL))
	}
}

func TestStreamAnswers(t *testing.T) {
	cases := []struct {
		name   string
		status int
		info   string
		want   error
		url    string
	}{
		{"whole", 200, `{"result":{"downloadInfo":{"quality":"hq","codec":"aac","transport":"raw","urls":["http://plain/x","BASE/song.m4a"]}}}`, nil, "/song.m4a"},
		{"preview", 200, `{"result":{"downloadInfo":{"quality":"preview","codec":"mp3","transport":"raw","url":"BASE/preview.mp3"}}}`, providers.ErrSignInRequired, ""},
		{"encrypted", 200, `{"result":{"downloadInfo":{"quality":"lossless","codec":"flac","transport":"encraw","key":"00","url":"BASE/x"}}}`, providers.ErrUnplayable, ""},
		{"unauthorized", 401, `{"error":"` + probetest.Leak + `"}`, providers.ErrSignInRequired, ""},
		{"forbidden", 403, `{"result":{"name":"track-download-info-error","message":"not-allowed"}}`, providers.ErrSignInRequired, ""},
		{"gone", 404, probetest.Leak, providers.ErrUnplayable, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var srv *httptest.Server
			srv = httptest.NewTLSServer(fileInfo(t, &srv, tc.status, tc.info))
			defer srv.Close()
			c := newClient(srv, Config{Token: probetest.Key, Now: func() time.Time { return time.Unix(1700000000, 0) }})
			got, err := c.StreamTrack(context.Background(), providers.Track{Source: "yandex", ID: "178529", DurationS: 188})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err %v, want %v", err, tc.want)
			}
			if tc.url != "" && (got.URL != srv.URL+tc.url || got.HLS || got.DurationS != 188) {
				t.Fatalf("stream %+v", got)
			}
		})
	}
}

func TestStreamFallsBackToTheLegacyRoute(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "OAuth "+probetest.Key {
			t.Errorf("auth on %s", r.URL.Path)
		}
		host := strings.TrimPrefix(srv.URL, "https://")
		switch r.URL.Path {
		case "/get-file-info":
			w.WriteHeader(http.StatusInternalServerError)
		case "/tracks/178529/download-info":
			_, _ = io.WriteString(w, `{"result":[
				{"codec":"mp3","bitrateInKbps":128,"downloadInfoUrl":"`+srv.URL+`/info128"},
				{"codec":"mp3","bitrateInKbps":320,"downloadInfoUrl":"`+srv.URL+`/info320"},
				{"codec":"aac","bitrateInKbps":192,"downloadInfoUrl":"`+srv.URL+`/aac"}]}`)
		case "/info320":
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><download-info><host>`+host+
				`</host><path>/rmusic/U2FsdGVk/abc</path><ts>0005</ts><region>-1</region><s>sss</s></download-info>`)
		default:
			t.Errorf("path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	got, err := newClient(srv, Config{Token: probetest.Key}).StreamTrack(context.Background(), providers.Track{Source: "yandex", ID: "178529"})
	if err != nil || got.URL != srv.URL+"/get-mp3/a14b74391f0b1d284f9ec138a4f51223/0005/rmusic/U2FsdGVk/abc" {
		t.Fatalf("stream %+v err %v", got, err)
	}
}

func TestStreamFailureCarriesTheCodeAlone(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, probetest.Leak)
	}))
	defer srv.Close()
	_, err := newClient(srv, Config{Token: probetest.Key}).StreamTrack(context.Background(), providers.Track{Source: "yandex", ID: "178529"})
	if err == nil || err.Error() != providers.CodePlayerFailed {
		t.Fatalf("err %v", err)
	}
}

func TestCookiesAreExchangedOnceAndAgainOnARefusal(t *testing.T) {
	var sessions, tokens, infos atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/1/bundle/oauth/token_by_sessionid":
			sessions.Add(1)
			if r.Header.Get("Ya-Client-Cookie") != "Session_id=3:abc|def; sessionid2=3:xyz" ||
				r.Header.Get("Ya-Client-Host") != "passport.yandex.ru" || r.PostForm.Get("client_id") != sessionClientID {
				t.Errorf("session exchange %v", r.Header)
			}
			_, _ = io.WriteString(w, `{"status":"ok","access_token":"x-token-1234567890"}`)
		case "/1/token":
			n := tokens.Add(1)
			if r.PostForm.Get("grant_type") != "x-token" || r.PostForm.Get("access_token") != "x-token-1234567890" ||
				r.PostForm.Get("client_id") != musicClientID {
				t.Errorf("token exchange %v", r.PostForm)
			}
			_, _ = io.WriteString(w, `{"access_token":"music-token-000000`+string(rune('0'+n))+`","expires_in":31536000}`)
		case "/get-file-info":
			n := infos.Add(1)
			auth := r.Header.Get("Authorization")
			// the first token is refused at the third call
			if n == 3 && auth == "OAuth music-token-0000001" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"result":{"downloadInfo":{"quality":"hq","transport":"raw","url":"`+srv.URL+`/a.mp3"}}}`)
		default:
			t.Errorf("path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	cookies, ok := ParseCookies("yandexuid=1; Session_id=3:abc|def; sessionid2=3:xyz; other=2")
	if !ok {
		t.Fatal("cookies refused")
	}
	c := newClient(srv, Config{Cookies: cookies})
	for range 3 {
		if _, err := c.StreamTrack(context.Background(), providers.Track{Source: "yandex", ID: "178529"}); err != nil {
			t.Fatal(err)
		}
	}
	if sessions.Load() != 2 || tokens.Load() != 2 || infos.Load() != 4 {
		t.Fatalf("sessions %d tokens %d infos %d", sessions.Load(), tokens.Load(), infos.Load())
	}
}

func TestRefusedCookiesNeedSignIn(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":"error","errors":["sessionid.invalid"]}`)
	}))
	defer srv.Close()
	c := newClient(srv, Config{Cookies: map[string]string{"Session_id": "x"}})
	if _, err := c.StreamTrack(context.Background(), providers.Track{Source: "yandex", ID: "1"}); !errors.Is(err, providers.ErrSignInRequired) {
		t.Fatalf("err %v", err)
	}
	probetest.Want(t, probe(context.Background(), c), "auth")
}

func TestParseCookies(t *testing.T) {
	file := "# Netscape HTTP Cookie File\n" +
		".yandex.ru\tTRUE\t/\tTRUE\t0\tSession_id\t3:netscape\n" +
		".yandex.ru\tTRUE\t/\tTRUE\t0\tsessionid2\t3:two\n" +
		".google.com\tTRUE\t/\tTRUE\t0\tSID\tnope\n"
	got, ok := ParseCookies(file)
	if !ok || len(got) != 2 || got["Session_id"] != "3:netscape" || got["sessionid2"] != "3:two" {
		t.Fatalf("netscape %v", got)
	}
	if _, ok := ParseCookies("yandexuid=1; sessionid2=3:x"); ok {
		t.Fatal("cookies without Session_id taken")
	}
	if got, ok := ParseCookies("Session_id=bad\r\nvalue"); ok {
		t.Fatalf("bad value taken: %v", got)
	}
}

func TestModuleChecksSecretsAndConfig(t *testing.T) {
	cases := []struct {
		cfg string
		sec map[string]string
		ok  bool
	}{
		{`{}`, nil, true},
		{`{"quality":"lossless"}`, map[string]string{"token": "y0_AgAAAAAAbcdefghijklmnop"}, true},
		{`{}`, map[string]string{"token": "bad token\r\n"}, false},
		{`{}`, map[string]string{"cookies": "yandexuid=1"}, false},
		{`{}`, map[string]string{"cookies": "Session_id=3:abc"}, true},
		{`{"quality":"best"}`, nil, false},
	}
	for _, tc := range cases {
		v, err := Module.New([]byte(tc.cfg), tc.sec)
		if (err == nil) != tc.ok {
			t.Fatalf("%s %v: err %v", tc.cfg, tc.sec, err)
		}
		if err != nil && strings.Contains(err.Error(), "bad token\r") {
			t.Fatal("error quotes the secret")
		}
		if tc.ok {
			var _ providers.Music = v.(*Client)
			var _ providers.Collections = v.(*Client)
			var _ providers.Streamer = v.(*Client)
			var _ providers.TrackRadio = v.(*Client)
		}
	}
}

func TestProbe(t *testing.T) {
	probetest.HTTP(t, probetest.Upstream{
		Probe: Module.Probe,
		Build: func(base string, hc *http.Client) any {
			return New(Config{APIURL: base, Token: probetest.Key}, hc)
		},
		OK: func(t *testing.T) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/account/status" || r.Header.Get("Authorization") != "OAuth "+probetest.Key {
					t.Errorf("probe %s", r.URL.Path)
				}
				_, _ = io.WriteString(w, `{"result":{"account":{"uid":1}}}`)
			}
		},
	})
}

func TestProbeWithoutAnAccountSearches(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.Header.Get("Authorization") != "" {
			t.Errorf("probe %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, searchTracks)
	}))
	defer srv.Close()
	probetest.Want(t, probe(context.Background(), newClient(srv, Config{})), "")
}
