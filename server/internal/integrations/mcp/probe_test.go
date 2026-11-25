package mcp_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp/mcptest"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
)

func probeClient(t *testing.T, url string, client *http.Client) *mcp.Client {
	t.Helper()
	c, err := mcp.NewClient(mcp.ClientConfig{URL: url, Authorization: "Bearer " + probetest.Key, HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func probeMCP(c *mcp.Client, d time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return mcp.Module.Probe(ctx, c)
}

// the probe connects a session of its own and lists the tools, then closes
// it: no tool is called, in either era
func TestProbeConnectsAndLists(t *testing.T) {
	for name, mode := range map[string]mcptest.Mode{"modern": mcptest.Modern, "legacy": mcptest.Legacy} {
		t.Run(name, func(t *testing.T) {
			f := fake(t, mcptest.Options{Mode: mode})
			c := probeClient(t, f.URL(), f.Client())
			probetest.Want(t, probeMCP(c, 5*time.Second), "")
			if f.Count("tools/list") == 0 || f.Count("tools/call") != 0 {
				t.Fatalf("lists %d calls %d", f.Count("tools/list"), f.Count("tools/call"))
			}
			for _, r := range f.Requests() {
				if r.Authorization != "Bearer "+probetest.Key {
					t.Fatalf("%s %s without the authorization", r.Method, r.RPC)
				}
			}
			if st := c.Status(); st.State != mcp.StateUnavailable {
				t.Fatalf("the probe's client left %s", st.State)
			}
		})
	}
}

func TestProbeCodes(t *testing.T) {
	cases := map[string]struct {
		setup func(f *mcptest.Server)
		ctx   time.Duration
		want  string
	}{
		"unauthorized": {setup: func(f *mcptest.Server) { f.Fail(http.StatusUnauthorized) }, want: "auth"},
		"forbidden":    {setup: func(f *mcptest.Server) { f.Fail(http.StatusForbidden) }, want: "auth"},
		"rate limited": {setup: func(f *mcptest.Server) { f.Fail(http.StatusTooManyRequests) }, want: "rate_limited"},
		"server error": {setup: func(f *mcptest.Server) { f.Fail(http.StatusBadGateway) }, want: "bad_response"},
		"redirect":     {setup: func(f *mcptest.Server) { f.RedirectTo("https://elsewhere.example/mcp") }, want: "bad_response"},
		"silent":       {setup: func(f *mcptest.Server) { f.Hold(true) }, ctx: probetest.Short, want: "timeout"},
		"down":         {setup: func(f *mcptest.Server) { f.Close() }, want: "unreachable"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := fake(t, mcptest.Options{})
			t.Cleanup(func() { f.Hold(false) })
			c.setup(f)
			d := c.ctx
			if d == 0 {
				d = 5 * time.Second
			}
			probetest.Want(t, probeMCP(probeClient(t, f.URL(), f.Client()), d), c.want)
		})
	}
}

// a server that answers 200 with what is no MCP at all
func TestProbeGarbage(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "<html>"+mcptest.Leak)
	}))
	defer srv.Close()
	probetest.Want(t, probeMCP(probeClient(t, srv.URL+"/mcp", probetest.Client(srv)), 5*time.Second), "bad_response")
}
