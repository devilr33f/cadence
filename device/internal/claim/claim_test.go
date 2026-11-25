package claim_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/claim"
	"git.shino.boo/devilreef/cadence/device/internal/config"
	"git.shino.boo/devilreef/cadence/device/internal/enroll"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
)

// fakeServer answers claims from a script: each poll of claim n takes the next state of states[n]
type fakeServer struct {
	mu       sync.Mutex
	states   [][]string
	started  int
	polls    map[string]int
	boards   []string
	enrolled string
	refuse   int
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v2/claim":
		if f.refuse > 0 {
			f.refuse--
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		var req struct{ Board string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.boards = append(f.boards, req.Board)
		n := f.started
		f.started++
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "cdc_" + strconv.Itoa(n), "code": "K7M4Q" + strconv.Itoa(n+2), "url": "https://c.example/claims/X",
			"expiresAt": time.Now().Add(10 * time.Minute), "pollAfterMs": 1000,
		})
	case r.Method == http.MethodGet && len(r.URL.Path) > len("/v2/claim/"):
		id := r.URL.Path[len("/v2/claim/"):]
		n, _ := strconv.Atoi(id[len("cdc_"):])
		if n >= len(f.states) {
			// hold the poll as the server would, until the client gives up
			f.mu.Unlock()
			<-r.Context().Done()
			f.mu.Lock()
			return
		}
		i := f.polls[id]
		f.polls[id]++
		if i >= len(f.states[n]) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if f.states[n][i] == "down" {
			// the server restarting under the poll
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		out := map[string]string{"state": f.states[n][i]}
		if out["state"] == "approved" {
			out["deviceId"], out["token"] = "dev-1", "cdd_token"
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPost && r.URL.Path == "/v2/enroll":
		var req struct{ Code string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Code != "ABCD-EFGH" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		f.enrolled = req.Code
		_ = json.NewEncoder(w).Encode(map[string]string{"deviceId": "dev-2", "token": "cdd_code"})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type shown struct {
	mu     sync.Mutex
	states []*screenv1.ClaimState
}

func (s *shown) show(c *screenv1.ClaimState) {
	s.mu.Lock()
	s.states = append(s.states, c)
	s.mu.Unlock()
}

func (s *shown) phases() []screenv1.ClaimPhase {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]screenv1.ClaimPhase, len(s.states))
	for i, c := range s.states {
		out[i] = c.GetPhase()
	}
	return out
}

func opts(t *testing.T, srv *httptest.Server, sh *shown, codes chan string) claim.Options {
	dir := t.TempDir()
	return claim.Options{
		Options: enroll.Options{
			Server: srv.URL, Board: "sim-tv", ConfigPath: filepath.Join(dir, "config.json"),
			DataDir: filepath.Join(dir, "data"), Client: srv.Client(),
		},
		Show: sh.show, Codes: codes,
		Sleep: func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil },
	}
}

func run(t *testing.T, o claim.Options) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := claim.Run(ctx, o); err != nil {
		t.Fatal(err)
	}
}

func TestClaimApprovedSavesTheConfig(t *testing.T) {
	f := &fakeServer{states: [][]string{{"expired"}, {"declined"}, {"pending", "approved"}}, polls: map[string]int{}, refuse: 1}
	srv := httptest.NewTLSServer(f)
	defer srv.Close()
	sh := &shown{}
	o := opts(t, srv, sh, nil)
	run(t, o)
	cfg, _, err := config.Load(o.ConfigPath)
	if err != nil || cfg.DeviceID != "dev-1" || cfg.DeviceToken != "cdd_token" || cfg.Board != "sim-tv" || cfg.ServerURL != srv.URL {
		t.Fatalf("%+v %v", cfg, err)
	}
	P := screenv1.ClaimPhase_CLAIM_PHASE_PENDING
	want := []screenv1.ClaimPhase{screenv1.ClaimPhase_CLAIM_PHASE_FAILED, P, screenv1.ClaimPhase_CLAIM_PHASE_EXPIRED, P,
		screenv1.ClaimPhase_CLAIM_PHASE_DECLINED, P, screenv1.ClaimPhase_CLAIM_PHASE_APPROVED}
	got := sh.phases()
	if len(got) != len(want) {
		t.Fatalf("phases %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("phases %v", got)
		}
	}
	sh.mu.Lock()
	first := sh.states[1]
	sh.mu.Unlock()
	if first.GetCode() != "K7M4Q2" || first.GetUrl() == "" || first.GetExpiresAtMs() < time.Now().Add(9*time.Minute).UnixMilli() {
		t.Fatalf("pending %+v", first)
	}
	if len(f.boards) != 3 || f.boards[0] != "sim-tv" {
		t.Fatalf("boards %v", f.boards)
	}
}

