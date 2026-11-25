package mcp_test

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp/mcptest"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

var builtins = []string{"echo", "fail", "huge", "image", "slow"}

func TestNewClientTakesOnlyPlainHTTPSEndpoints(t *testing.T) {
	for _, ok := range []string{"https://m.example/mcp", "https://m.example:8443/a/b", "https://m.example"} {
		if _, err := mcp.NewClient(mcp.ClientConfig{URL: ok}); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "http://m.example/mcp", "ftp://m.example/mcp", "https://", "https://:443/mcp", "https://u:pw@m.example/mcp",
		"https://m.example/mcp?x=1", "https://m.example/mcp#f", "https://m.example/a/../mcp", "https://m.example/./mcp",
		"https://m.example/m cp", "https://m.example\\mcp", "m.example/mcp", "https://m.example/" + strings.Repeat("a", 2040),
	} {
		_, err := mcp.NewClient(mcp.ClientConfig{URL: bad})
		if !errors.Is(err, mcp.ErrBadURL) || (bad != "" && strings.Contains(err.Error(), bad)) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	for _, cfg := range []mcp.ClientConfig{
		{Timeout: 999 * time.Millisecond}, {Timeout: 31 * time.Second}, {MaxResultChars: 255}, {MaxResultChars: 65537},
		{ToolPrefix: "Bad"}, {ToolPrefix: "a-b"}, {Authorization: "Bearer \x7f"},
	} {
		cfg.URL = "https://m.example/mcp"
		if _, err := mcp.NewClient(cfg); err == nil || strings.Contains(err.Error(), "Bearer") {
			t.Errorf("%+v: %v", cfg, err)
		}
	}
}

func TestConnectAndListInBothEras(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    mcptest.Options
		era  string
	}{
		{"modern", mcptest.Options{Mode: mcptest.Modern}, mcp.EraModern},
		{"modern json", mcptest.Options{Mode: mcptest.Modern, JSONResponse: true}, mcp.EraModern},
		{"legacy", mcptest.Options{Mode: mcptest.Legacy}, mcp.EraLegacy},
		{"legacy json", mcptest.Options{Mode: mcptest.Legacy, JSONResponse: true}, mcp.EraLegacy},
		{"legacy stateless", mcptest.Options{Mode: mcptest.LegacyStateless}, mcp.EraLegacy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fake(t, tc.o)
			c := client(t, f)
			if _, err := c.ListTools(bg()); mcp.Code(err) != mcp.CodeUnavailable {
				t.Fatalf("listed before connecting: %v", err)
			}
			if err := c.Connect(bg()); err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(f.URL())
			if st := c.Status(); st.State != mcp.StateConnected || st.Era != tc.era || st.Host != u.Host || st.ConnectedAt.IsZero() || st.LastCode != "" {
				t.Fatalf("status %+v", st)
			}
			tools, err := c.ListTools(bg())
			if err != nil || !slices.Equal(names(tools), builtins) {
				t.Fatalf("tools %v %v", names(tools), err)
			}
			for _, tl := range tools {
				if tl.Name == "echo" && (tl.Description != "Echoes." || !strings.Contains(string(tl.InputSchema), `"type":"object"`)) {
					t.Fatalf("echo %+v", tl)
				}
			}
			res, err := c.CallTool(bg(), "echo", map[string]any{"text": "x"})
			if err != nil || len(res.Content) != 1 {
				t.Fatalf("call %+v %v", res, err)
			}
			if txt, ok := res.Content[0].(*sdk.TextContent); !ok || txt.Text != "ok" || f.Calls("echo") != 1 {
				t.Fatalf("content %#v, calls %d", res.Content[0], f.Calls("echo"))
			}
			if res, err := c.CallTool(bg(), "image", nil); err != nil || len(res.Content) != 1 {
				t.Fatalf("image %+v %v", res, err)
			}
			discovered, initialized := f.Count("server/discover"), f.Count("initialize")
			if (tc.era == mcp.EraModern) != (initialized == 0) || discovered == 0 {
				t.Fatalf("discover %d, initialize %d", discovered, initialized)
			}
			// no roots, sampling or elicitation advertised
			if caps := f.Capabilities(); len(caps) == 0 || slices.ContainsFunc(caps, func(c string) bool { return c != "{}" }) {
				t.Fatalf("capabilities %q", caps)
			}
			c.Close()
			if st := c.Status(); st.State != mcp.StateUnavailable || !st.ConnectedAt.IsZero() {
				t.Fatalf("closed %+v", st)
			}
			wantCode(t, func() error { _, err := c.CallTool(bg(), "echo", nil); return err }(), mcp.CodeUnavailable)
			deletes := 0
			for _, r := range f.Requests() {
				switch {
				case r.Authorization != auth:
					t.Fatalf("%s %s without the authorization", r.Method, r.RPC)
				case r.Method == http.MethodGet:
					t.Fatal("standalone SSE stream opened")
				case r.Method == http.MethodDelete:
					deletes++
				}
			}
			if wantDelete := tc.o.Mode == mcptest.Legacy; (deletes == 1) != wantDelete {
				t.Fatalf("%d DELETEs closing the session", deletes)
			}
		})
	}
}

