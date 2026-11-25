package mcp_test

import (
	"context"
	"crypto/x509"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp/mcptest"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

func TestSupervisorConnectsAndListsInBothEras(t *testing.T) {
	for _, tc := range []struct {
		mode mcptest.Mode
		era  string
	}{{mcptest.Modern, mcp.EraModern}, {mcptest.Legacy, mcp.EraLegacy}, {mcptest.LegacyStateless, mcp.EraLegacy}} {
		f := fake(t, mcptest.Options{Mode: tc.mode})
		r := supervise(t, f, client(t, f))
		if got := names(r.list(t)); !slices.Equal(got, builtins) {
			t.Fatalf("listing %v", got)
		}
		eventually(t, "connected", func() bool { return r.c.Status().State == mcp.StateConnected })
		if st := r.c.Status(); st.Era != tc.era {
			t.Fatalf("status %+v", st)
		}
		if !strings.Contains(r.logs.String(), "msg=mcp_connected instance=srv era="+tc.era+" tools=5\n") {
			t.Fatalf("log:\n%s", r.logs)
		}
		r.s.Stop()
		if st := r.c.Status(); st.State != mcp.StateUnavailable {
			t.Fatalf("stopped %+v", st)
		}
		r.logs.private(t, f)
	}
}

func TestSupervisorReconnectsWithBackoffAndRelists(t *testing.T) {
	f := fake(t, mcptest.Options{})
	r := supervise(t, f, client(t, f))
	r.list(t)
	eventually(t, "connected", func() bool { return r.c.Status().State == mcp.StateConnected })
	f.Fail(http.StatusBadRequest)
	_, err := r.c.CallTool(bg(), "echo", nil)
	wantCode(t, err, mcp.CodeUnavailable)
	eventually(t, "backed off to the max", func() bool { return len(r.logs.delays()) >= 6 })
	if d := r.logs.delays()[:6]; !slices.Equal(d, []int{10, 20, 40, 80, 80, 80}) {
		t.Fatalf("delays %v", d)
	}
	if st := r.c.Status(); st.State == mcp.StateConnected || st.LastCode != mcp.CodeUnavailable {
		t.Fatalf("status %+v", st)
	}
	f.Add("fresh", "Fresh.")
	f.Fail(0)
	if got := names(r.list(t)); !slices.Contains(got, "fresh") {
		t.Fatalf("relisted %v", got)
	}
	if r.logs.count("mcp_connected") != 2 || !strings.Contains(r.logs.String(), "msg=mcp_unavailable instance=srv code=mcp_unavailable\n") {
		t.Fatalf("log:\n%s", r.logs)
	}
	// a session that stayed up past StableAfter starts the backoff over
	time.Sleep(fast.StableAfter + 50*time.Millisecond)
	n := len(r.logs.delays())
	f.Fail(http.StatusBadRequest)
	_, err = r.c.CallTool(bg(), "echo", nil)
	wantCode(t, err, mcp.CodeUnavailable)
	eventually(t, "reconnecting", func() bool { return len(r.logs.delays()) > n })
	if d := r.logs.delays()[n]; d != 10 {
		t.Fatalf("delay after a stable session %d", d)
	}
	r.logs.private(t, f)
}

func TestSupervisorBacksOffAtTheMaxOnARejectedCredential(t *testing.T) {
	f := fake(t, mcptest.Options{})
	f.Fail(http.StatusUnauthorized)
	r := supervise(t, f, client(t, f))
	eventually(t, "two retries", func() bool { return len(r.logs.delays()) >= 2 })
	for _, d := range r.logs.delays() {
		if d != 80 {
			t.Fatalf("delays %v", r.logs.delays())
		}
	}
	if st := r.c.Status(); st.State != mcp.StateUnavailable && st.State != mcp.StateConnecting || st.LastCode != mcp.CodeAuth {
		t.Fatalf("status %+v", st)
	}
	if !strings.Contains(r.logs.String(), "msg=mcp_unavailable instance=srv code=mcp_auth\n") {
		t.Fatalf("log:\n%s", r.logs)
	}
	r.logs.private(t, f)
	r.s.Stop()
	eventually(t, "no mcp goroutine left", func() bool { return leftover() == "" })
}

// a call that breaks the session leaves the ones in flight to finish while
// the supervisor reconnects
func TestSupervisorReconnectsWhileCallsInFlightFinish(t *testing.T) {
	for _, mode := range []mcptest.Mode{mcptest.Modern, mcptest.Legacy} {
		f := fake(t, mcptest.Options{Mode: mode})
		r := supervise(t, f, client(t, f))
		r.list(t)
		eventually(t, "connected", func() bool { return r.c.Status().State == mcp.StateConnected })
		done := make(chan error, 1)
		go func() {
			_, err := r.c.CallTool(bg(), "slow", nil)
			done <- err
		}()
		eventually(t, "slow running", func() bool { return f.Calls("slow") == 1 })
		f.FailNext(http.StatusUnauthorized)
		_, err := r.c.CallTool(bg(), "echo", nil)
		wantCode(t, err, mcp.CodeAuth)
		r.list(t)
		eventually(t, "connected again", func() bool { return r.c.Status().State == mcp.StateConnected })
		f.Release()
		if err := <-done; err != nil {
			t.Fatalf("the call in flight failed: %v", err)
		}
		if f.Calls("slow") != 1 {
			t.Fatalf("slow ran %d times", f.Calls("slow"))
		}
		if _, err := r.c.CallTool(bg(), "echo", nil); err != nil || f.Calls("echo") != 1 {
			t.Fatalf("call on the new session: %v, %d", err, f.Calls("echo"))
		}
		r.s.Stop()
	}
}

func TestStopNeverWaitsOnABlockedListingCallback(t *testing.T) {
	f := fake(t, mcptest.Options{})
	c := client(t, f)
	entered := make(chan struct{})
	s := mcp.Supervise(mcp.SupervisorConfig{Instance: "srv", Client: c, Log: slog.New(slog.DiscardHandler), OnTools: func(ctx context.Context, _ []mcp.RemoteTool) {
		close(entered)
		<-ctx.Done()
	}})
	<-entered
	start := time.Now()
	s.Stop()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Stop took %v", d)
	}
}

