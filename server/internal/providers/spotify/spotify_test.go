package spotify_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
	"git.shino.boo/devilreef/cadence/server/internal/providers/spotify"
)

const answer = `{"tracks":{"items":[
	{"id":"2nLtzopw4rPReszdYBJU6h","name":"Numb","duration_ms":185586,
	 "artists":[{"name":"Linkin Park"}],
	 "album":{"name":"Meteora","images":[{"url":"https://i.scdn.co/image/large"},{"url":"https://i.scdn.co/image/small"}]}},
	{"id":"x","name":"Numb / Encore","duration_ms":205000,
	 "artists":[{"name":"JAY-Z"},{"name":"Linkin Park"}],"album":{"name":"Collision Course","images":[]}},
	{"id":"","name":"no id"}]}}`

type fake struct {
	tokens, searches atomic.Int32
	status           int
}

func (f *fake) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/token":
			f.tokens.Add(1)
			id, secret, ok := r.BasicAuth()
			_ = r.ParseForm()
			if r.Method != http.MethodPost || !ok || id != "id" || secret != probetest.Key || r.PostForm.Get("grant_type") != "client_credentials" {
				t.Errorf("token request %s basic %v %q", r.Method, ok, id)
			}
			_, _ = io.WriteString(w, `{"access_token":"app-token","token_type":"Bearer","expires_in":3600}`)
		case "/v1/search":
			f.searches.Add(1)
			q := r.URL.Query()
			if r.Header.Get("Authorization") != "Bearer app-token" || q.Get("type") != "track" || q.Get("q") != "numb" || q.Get("market") != "DE" {
				t.Errorf("search %s auth %q", r.URL, r.Header.Get("Authorization"))
			}
			if f.status != 0 {
				w.WriteHeader(f.status)
				_, _ = io.WriteString(w, probetest.Leak)
				return
			}
			_, _ = io.WriteString(w, answer)
		default:
			t.Errorf("path %s", r.URL.Path)
		}
	}
}

func client(srv *httptest.Server) *spotify.Client {
	return spotify.New(spotify.Config{
		AccountsURL: srv.URL, APIURL: srv.URL, ClientID: "id", ClientSecret: probetest.Key, Market: "DE",
	}, probetest.Client(srv))
}

func TestSearchReadsTracksAndKeepsTheToken(t *testing.T) {
	f := &fake{}
	srv := httptest.NewTLSServer(f.handler(t))
	defer srv.Close()
	c := client(srv)
	for range 2 {
		got, err := c.SearchMusic(context.Background(), "numb", 5)
		if err != nil {
			t.Fatal(err)
		}
		want := []providers.Track{
			{Source: "spotify", ID: "2nLtzopw4rPReszdYBJU6h", Title: "Numb", Artist: "Linkin Park", Album: "Meteora", DurationS: 186, ArtworkURL: "https://i.scdn.co/image/large", WebURL: "https://open.spotify.com/track/2nLtzopw4rPReszdYBJU6h"},
			{Source: "spotify", ID: "x", Title: "Numb / Encore", Artist: "JAY-Z, Linkin Park", Album: "Collision Course", DurationS: 205, WebURL: "https://open.spotify.com/track/x"},
		}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("got %+v", got)
		}
	}
	if f.tokens.Load() != 1 || f.searches.Load() != 2 {
		t.Fatalf("%d tokens, %d searches", f.tokens.Load(), f.searches.Load())
	}
}

func TestSearchFailsWithTheCodeAndDropsARefusedToken(t *testing.T) {
	f := &fake{status: http.StatusUnauthorized}
	srv := httptest.NewTLSServer(f.handler(t))
	defer srv.Close()
	c := client(srv)
	for range 2 {
		_, err := c.SearchMusic(context.Background(), "numb", 5)
		if err == nil || err.Error() != providers.CodeMusicFailed || strings.Contains(err.Error(), probetest.Leak) {
			t.Fatalf("err %v", err)
		}
	}
	if f.tokens.Load() != 2 {
		t.Fatalf("%d tokens: a refused one was kept", f.tokens.Load())
	}
}

func TestProbe(t *testing.T) {
	probetest.HTTP(t, probetest.Upstream{
		Probe: spotify.Module.Probe,
		Build: func(base string, hc *http.Client) any {
			return spotify.New(spotify.Config{AccountsURL: base, APIURL: base, ClientID: "id", ClientSecret: probetest.Key}, hc)
		},
		OK: func(t *testing.T) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/token" {
					t.Errorf("probe searched: %s", r.URL.Path)
				}
				_, _ = io.WriteString(w, `{"access_token":"app-token","expires_in":3600}`)
			}
		},
	})
}

// a wrong client id or secret is answered 400 invalid_client
func TestProbeWrongCredentialIsAuth(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_client","error_description":"`+probetest.Leak+`"}`)
	}))
	defer srv.Close()
	c := spotify.New(spotify.Config{AccountsURL: srv.URL, APIURL: srv.URL, ClientID: "id", ClientSecret: probetest.Key}, probetest.Client(srv))
	probetest.Want(t, spotify.Module.Probe(context.Background(), c), "auth")
}

func TestModuleNeedsBothSecrets(t *testing.T) {
	if _, err := spotify.Module.New(nil, map[string]string{"clientId": "id"}); err == nil {
		t.Fatal("built without a secret")
	}
	if _, err := spotify.Module.New([]byte(`{"market":"DE"}`), map[string]string{"clientId": "id", "clientSecret": "s"}); err != nil {
		t.Fatal(err)
	}
}