func TestCloseAbortsTheCallsInFlight(t *testing.T) {
	f := fake(t, mcptest.Options{})
	c := client(t, f)
	if err := c.Connect(bg()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.CallTool(bg(), "slow", nil)
		done <- err
	}()
	eventually(t, "slow running", func() bool { return f.Calls("slow") == 1 })
	start := time.Now()
	c.Close()
	wantCode(t, <-done, mcp.CodeUnavailable)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Close took %v", d)
	}
}

func TestCloseAbortsARefreshDialingAHangingServer(t *testing.T) {
	f := mcptest.New(mcptest.Options{})
	defer f.Close()
	c := client(t, f)
	mcp.SetTimings(c, mcp.Timings{Connect: time.Minute, List: time.Minute})
	if err := c.Connect(bg()); err != nil {
		t.Fatal(err)
	}
	f.Hold(true)
	before := f.Hits()
	done := make(chan error, 1)
	go func() {
		_, err := c.Refresh(bg())
		done <- err
	}()
	eventually(t, "refresh dialing", func() bool { return f.Hits() > before })
	start := time.Now()
	c.Close()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Close took %v", d)
	}
	wantCode(t, <-done, mcp.CodeUnavailable)
	f.Close()
	eventually(t, "no mcp goroutine left", func() bool { return leftover() == "" })
}

func TestAFailedCallLeavesTheOthersInFlight(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		breaks bool
	}{
		{http.StatusServiceUnavailable, mcp.CodeUnavailable, false},
		{http.StatusTooManyRequests, mcp.CodeUnavailable, false},
		{http.StatusForbidden, mcp.CodeAuth, false},
		{http.StatusUnauthorized, mcp.CodeAuth, true},
	} {
		for _, mode := range []mcptest.Mode{mcptest.Modern, mcptest.Legacy} {
			f := fake(t, mcptest.Options{Mode: mode})
			c := client(t, f)
			if err := c.Connect(bg()); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				res, err := c.CallTool(bg(), "slow", nil)
				if err == nil && res.Content[0].(*sdk.TextContent).Text != "done" {
					err = errors.New("wrong answer")
				}
				done <- err
			}()
			eventually(t, "slow running", func() bool { return f.Calls("slow") == 1 })
			f.FailNext(tc.status)
			_, err := c.CallTool(bg(), "echo", nil)
			wantCode(t, err, tc.code)
			if st := c.Status(); (st.State != mcp.StateConnected) != tc.breaks {
				t.Fatalf("%d: status %+v", tc.status, st)
			}
			f.Release()
			if err := <-done; err != nil {
				t.Fatalf("%d: the call in flight failed: %v", tc.status, err)
			}
			if f.Calls("slow") != 1 || f.Calls("echo") != 0 {
				t.Fatalf("%d: slow %d, echo %d", tc.status, f.Calls("slow"), f.Calls("echo"))
			}
		}
	}
}

