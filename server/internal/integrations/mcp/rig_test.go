package mcp_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp/mcptest"
)

const auth = "Bearer mcp-token-5d2c8e-never-printed"

// fast keeps every wait in the tens of milliseconds
var fast = mcp.Timings{
	Connect: 2 * time.Second, List: 2 * time.Second,
	BackoffMin: 10 * time.Millisecond, BackoffMax: 80 * time.Millisecond, StableAfter: 300 * time.Millisecond,
}

func bg() context.Context { return context.Background() }

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if got := mcp.Code(err); got != code {
		t.Fatalf("code %q (%v), want %q", got, err, code)
	}
	if strings.Contains(err.Error(), mcptest.Leak) || strings.Contains(err.Error(), auth) {
		t.Fatalf("error %q leaks", err)
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func fake(t *testing.T, o mcptest.Options) *mcptest.Server {
	f := mcptest.New(o)
	t.Cleanup(f.Close)
	return f
}

type clientOpt func(*mcp.ClientConfig)

func withAuth(v string) clientOpt           { return func(c *mcp.ClientConfig) { c.Authorization = v } }
func withHTTP(h *http.Client) clientOpt     { return func(c *mcp.ClientConfig) { c.HTTP = h } }
func withTimeout(d time.Duration) clientOpt { return func(c *mcp.ClientConfig) { c.Timeout = d } }

// client talks to f with auth over a client that trusts f, fast timings.
func client(t *testing.T, f *mcptest.Server, opts ...clientOpt) *mcp.Client {
	t.Helper()
	cfg := mcp.ClientConfig{URL: f.URL(), Authorization: auth, HTTP: f.Client()}
	for _, o := range opts {
		o(&cfg)
	}
	c, err := mcp.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mcp.SetTimings(c, fast)
	t.Cleanup(c.Close)
	return c
}

func names(tools []mcp.RemoteTool) []string {
	var out []string
	for _, tl := range tools {
		out = append(out, tl.Name)
	}
	slices.Sort(out)
	return out
}

type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *logBuffer) count(event string) int {
	return strings.Count(b.String(), "msg="+event+" ") + strings.Count(b.String(), "msg="+event+"\n")
}

var delayRe = regexp.MustCompile(`msg=mcp_reconnect instance=srv delay_ms=(\d+)`)

func (b *logBuffer) delays() []int {
	var out []int
	for _, m := range delayRe.FindAllStringSubmatch(b.String(), -1) {
		n, _ := strconv.Atoi(m[1])
		out = append(out, n)
	}
	return out
}

// private fails t when the log holds the credential, the endpoint, remote
// text or a tool description
func (b *logBuffer) private(t *testing.T, f *mcptest.Server) {
	t.Helper()
	s := b.String()
	for _, secret := range []string{auth, f.URL(), strings.TrimPrefix(f.URL(), "https://"), mcptest.Path, mcptest.Leak, "Echoes.", "Waits."} {
		if strings.Contains(s, secret) {
			t.Fatalf("log holds %q:\n%s", secret, s)
		}
	}
}

type rig struct {
	f     *mcptest.Server
	c     *mcp.Client
	logs  *logBuffer
	s     *mcp.Supervisor
	lists chan []mcp.RemoteTool
}

// supervise runs a supervisor named srv on c, its listings on r.lists.
func supervise(t *testing.T, f *mcptest.Server, c *mcp.Client) *rig {
	t.Helper()
	r := &rig{f: f, c: c, logs: &logBuffer{}, lists: make(chan []mcp.RemoteTool, 16)}
	r.s = mcp.Supervise(mcp.SupervisorConfig{
		Instance: "srv", Client: c, Log: slog.New(slog.NewTextHandler(r.logs, nil)),
		OnTools: func(_ context.Context, tools []mcp.RemoteTool) { r.lists <- tools },
	})
	t.Cleanup(r.s.Stop)
	return r
}

func (r *rig) list(t *testing.T) []mcp.RemoteTool {
	t.Helper()
	select {
	case tools := <-r.lists:
		return tools
	case <-time.After(5 * time.Second):
		t.Fatalf("no listing; log:\n%s", r.logs)
		return nil
	}
}
