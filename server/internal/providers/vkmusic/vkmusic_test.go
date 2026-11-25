package vkmusic_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
	"git.shino.boo/devilreef/cadence/server/internal/providers/vkmusic"
)

const ua = "VKAndroidApp/test"

const searchAnswer = `{"response":{"count":3,"items":[
	{"id":456239017,"owner_id":-2001,"access_key":"ab12cd","artist":"Linkin Park","title":"Numb","duration":186,
	 "url":"https://cs1-2v4.vkuseraudio.net/s/v1/ac/x/index.m3u8?extra=y",
	 "album":{"id":1,"title":"Meteora","owner_id":-2001,"access_key":"k","thumb":{"photo_300":"https://sun.userapi.com/300","photo_600":"https://sun.userapi.com/600"}}},
	{"id":2,"owner_id":5,"artist":"Blocked","title":"Gone","duration":100,"url":""},
	{"id":3,"owner_id":5,"artist":"Linkin Park","title":"In the End","duration":216,"url":"https://psv4.vkuseraudio.net/c/a.mp3?extra=z"}]}}`

// fake answers each method from its map and checks the token, version and
// user agent on every call
type fake struct {
	mu      sync.Mutex
	answers map[string]string
	calls   []string
	queries map[string]map[string]string
}

func (f *fake) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Header.Get("User-Agent") != ua || q.Get("access_token") != probetest.Key || q.Get("v") != "5.131" || q.Get("https") != "1" {
			t.Errorf("call %s without the token, version or user agent", r.URL.Path)
		}
		m := strings.TrimPrefix(r.URL.Path, "/method/")
		f.mu.Lock()
		f.calls = append(f.calls, m)
		if f.queries == nil {
			f.queries = map[string]map[string]string{}
		}
		f.queries[m] = map[string]string{}
		for k := range q {
			f.queries[m][k] = q.Get(k)
		}
		// a paged method's answer is keyed by its offset too
		a, ok := f.answers[m+"@"+q.Get("offset")]
		if !ok {
			a, ok = f.answers[m]
		}
		f.mu.Unlock()
		if !ok {
			t.Errorf("method %s", m)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, a)
	}
}

func newClient(t *testing.T, answers map[string]string) (*vkmusic.Client, *fake) {
	f := &fake{answers: answers}
	srv := httptest.NewTLSServer(f.handler(t))
	t.Cleanup(srv.Close)
	return build(srv.URL, probetest.Client(srv)), f
}

func build(base string, hc *http.Client) *vkmusic.Client {
	return vkmusic.New(vkmusic.Config{APIURL: base + "/method", Token: probetest.Key, UserAgent: ua, Interval: -1}, hc)
}

func vkError(code int) string {
	return `{"error":{"error_code":` + strconv.Itoa(code) + `,"error_msg":"` + probetest.Leak + `"}}`
}

