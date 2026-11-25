package zvuk_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
	"git.shino.boo/devilreef/cadence/server/internal/providers/zvuk"
)

// fake is zvuk.com: a guest token from its profile, and GraphQL operations
// answered by name
type fake struct {
	t        *testing.T
	profiles atomic.Int32
	// refuse answers this many GraphQL calls 401
	refuse atomic.Int32
	// status answers every GraphQL call with it and a leaky body
	status int
	ops    map[string]func(vars map[string]any) string
	tiny   func(r *http.Request) (int, string)

	mu     sync.Mutex
	tokens []string
	calls  []string
}

func (f *fake) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "https://zvuk.com/" || r.Header.Get("Origin") != "https://zvuk.com" ||
			!strings.Contains(r.Header.Get("User-Agent"), "Chrome") {
			f.t.Errorf("browser headers missing on %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/tiny/profile":
			n := f.profiles.Add(1)
			if r.Header.Get("X-Auth-Token") != "" {
				f.t.Error("profile sent a token")
			}
			_, _ = io.WriteString(w, `{"result":{"token":"guest`+string(rune('0'+n))+`abcdefgh"}}`)
		case "/api/v1/graphql":
			var body struct {
				OperationName string         `json:"operationName"`
				Query         string         `json:"query"`
				Variables     map[string]any `json:"variables"`
			}
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" ||
				json.NewDecoder(r.Body).Decode(&body) != nil || !strings.Contains(body.Query, "query "+body.OperationName) {
				f.t.Errorf("graphql request %s %q", r.Method, body.OperationName)
			}
			f.mu.Lock()
			f.tokens = append(f.tokens, r.Header.Get("X-Auth-Token"))
			f.calls = append(f.calls, body.OperationName)
			f.mu.Unlock()
			if f.refuse.Load() > 0 {
				f.refuse.Add(-1)
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, probetest.Leak)
				return
			}
			if f.status != 0 {
				w.WriteHeader(f.status)
				_, _ = io.WriteString(w, "<html>"+probetest.Leak)
				return
			}
			op, ok := f.ops[body.OperationName]
			if !ok {
				f.t.Errorf("operation %q", body.OperationName)
				return
			}
			_, _ = io.WriteString(w, op(body.Variables))
		default:
			if f.tiny == nil {
				f.t.Errorf("path %s", r.URL.Path)
				return
			}
			code, b := f.tiny(r)
			w.WriteHeader(code)
			_, _ = io.WriteString(w, b)
		}
	}
}

func start(t *testing.T, f *fake, cfg zvuk.Config) *zvuk.Client {
	f.t = t
	srv := httptest.NewTLSServer(f.handler())
	t.Cleanup(srv.Close)
	cfg.BaseURL = srv.URL
	return zvuk.New(cfg, probetest.Client(srv))
}

const searchAnswer = `{"data":{"search":{"tracks":{"items":[
	{"id":"5896627","title":"Кукла колдуна","duration":203,"artists":[{"title":"Король и Шут"}],
	 "release":{"id":"590001","title":"Акустический альбом","image":{"src":"https://cdn-image.zvuk.com/pic?type=release&id=590001&size={size}"}}},
	{"id":"","title":"gone"},
	{"id":7001,"title":"Лесник","duration":190.6,"artists":[{"title":"Король и Шут"},{"title":"Гость"}],
	 "release":{"id":"590002","title":"Будь как дома","image":{"src":"/static/covers/1.jpg"}}}
]}}}}`

func searchOp(t *testing.T) func(map[string]any) string {
	return func(v map[string]any) string {
		if v["query"] != "король" || v["tracks"] != true || v["releases"] != false || v["artists"] != false || v["playlists"] != false {
			t.Errorf("search vars %v", v)
		}
		return searchAnswer
	}
}

func TestSearchFetchesAGuestTokenOnceAndSkipsEmptyItems(t *testing.T) {
	f := &fake{ops: map[string]func(map[string]any) string{"search": searchOp(t)}}
	c := start(t, f, zvuk.Config{})
	for range 2 {
		got, err := c.SearchMusic(context.Background(), "король", 5)
		if err != nil {
			t.Fatal(err)
		}
		want := []providers.Track{
			{Source: "zvuk", ID: "5896627", Title: "Кукла колдуна", Artist: "Король и Шут", Album: "Акустический альбом",
				DurationS: 203, ArtworkURL: "https://cdn-image.zvuk.com/pic?type=release&id=590001&size=600x600",
				WebURL: "https://zvuk.com/track/5896627"},
			{Source: "zvuk", ID: "7001", Title: "Лесник", Artist: "Король и Шут, Гость", Album: "Будь как дома",
				DurationS: 191, ArtworkURL: "https://zvuk.com/static/covers/1.jpg?size=600x600",
				WebURL: "https://zvuk.com/track/7001"},
		}
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("got %+v", got)
		}
	}
	if f.profiles.Load() != 1 || f.tokens[0] != "guest1abcdefgh" || f.tokens[1] != "guest1abcdefgh" {
		t.Fatalf("%d profiles, tokens %v", f.profiles.Load(), f.tokens)
	}
}