// a 403 refuses one call; on a listing, as a 401 anywhere, it rejects the
// credential and breaks the session
func TestForbiddenBreaksOnlyOutsideACall(t *testing.T) {
	f := fake(t, mcptest.Options{})
	c := client(t, f)
	if err := c.Connect(bg()); err != nil {
		t.Fatal(err)
	}
	f.FailNext(http.StatusForbidden)
	_, err := c.ListTools(bg())
	wantCode(t, err, mcp.CodeAuth)
	if st := c.Status(); st.State != mcp.StateUnavailable || st.LastCode != mcp.CodeAuth {
		t.Fatalf("status %+v", st)
	}
}

func TestServerRPCErrorWithAnSDKCodeIsTheTools(t *testing.T) {
	f := fake(t, mcptest.Options{})
	codes := []int64{-32005, -32001, -32603, -32004, -32003}
	for _, code := range codes {
		f.AddRPCError("rpc"+strconv.Itoa(int(-code)), code)
	}
	c := client(t, f)
	for _, code := range codes {
		if err := c.Connect(bg()); err != nil {
			t.Fatal(err)
		}
		_, err := c.CallTool(bg(), "rpc"+strconv.Itoa(int(-code)), nil)
		if code == -32004 || code == -32003 {
			// the SDK reads its own closing codes from the wire as a closed
			// connection and keeps only their text: indistinguishable by type
			// from a dead connection, so the session is replaced
			wantCode(t, err, mcp.CodeUnavailable)
			continue
		}
		wantCode(t, err, mcp.CodeToolError)
		if st := c.Status(); st.State != mcp.StateConnected {
			t.Fatalf("%d: status %+v", code, st)
		}
	}
}

func TestRefreshNeverServesACachedListing(t *testing.T) {
	for _, mode := range []mcptest.Mode{mcptest.Modern, mcptest.Legacy} {
		f := fake(t, mcptest.Options{Mode: mode, ListTTL: time.Hour})
		c := client(t, f)
		if err := c.Connect(bg()); err != nil {
			t.Fatal(err)
		}
		if _, err := c.ListTools(bg()); err != nil {
			t.Fatal(err)
		}
		f.Add("fresh", "Fresh.")
		tools, err := c.ListTools(bg())
		if err != nil {
			t.Fatal(err)
		}
		if cached := !slices.Contains(names(tools), "fresh"); cached != (mode == mcptest.Modern) {
			t.Fatalf("mode %d: listing %v", mode, names(tools))
		}
		tools, err = c.Refresh(bg())
		if err != nil || !slices.Contains(names(tools), "fresh") {
			t.Fatalf("mode %d: refreshed %v %v", mode, names(tools), err)
		}
		if st := c.Status(); st.State != mcp.StateConnected {
			t.Fatalf("status %+v", st)
		}
		f.Fail(http.StatusUnauthorized)
		_, err = c.Refresh(bg())
		wantCode(t, err, mcp.CodeAuth)
		if st := c.Status(); st.State != mcp.StateUnavailable || st.LastCode != mcp.CodeAuth {
			t.Fatalf("mode %d: status %+v", mode, st)
		}
		wantCode(t, func() error { _, err := c.Refresh(bg()); return err }(), mcp.CodeUnavailable)
	}
}