func TestSearchSkipsBlockedSongs(t *testing.T) {
	c, f := newClient(t, map[string]string{"audio.search": searchAnswer})
	got, err := c.SearchMusic(context.Background(), "numb", 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []providers.Track{
		{Source: "vk", ID: "-2001_456239017_ab12cd", Title: "Numb", Artist: "Linkin Park", Album: "Meteora", DurationS: 186,
			ArtworkURL: "https://sun.userapi.com/600", WebURL: "https://vk.com/audio-2001_456239017"},
		{Source: "vk", ID: "5_3", Title: "In the End", Artist: "Linkin Park", DurationS: 216, WebURL: "https://vk.com/audio5_3"},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v", got)
	}
	if q := f.queries["audio.search"]; q["q"] != "numb" || q["sort"] != "0" || q["autocomplete"] != "1" || q["lang"] != "ru" {
		t.Fatalf("query %v", q)
	}
}

func TestSearchFailsWithTheCodeAlone(t *testing.T) {
	for _, code := range []int{5, 6, 14, 15} {
		c, _ := newClient(t, map[string]string{"audio.search": vkError(code)})
		_, err := c.SearchMusic(context.Background(), "numb", 5)
		if err == nil || err.Error() != providers.CodeMusicFailed {
			t.Fatalf("error %d: %v", code, err)
		}
	}
}

func TestStreamReadsAFreshURL(t *testing.T) {
	c, f := newClient(t, map[string]string{"audio.getById": `{"response":[{"id":456239017,"owner_id":-2001,"title":"Numb","duration":186,
		"url":"https://cs9-9v4.vkuseraudio.net/s/v1/ac/fresh/index.m3u8?extra=q"}]}`})
	s, err := c.StreamTrack(context.Background(), providers.Track{Source: "vk", ID: "-2001_456239017_ab12cd"})
	if err != nil {
		t.Fatal(err)
	}
	if s.URL != "https://cs9-9v4.vkuseraudio.net/s/v1/ac/fresh/index.m3u8?extra=q" || !s.HLS || s.DurationS != 186 {
		t.Fatalf("stream %+v", s)
	}
	if f.queries["audio.getById"]["audios"] != "-2001_456239017_ab12cd" {
		t.Fatalf("audios %q", f.queries["audio.getById"]["audios"])
	}
}

func TestStreamOfAnMP3IsNotHLS(t *testing.T) {
	c, _ := newClient(t, map[string]string{"audio.getById": `{"response":[{"id":3,"owner_id":5,"title":"x","duration":10,"url":"https://psv4.vkuseraudio.net/c/a.mp3?extra=z"}]}`})
	s, err := c.StreamTrack(context.Background(), providers.Track{Source: "vk", ID: "5_3"})
	if err != nil || s.HLS {
		t.Fatalf("stream %+v %v", s, err)
	}
}

func TestStreamErrors(t *testing.T) {
	cases := []struct {
		name, answer string
		want         error
	}{
		{"bad token", vkError(5), providers.ErrSignInRequired},
		{"access denied", vkError(15), providers.ErrSignInRequired},
		{"rate limited", vkError(6), errors.New(providers.CodePlayerFailed)},
		{"captcha", vkError(14), errors.New(providers.CodePlayerFailed)},
		{"blocked", `{"response":[{"id":3,"owner_id":5,"title":"x","url":""}]}`, providers.ErrUnplayable},
		{"gone", `{"response":[]}`, providers.ErrUnplayable},
		{"no audio access", `{"response":[{"id":3,"owner_id":5,"title":"x","url":"https://vk.com/mp3/audio_api_unavailable.mp3"}]}`, providers.ErrSignInRequired},
		{"garbage", `<html>` + probetest.Leak, errors.New(providers.CodePlayerFailed)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newClient(t, map[string]string{"audio.getById": tc.answer})
			_, err := c.StreamTrack(context.Background(), providers.Track{Source: "vk", ID: "5_3"})
			if err == nil || err.Error() != tc.want.Error() || strings.Contains(err.Error(), probetest.Leak) {
				t.Fatalf("error %v, want %v", err, tc.want)
			}
		})
	}
}

func TestStreamRefusesForeignIDs(t *testing.T) {
	c, f := newClient(t, map[string]string{})
	for _, tr := range []providers.Track{{Source: "vk", ID: "5_3,6_4"}, {Source: "vk", ID: "x"}, {Source: "spotify", ID: "5_3"}} {
		if _, err := c.StreamTrack(context.Background(), tr); !errors.Is(err, providers.ErrUnplayable) {
			t.Fatalf("%q: %v", tr.ID, err)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestRadioLeavesTheSeedOut(t *testing.T) {
	c, f := newClient(t, map[string]string{"audio.getRecommendations": `{"response":{"items":[
		{"id":456239017,"owner_id":-2001,"access_key":"zz","title":"Numb","artist":"LP","url":"https://a.vkuseraudio.net/x.mp3"},
		{"id":7,"owner_id":8,"title":"Faint","artist":"LP","url":"https://a.vkuseraudio.net/y.mp3"}]}}`})
	got, err := c.TrackRadio(context.Background(), providers.Track{Source: "vk", ID: "-2001_456239017_ab12cd"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "8_7" {
		t.Fatalf("got %+v", got)
	}
	if f.queries["audio.getRecommendations"]["target_audio"] != "-2001_456239017" {
		t.Fatalf("query %v", f.queries["audio.getRecommendations"])
	}
}

func TestProbe(t *testing.T) {
	probetest.HTTP(t, probetest.Upstream{
		Probe: vkmusic.Module.Probe,
		Build: func(base string, hc *http.Client) any { return build(base, hc) },
		OK: func(t *testing.T) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/method/audio.search" || r.Header.Get("User-Agent") != ua {
					t.Errorf("probe %s", r.URL.Path)
				}
				_, _ = io.WriteString(w, `{"response":{"count":0,"items":[]}}`)
			}
		},
	})
}

func TestProbeMapsVKErrors(t *testing.T) {
	for code, want := range map[int]string{5: "auth", 15: "auth", 6: "rate_limited", 29: "rate_limited", 14: "bad_response"} {
		c, _ := newClient(t, map[string]string{"audio.search": vkError(code)})
		probetest.Want(t, vkmusic.Module.Probe(context.Background(), c), want)
	}
}

func TestModuleNeedsTheToken(t *testing.T) {
	if _, err := vkmusic.Module.New(nil, map[string]string{}); err == nil {
		t.Fatal("built without a token")
	}
	if _, err := vkmusic.Module.New([]byte(`{"userAgent":"VKAndroidApp/1"}`), map[string]string{"token": "t"}); err != nil {
		t.Fatal(err)
	}
}