func TestSupervisorReconnectsOnceAfterALegacySessionDrop(t *testing.T) {
	// the SDK's own 404 answer, and an older SDK's 404 with a JSON-RPC error body
	for name, drop := range map[string]func(*mcptest.Server){
		"plain 404": (*mcptest.Server).DropSessions, "json-rpc 404": (*mcptest.Server).ForgetSessions,
	} {
		t.Run(name, func(t *testing.T) {
			f := fake(t, mcptest.Options{Mode: mcptest.Legacy})
			c := client(t, f)
			// no session here lives long enough to start the backoff over
			mcp.SetTimings(c, mcp.Timings{StableAfter: time.Minute})
			r := supervise(t, f, c)
			r.list(t)
			eventually(t, "connected", func() bool { return r.c.Status().State == mcp.StateConnected })
			drop(f)
			_, err := r.c.CallTool(bg(), "echo", nil)
			wantCode(t, err, mcp.CodeUnavailable)
			r.list(t)
			eventually(t, "connected again", func() bool { return r.c.Status().State == mcp.StateConnected })
			if d := r.logs.delays(); !slices.Equal(d, []int{0}) || f.Count("initialize") != 2 {
				t.Fatalf("delays %v, initialize %d", d, f.Count("initialize"))
			}
			time.Sleep(50 * time.Millisecond)
			if f.Calls("echo") != 0 {
				t.Fatalf("the failed call was replayed")
			}
			if _, err := r.c.CallTool(bg(), "echo", nil); err != nil || f.Calls("echo") != 1 {
				t.Fatalf("call after the reconnect: %v, %d", err, f.Calls("echo"))
			}
			// a second drop soon after is not reconnected at once again
			drop(f)
			_, err = r.c.CallTool(bg(), "echo", nil)
			wantCode(t, err, mcp.CodeUnavailable)
			r.list(t)
			if d := r.logs.delays(); !slices.Equal(d, []int{0, 10}) {
				t.Fatalf("delays %v", d)
			}
			r.logs.private(t, f)
		})
	}
}

func TestSupervisorNeverBlocksStartOrOtherServers(t *testing.T) {
	release := make(chan struct{})
	hang := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer hang.Close()
	defer close(release)
	hc, err := mcp.NewClient(mcp.ClientConfig{URL: hang.URL + "/mcp", HTTP: httpx.Client(poolOf(hang))})
	if err != nil {
		t.Fatal(err)
	}
	mcp.SetTimings(hc, mcp.Timings{Connect: time.Minute})
	start := time.Now()
	stuck := mcp.Supervise(mcp.SupervisorConfig{Instance: "stuck", Client: hc})
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("Supervise blocked %v", d)
	}
	f := fake(t, mcptest.Options{})
	r := supervise(t, f, client(t, f))
	r.list(t)
	if st := hc.Status(); st.State != mcp.StateConnecting {
		t.Fatalf("stuck %+v", st)
	}
	start = time.Now()
	stuck.Stop()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Stop took %v", d)
	}
	if st := r.c.Status(); st.State != mcp.StateConnected {
		t.Fatalf("other %+v", st)
	}
}

func TestStopLeavesNoGoroutine(t *testing.T) {
	for _, mode := range []mcptest.Mode{mcptest.Modern, mcptest.Legacy} {
		f := mcptest.New(mcptest.Options{Mode: mode})
		c := client(t, f)
		r := supervise(t, f, c)
		r.list(t)
		eventually(t, "connected", func() bool { return c.Status().State == mcp.StateConnected })
		if _, err := c.CallTool(bg(), "echo", nil); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(bg(), 50*time.Millisecond)
		_, _ = c.CallTool(ctx, "slow", nil)
		cancel()
		r.s.Stop()
		r.s.Stop()
		f.Close()
		eventually(t, "no mcp goroutine left", func() bool { return leftover() == "" })
	}
}

func poolOf(s *httptest.Server) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(s.Certificate())
	return p
}

// leftover is the first goroutine stack running the SDK or this package.
func leftover() string {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	for g := range strings.SplitSeq(string(buf), "\n\n") {
		if strings.Contains(g, "modelcontextprotocol/go-sdk") || strings.Contains(g, "cadence/server/internal/integrations/mcp.") {
			return g
		}
	}
	return ""
}

// a wrong path answers 404 before any session: a misconfiguration, never a
// dropped session, so it backs off like any failure
func TestSupervisorBacksOffOnAMisconfiguredEndpoint(t *testing.T) {
	for _, mode := range []mcptest.Mode{mcptest.Modern, mcptest.Legacy} {
		f := fake(t, mcptest.Options{Mode: mode})
		c, err := mcp.NewClient(mcp.ClientConfig{URL: f.URL() + "/wrong", Authorization: auth, HTTP: f.Client()})
		if err != nil {
			t.Fatal(err)
		}
		mcp.SetTimings(c, fast)
		r := supervise(t, f, c)
		eventually(t, "backed off to the max", func() bool { return len(r.logs.delays()) >= 5 })
		if d := r.logs.delays()[:5]; !slices.Equal(d, []int{10, 20, 40, 80, 80}) {
			t.Fatalf("mode %d: delays %v", mode, d)
		}
		r.s.Stop()
		r.logs.private(t, f)
	}
}