func TestARefusedGuestTokenIsFetchedAgainOnce(t *testing.T) {
	f := &fake{ops: map[string]func(map[string]any) string{"search": searchOp(t)}}
	c := start(t, f, zvuk.Config{})
	f.refuse.Store(1)
	if _, err := c.SearchMusic(context.Background(), "король", 5); err != nil {
		t.Fatal(err)
	}
	if f.profiles.Load() != 2 || f.tokens[1] != "guest2abcdefgh" {
		t.Fatalf("%d profiles, tokens %v", f.profiles.Load(), f.tokens)
	}
	f.refuse.Store(5)
	_, err := c.SearchMusic(context.Background(), "король", 5)
	if err == nil || err.Error() != providers.CodeMusicFailed {
		t.Fatalf("err %v", err)
	}
	if f.profiles.Load() != 3 {
		t.Fatalf("%d profiles: retried more than once", f.profiles.Load())
	}
}

func TestAUserTokenIsSentAndNoGuestFetched(t *testing.T) {
	f := &fake{ops: map[string]func(map[string]any) string{"search": searchOp(t)}}
	c := start(t, f, zvuk.Config{Token: probetest.Key})
	f.refuse.Store(1)
	if _, err := c.SearchMusic(context.Background(), "король", 5); err == nil {
		t.Fatal("a refused user token was retried")
	}
	if _, err := c.SearchMusic(context.Background(), "король", 5); err != nil {
		t.Fatal(err)
	}
	if f.profiles.Load() != 0 || f.tokens[1] != probetest.Key {
		t.Fatalf("%d profiles, tokens %v", f.profiles.Load(), f.tokens)
	}
}

func TestFailuresCarryOnlyTheCode(t *testing.T) {
	for name, f := range map[string]*fake{
		"graphql errors with 200": {ops: map[string]func(map[string]any) string{"search": func(map[string]any) string {
			return `{"data":null,"errors":[{"message":"` + probetest.Leak + `"}]}`
		}}},
		"anti-bot 418":     {status: http.StatusTeapot},
		"anti-bot html":    {ops: map[string]func(map[string]any) string{"search": func(map[string]any) string { return "<html>" + probetest.Leak }}},
		"server error":     {status: http.StatusBadGateway},
		"not json at all":  {ops: map[string]func(map[string]any) string{"search": func(map[string]any) string { return "{" + probetest.Leak }}},
		"null data no err": {ops: map[string]func(map[string]any) string{"search": func(map[string]any) string { return `{"data":null}` }}},
	} {
		t.Run(name, func(t *testing.T) {
			c := start(t, f, zvuk.Config{})
			_, err := c.SearchMusic(context.Background(), "король", 5)
			if err == nil || err.Error() != providers.CodeMusicFailed || strings.Contains(err.Error(), probetest.Leak) {
				t.Fatalf("err %v", err)
			}
		})
	}
}

func streamFake(t *testing.T, stream string) *fake {
	return &fake{ops: map[string]func(map[string]any) string{"getStream": func(v map[string]any) string {
		if ids, _ := v["ids"].([]any); len(ids) != 1 || ids[0] != "5896627" {
			t.Errorf("stream ids %v", v["ids"])
		}
		return `{"data":{"mediaContents":[{"__typename":"Track","stream":` + stream + `}]}}`
	}}}
}

var seed = providers.Track{Source: "zvuk", ID: "5896627", DurationS: 203}