func TestNoAuthorizationSendsNoHeader(t *testing.T) {
	f := fake(t, mcptest.Options{})
	c := client(t, f, withAuth(""))
	if err := c.Connect(bg()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListTools(bg()); err != nil {
		t.Fatal(err)
	}
	for _, a := range f.Authorizations() {
		if a != "" {
			t.Fatalf("authorization %q", a)
		}
	}
}

func TestListPagesThroughEveryCursor(t *testing.T) {
	f := fake(t, mcptest.Options{PageSize: 2})
	for i := range 6 {
		f.Add("extra"+strconv.Itoa(i), "Extra.")
	}
	c := client(t, f)
	if err := c.Connect(bg()); err != nil {
		t.Fatal(err)
	}
	tools, err := c.ListTools(bg())
	if err != nil || len(tools) != 11 || f.Count("tools/list") != 6 {
		t.Fatalf("%d tools, %d pages: %v", len(tools), f.Count("tools/list"), err)
	}
	// the listing sees the server's tools as they are now, a changed description too
	f.Remove("extra0")
	f.Add("extra1", "Changed.")
	tools, err = c.ListTools(bg())
	if err != nil || len(tools) != 10 {
		t.Fatalf("%d tools: %v", len(tools), err)
	}
	for _, tl := range tools {
		if tl.Name == "extra1" && tl.Description != "Changed." {
			t.Fatalf("extra1 %q", tl.Description)
		}
	}
}

func TestListStopsAtARepeatedCursor(t *testing.T) {
	f := fake(t, mcptest.Options{})
	c := client(t, f)
	if err := c.Connect(bg()); err != nil {
		t.Fatal(err)
	}
	f.RepeatCursor(true)
	_, err := c.ListTools(bg())
	wantCode(t, err, mcp.CodeProtocol)
	if n := f.Count("tools/list"); n != 2 {
		t.Fatalf("%d pages asked", n)
	}
	// the listing failed, not the session
	if st := c.Status(); st.State != mcp.StateConnected || st.LastCode != "" {
		t.Fatalf("status %+v", st)
	}
	if _, err := c.CallTool(bg(), "echo", nil); err != nil {
		t.Fatal(err)
	}
}

func TestListCaps(t *testing.T) {
	list := func(f *mcptest.Server) ([]mcp.RemoteTool, error) {
		c := client(t, f)
		if err := c.Connect(bg()); err != nil {
			t.Fatal(err)
		}
		return c.ListTools(bg())
	}
	t.Run("pages", func(t *testing.T) {
		f := fake(t, mcptest.Options{PageSize: 1})
		for i := range 15 {
			f.Add("p"+strconv.Itoa(i), "P.")
		}
		if tools, err := list(f); err != nil || len(tools) != 20 {
			t.Fatalf("20 pages: %d %v", len(tools), err)
		}
		f.Add("p15", "P.")
		before := f.Count("tools/list")
		_, err := list(f)
		wantCode(t, err, mcp.CodeProtocol)
		if n := f.Count("tools/list") - before; n != 20 {
			t.Fatalf("%d pages asked", n)
		}
	})
	t.Run("tools", func(t *testing.T) {
		f := fake(t, mcptest.Options{})
		for i := range 507 {
			f.Add("t"+strconv.Itoa(i), "T.")
		}
		if tools, err := list(f); err != nil || len(tools) != 512 {
			t.Fatalf("512 tools: %d %v", len(tools), err)
		}
		f.Add("t507", "T.")
		_, err := list(f)
		wantCode(t, err, mcp.CodeProtocol)
	})
}

func TestOddNamesListed(t *testing.T) {
	f := fake(t, mcptest.Options{})
	odd := []string{"a.b", strings.Repeat("n", 100), "wëird name"}
	for _, n := range odd {
		f.Add(n, "Odd.")
	}
	c := client(t, f)
	if err := c.Connect(bg()); err != nil {
		t.Fatal(err)
	}
	tools, err := c.ListTools(bg())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range odd {
		if !slices.Contains(names(tools), n) {
			t.Fatalf("%q missing", n)
		}
	}
}

func TestTransportPinsTheEndpoint(t *testing.T) {
	f := fake(t, mcptest.Options{})
	c := client(t, f)
	rt := mcp.Pin(c)
	u, _ := url.Parse(f.URL())
	other := *u
	other.Host = u.Hostname() + ":1"
	for _, target := range []string{
		"https://m.example" + mcptest.Path, f.URL() + "/x", f.URL() + "?q=1", strings.Replace(f.URL(), "https:", "http:", 1),
		other.String(), strings.TrimSuffix(f.URL(), mcptest.Path) + "/",
	} {
		req, _ := http.NewRequestWithContext(bg(), http.MethodPost, target, strings.NewReader("{}"))
		resp, err := rt.RoundTrip(req)
		if err == nil {
			_ = resp.Body.Close()
			t.Fatalf("%s sent", target)
		}
		if strings.Contains(err.Error(), target) || strings.Contains(err.Error(), auth) {
			t.Fatalf("error %q", err)
		}
	}
	if f.Hits() != 0 {
		t.Fatalf("%d requests reached the server", f.Hits())
	}
	req, _ := http.NewRequestWithContext(bg(), http.MethodPost, f.URL(), strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer someone-else")
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := f.Authorizations(); len(got) != 1 || got[0] != auth || req.Header.Get("Authorization") != "Bearer someone-else" {
		t.Fatalf("authorizations %q; caller's request changed: %q", got, req.Header.Get("Authorization"))
	}
}

func TestRedirectRefusedAndNeverFollowed(t *testing.T) {
	f := fake(t, mcptest.Options{})
	target := fake(t, mcptest.Options{})
	pool := x509.NewCertPool()
	pool.AddCert(f.Certificate())
	pool.AddCert(target.Certificate())
	c := client(t, f, withHTTP(httpx.Client(pool)))
	f.RedirectTo(target.URL())
	wantCode(t, c.Connect(bg()), mcp.CodeUnavailable)
	if f.Hits() == 0 || target.Hits() != 0 {
		t.Fatalf("hits %d, target %d", f.Hits(), target.Hits())
	}
	// once connected, a redirect breaks the session the same way
	f.RedirectTo("")
	if err := c.Connect(bg()); err != nil {
		t.Fatal(err)
	}
	f.RedirectTo(target.URL())
	_, err := c.CallTool(bg(), "echo", nil)
	wantCode(t, err, mcp.CodeUnavailable)
	if target.Hits() != 0 || f.Calls("echo") != 0 {
		t.Fatalf("target %d, calls %d", target.Hits(), f.Calls("echo"))
	}
}

func TestBadCertificateRefused(t *testing.T) {
	f := fake(t, mcptest.Options{})
	// the fake's self-signed certificate is in neither pool
	for _, roots := range []*x509.CertPool{nil, x509.NewCertPool()} {
		c := client(t, f, withHTTP(httpx.Client(roots)))
		wantCode(t, c.Connect(bg()), mcp.CodeUnavailable)
		if st := c.Status(); st.State != mcp.StateUnavailable || st.LastCode != mcp.CodeUnavailable {
			t.Fatalf("status %+v", st)
		}
	}
	if f.Hits() != 0 {
		t.Fatalf("%d requests past a bad certificate", f.Hits())
	}
}

func TestRejectedCredentialIsAuth(t *testing.T) {
	for _, mode := range []mcptest.Mode{mcptest.Modern, mcptest.Legacy} {
		f := fake(t, mcptest.Options{Mode: mode})
		c := client(t, f)
		f.Fail(http.StatusUnauthorized)
		wantCode(t, c.Connect(bg()), mcp.CodeAuth)
		if st := c.Status(); st.State != mcp.StateUnavailable || st.LastCode != mcp.CodeAuth {
			t.Fatalf("status %+v", st)
		}
		f.Fail(0)
		if err := c.Connect(bg()); err != nil {
			t.Fatal(err)
		}
		f.Fail(http.StatusForbidden)
		_, err := c.ListTools(bg())
		wantCode(t, err, mcp.CodeAuth)
		if st := c.Status(); st.State != mcp.StateUnavailable || st.LastCode != mcp.CodeAuth {
			t.Fatalf("status %+v", st)
		}
	}
}

func TestBodyOverTheCapIsProtocol(t *testing.T) {
	for _, mode := range []mcptest.Mode{mcptest.Modern, mcptest.Legacy} {
		f := fake(t, mcptest.Options{Mode: mode})
		c := client(t, f)
		if err := c.Connect(bg()); err != nil {
			t.Fatal(err)
		}
		f.OversizeOn("tools/list")
		_, err := c.ListTools(bg())
		wantCode(t, err, mcp.CodeProtocol)
		if st := c.Status(); st.State != mcp.StateUnavailable || st.LastCode != mcp.CodeProtocol {
			t.Fatalf("status %+v", st)
		}
		f.OversizeOn("tools/call")
		if err := c.Connect(bg()); err != nil {
			t.Fatal(err)
		}
		_, err = c.CallTool(bg(), "echo", nil)
		wantCode(t, err, mcp.CodeProtocol)
		f.OversizeOn("initialize")
		if mode == mcptest.Legacy {
			wantCode(t, c.Connect(bg()), mcp.CodeProtocol)
		}
		// the cap names its own response's failure, never a later one
		f.OversizeOn("")
		if err := c.Connect(bg()); err != nil {
			t.Fatal(err)
		}
		f.FailNext(http.StatusServiceUnavailable)
		_, err = c.CallTool(bg(), "echo", nil)
		wantCode(t, err, mcp.CodeUnavailable)
	}
}

func TestCallFailures(t *testing.T) {
	f := fake(t, mcptest.Options{})
	c := client(t, f)
	if err := c.Connect(bg()); err != nil {
		t.Fatal(err)
	}
	_, err := c.CallTool(bg(), "fail", nil)
	wantCode(t, err, mcp.CodeToolError)
	_, err = c.CallTool(bg(), "nope", nil)
	wantCode(t, err, mcp.CodeUnknownTool)
	ctx, cancel := context.WithTimeout(bg(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = c.CallTool(ctx, "slow", nil)
	wantCode(t, err, mcp.CodeTimeout)
	if time.Since(start) > 2*time.Second {
		t.Fatalf("timed out after %v", time.Since(start))
	}
	cctx, stop := context.WithCancel(bg())
	time.AfterFunc(50*time.Millisecond, stop)
	if _, err := c.CallTool(cctx, "slow", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	if st := c.Status(); st.State != mcp.StateConnected || st.LastCode != "" {
		t.Fatalf("a tool's failures broke the session: %+v", st)
	}
	if _, err := c.CallTool(bg(), "echo", nil); err != nil {
		t.Fatal(err)
	}
	if f.Calls("slow") != 2 || f.Calls("fail") != 1 {
		t.Fatalf("slow %d, fail %d: a call was retried", f.Calls("slow"), f.Calls("fail"))
	}
}

func TestCallTimesOutAtTheConfiguredTimeout(t *testing.T) {
	f := fake(t, mcptest.Options{})
	c := client(t, f, withTimeout(time.Second))
	if err := c.Connect(bg()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := c.CallTool(bg(), "slow", nil)
	wantCode(t, err, mcp.CodeTimeout)
	if d := time.Since(start); d < time.Second || d > 3*time.Second {
		t.Fatalf("timed out after %v", d)
	}
}

// a caller's deadline that ends a refresh's own dial is a timeout, as on a call
func TestRefreshPastTheCallersDeadlineTimesOut(t *testing.T) {
	f := fake(t, mcptest.Options{})
	c := client(t, f)
	if err := c.Connect(bg()); err != nil {
		t.Fatal(err)
	}
	f.Hold(true)
	defer f.Hold(false)
	ctx, cancel := context.WithTimeout(bg(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Refresh(ctx)
	wantCode(t, err, mcp.CodeTimeout)
	if st := c.Status(); st.State != mcp.StateConnected {
		t.Fatalf("a timed-out refresh broke the session: %+v", st)
	}
}

// a call whose deadline has passed is refused before it leaves, as mcp_timeout
func TestACallPastItsDeadlineIsNotSent(t *testing.T) {
	f := fake(t, mcptest.Options{})
	c := client(t, f)
	if err := c.Connect(bg()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithDeadline(bg(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := c.CallTool(ctx, "echo", nil)
	wantCode(t, err, mcp.CodeTimeout)
	if !mcp.NotSent(err) || f.Count("tools/call") != 0 {
		t.Fatalf("sent: %v, %d calls", mcp.NotSent(err), f.Count("tools/call"))
	}
}