// a server away under the poll (a restart, the network) leaves the claim open: its code, which the owner may be typing,
// is polled again until it is approved, and no fresh claim is opened (each would burn the server's claim rate)
func TestClaimOutlivesAServerAwayUnderItsPoll(t *testing.T) {
	f := &fakeServer{states: [][]string{{"pending", "down", "down", "approved"}}, polls: map[string]int{}}
	srv := httptest.NewTLSServer(f)
	defer srv.Close()
	sh := &shown{}
	o := opts(t, srv, sh, nil)
	run(t, o)
	if cfg, _, err := config.Load(o.ConfigPath); err != nil || cfg.DeviceID != "dev-1" {
		t.Fatalf("%+v %v", cfg, err)
	}
	f.mu.Lock()
	started, polls := f.started, f.polls["cdc_0"]
	f.mu.Unlock()
	if started != 1 || polls != 4 {
		t.Fatalf("started %d claims, polled the first %d times", started, polls)
	}
	got := sh.phases()
	if len(got) != 2 || got[0] != screenv1.ClaimPhase_CLAIM_PHASE_PENDING || got[1] != screenv1.ClaimPhase_CLAIM_PHASE_APPROVED {
		t.Fatalf("phases %v", got)
	}
}

func TestClaimEnrolsByTypedCode(t *testing.T) {
	f := &fakeServer{polls: map[string]int{}}
	srv := httptest.NewTLSServer(f)
	defer srv.Close()
	sh := &shown{}
	codes := make(chan string, 2)
	codes <- "WRONG-CODE"
	codes <- "ABCD-EFGH"
	o := opts(t, srv, sh, codes)
	run(t, o)
	cfg, _, err := config.Load(o.ConfigPath)
	if err != nil || cfg.DeviceID != "dev-2" || cfg.DeviceToken != "cdd_code" {
		t.Fatalf("%+v %v", cfg, err)
	}
	got := sh.phases()
	if got[len(got)-1] != screenv1.ClaimPhase_CLAIM_PHASE_APPROVED {
		t.Fatalf("phases %v", got)
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	refused := false
	for _, s := range sh.states {
		refused = refused || s.GetErrorCode() == "code_refused"
	}
	if !refused {
		t.Fatal("the wrong code was not shown refused")
	}
}

func TestClaimStopsWithItsContext(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	sh := &shown{}
	o := opts(t, srv, sh, nil)
	o.Sleep = nil
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := claim.Run(ctx, o); err == nil {
		t.Fatal("ran past its context")
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if len(sh.states) == 0 || sh.states[0].GetErrorCode() != "status_503" {
		t.Fatalf("%+v", sh.states)
	}
}

func TestClaimRefusesAConfigItCouldNotSave(t *testing.T) {
	var asked bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { asked = true }))
	defer srv.Close()
	o := opts(t, srv, &shown{}, nil)
	o.Server, o.Board = "http://server:8080", "rpi"
	if err := claim.Run(context.Background(), o); !errors.Is(err, enroll.ErrInvalid) || asked {
		t.Fatalf("%v, asked %v", err, asked)
	}
}

// a server address saved on the screen drops the open claim and claims on the new server, whose url the config
// keeps; one Save would refuse is shown failed and the claim goes on
func TestClaimMovesToAServerAddressFromTheScreen(t *testing.T) {
	first := &fakeServer{polls: map[string]int{}}
	srv1 := httptest.NewTLSServer(first)
	defer srv1.Close()
	second := &fakeServer{states: [][]string{{"pending", "approved"}}, polls: map[string]int{}}
	srv2 := httptest.NewTLSServer(second)
	defer srv2.Close()
	sh := &shown{}
	servers := make(chan string, 2)
	o := opts(t, srv1, sh, nil)
	// both test servers share one CA: a client of either trusts both
	o.Client = srv2.Client()
	o.Servers = servers
	var moved []string
	o.ServerChanged = func(u string) { moved = append(moved, u) }
	servers <- "ftp://not-a-server.example"
	servers <- srv2.URL
	run(t, o)
	cfg, _, err := config.Load(o.ConfigPath)
	if err != nil || cfg.ServerURL != srv2.URL || cfg.DeviceID != "dev-1" {
		t.Fatalf("%+v %v", cfg, err)
	}
	// the screen hears the address it moved to, never the refused one
	if len(moved) != 1 || moved[0] != srv2.URL {
		t.Fatalf("moved %q", moved)
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	refused := false
	for _, s := range sh.states {
		refused = refused || s.GetErrorCode() == "server_url"
	}
	if !refused {
		t.Fatal("the refused address was not shown")
	}
}

// the same address again while a failed claim backs off is a retry: the next claim starts at once
func TestClaimRetriesAtOnceOnTheSameAddress(t *testing.T) {
	f := &fakeServer{states: [][]string{{"pending", "approved"}}, polls: map[string]int{}, refuse: 1}
	srv := httptest.NewTLSServer(f)
	defer srv.Close()
	sh := &shown{}
	servers := make(chan string, 1)
	o := opts(t, srv, sh, nil)
	o.Servers = servers
	// the backoff would outlast the test but for the retry
	o.Sleep = func(ctx context.Context, _ time.Duration) bool {
		select {
		case servers <- srv.URL:
		default:
		}
		<-ctx.Done()
		return false
	}
	run(t, o)
	if cfg, _, err := config.Load(o.ConfigPath); err != nil || cfg.DeviceID != "dev-1" {
		t.Fatalf("%+v %v", cfg, err)
	}
}