func TestStreamPicksTheConfiguredQualityOrTheBestBelowIt(t *testing.T) {
	all := `{"expire":"x","expireDelta":86400,"flac":"https://cdn66.zvuk.com/f","flacdrm":"https://cdn66.zvuk.com/drm","high":"https://cdn66.zvuk.com/h","mid":"https://cdn66.zvuk.com/m"}`
	noHigh := `{"flac":null,"flacdrm":"https://cdn66.zvuk.com/drm","high":null,"mid":"https://cdn66.zvuk.com/m"}`
	for _, c := range []struct {
		name, quality, stream, want string
	}{
		{"default high", "", all, "https://cdn66.zvuk.com/h"},
		{"high falls back to mid", "high", noHigh, "https://cdn66.zvuk.com/m"},
		{"flac when asked", "flac", all, "https://cdn66.zvuk.com/f"},
		{"never flacdrm", "flac", noHigh, "https://cdn66.zvuk.com/m"},
		{"mid when asked", "mid", all, "https://cdn66.zvuk.com/m"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cl := start(t, streamFake(t, c.stream), zvuk.Config{Quality: c.quality})
			got, err := cl.StreamTrack(context.Background(), seed)
			if err != nil || got != (providers.Stream{URL: c.want, DurationS: 203}) {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}

func TestStreamFallsBackToTheRESTRouteThenUnplayable(t *testing.T) {
	f := streamFake(t, `null`)
	var asked []string
	f.tiny = func(r *http.Request) (int, string) {
		if r.URL.Path != "/api/tiny/track/stream" || r.URL.Query().Get("id") != "5896627" || r.Header.Get("X-Auth-Token") == "" {
			t.Errorf("rest %s", r.URL)
		}
		asked = append(asked, r.URL.Query().Get("quality"))
		if r.URL.Query().Get("quality") == "mid" {
			return 200, `{"result":{"stream":"https://cdn66.zvuk.com/rest"}}`
		}
		return http.StatusTeapot, "<html>" + probetest.Leak
	}
	c := start(t, f, zvuk.Config{})
	got, err := c.StreamTrack(context.Background(), seed)
	if err != nil || got.URL != "https://cdn66.zvuk.com/rest" || strings.Join(asked, ",") != "high,mid" {
		t.Fatalf("got %+v, %v, asked %v", got, err, asked)
	}
	f.tiny = func(*http.Request) (int, string) { return http.StatusTeapot, probetest.Leak }
	if _, err := c.StreamTrack(context.Background(), seed); !errors.Is(err, providers.ErrUnplayable) {
		t.Fatalf("err %v", err)
	}
}

func TestStreamFailures(t *testing.T) {
	refused := streamFake(t, `null`)
	refused.refuse.Store(10)
	c := start(t, refused, zvuk.Config{})
	if _, err := c.StreamTrack(context.Background(), seed); !errors.Is(err, providers.ErrSignInRequired) {
		t.Fatalf("refused: %v", err)
	}
	broken := &fake{status: http.StatusTeapot, tiny: func(*http.Request) (int, string) { return http.StatusTeapot, probetest.Leak }}
	c = start(t, broken, zvuk.Config{})
	_, err := c.StreamTrack(context.Background(), seed)
	if err == nil || err.Error() != providers.CodePlayerFailed {
		t.Fatalf("blocked: %v", err)
	}
	if _, err := c.StreamTrack(context.Background(), providers.Track{Source: "zvuk", ID: "../x"}); !errors.Is(err, providers.ErrUnplayable) {
		t.Fatalf("bad id: %v", err)
	}
}

func TestProbe(t *testing.T) {
	probetest.HTTP(t, probetest.Upstream{
		Probe: zvuk.Module.Probe,
		Build: func(base string, hc *http.Client) any {
			return zvuk.New(zvuk.Config{BaseURL: base, Token: probetest.Key}, hc)
		},
		OK: func(t *testing.T) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/graphql" || r.Header.Get("X-Auth-Token") != probetest.Key {
					t.Errorf("probe %s", r.URL.Path)
				}
				_, _ = io.WriteString(w, `{"data":{"search":{"tracks":{"items":[]}}}}`)
			}
		},
	})
}

func TestProbeAntiBotIsBadResponse(t *testing.T) {
	for _, status := range []int{http.StatusTeapot, http.StatusOK} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "<html>"+probetest.Leak)
		}))
		c := zvuk.New(zvuk.Config{BaseURL: srv.URL}, probetest.Client(srv))
		probetest.Want(t, zvuk.Module.Probe(context.Background(), c), "bad_response")
		srv.Close()
	}
}

func TestModule(t *testing.T) {
	if _, err := zvuk.Module.New(nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := zvuk.Module.New([]byte(`{"quality":"flac"}`), map[string]string{"token": "abcdef0123456789"}); err != nil {
		t.Fatal(err)
	}
	if _, err := zvuk.Module.New([]byte(`{"quality":"flacdrm"}`), nil); err == nil {
		t.Fatal("took flacdrm")
	}
	if _, err := zvuk.Module.New(nil, map[string]string{"token": "a b\r\nX: y"}); err == nil {
		t.Fatal("took a bad token")
	}
}
