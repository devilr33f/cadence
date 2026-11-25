package ha_test

import (
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha/hatest"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
)

func probeClient(t *testing.T, base, token string, roots *x509.CertPool) *ha.Client {
	t.Helper()
	c, err := ha.NewClient(ha.ClientConfig{BaseURL: base, Token: token, Roots: roots})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func probeHA(t *testing.T, c *ha.Client, ctxTimeout time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), ctxTimeout)
	defer cancel()
	return ha.Module.Probe(ctx, c)
}

// the probe opens a connection of its own, authenticates and reads the core
// config, then closes it: it calls no service
func TestProbeAuthenticatesAndReadsTheConfig(t *testing.T) {
	f := hatest.New(probetest.Key)
	defer f.Close()
	f.SetResult("get_config", map[string]any{"location_name": hatest.Leak, "version": "2026.11.0"})
	probetest.Want(t, probeHA(t, probeClient(t, f.URL(), probetest.Key, f.CA()), 5*time.Second), "")
	if f.Auths() != 1 || f.Received("get_config") != 1 || len(f.Calls()) != 0 {
		t.Fatalf("auths %d get_config %d calls %d", f.Auths(), f.Received("get_config"), len(f.Calls()))
	}
	eventually(t, "the probe's connection closed", func() bool { return f.Conns() == 0 })
}

func TestProbeCodes(t *testing.T) {
	cases := map[string]struct {
		setup func(f *hatest.Server)
		token string
		ctx   time.Duration
		want  string
	}{
		"wrong token":        {token: "not-the-token", want: "auth"},
		"refused get_config": {setup: func(f *hatest.Server) { f.Fail("get_config", "unknown_error") }, want: "bad_response"},
		"upgrade refused":    {setup: func(f *hatest.Server) { f.Restart(time.Minute) }, want: "bad_response"},
		"redirect":           {setup: func(f *hatest.Server) { f.Redirect("https://elsewhere.example/api/websocket") }, want: "bad_response"},
		"silent":             {setup: func(f *hatest.Server) { f.Hold("get_config") }, ctx: probetest.Short, want: "timeout"},
		"down":               {setup: func(f *hatest.Server) { f.Close() }, want: "unreachable"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := hatest.New(probetest.Key)
			defer f.Close()
			f.SetResult("get_config", map[string]any{})
			if c.setup != nil {
				c.setup(f)
			}
			token, ctx := c.token, c.ctx
			if token == "" {
				token = probetest.Key
			}
			if ctx == 0 {
				ctx = 5 * time.Second
			}
			probetest.Want(t, probeHA(t, probeClient(t, f.URL(), token, f.CA()), ctx), c.want)
		})
	}
}

// statuses on the upgrade and a server that does not speak the api
func TestProbeUpgradeAnswers(t *testing.T) {
	cases := map[string]struct {
		h    http.HandlerFunc
		want string
	}{
		"unauthorized": {func(w http.ResponseWriter, _ *http.Request) { http.Error(w, hatest.Leak, http.StatusUnauthorized) }, "auth"},
		"rate limited": {func(w http.ResponseWriter, _ *http.Request) { http.Error(w, hatest.Leak, http.StatusTooManyRequests) }, "rate_limited"},
		"garbage": {func(w http.ResponseWriter, r *http.Request) {
			ws, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = ws.CloseNow() }()
			_ = ws.Write(r.Context(), websocket.MessageText, []byte("<html>"+hatest.Leak))
			_, _, _ = ws.Read(r.Context())
		}, "bad_response"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewTLSServer(c.h)
			defer srv.Close()
			roots := x509.NewCertPool()
			roots.AddCert(srv.Certificate())
			err := probeHA(t, probeClient(t, srv.URL, probetest.Key, roots), 5*time.Second)
			probetest.Want(t, err, c.want)
			if err != nil && strings.Contains(err.Error(), hatest.Leak) {
				t.Fatal("upstream text in the error")
			}
		})
	}
}
