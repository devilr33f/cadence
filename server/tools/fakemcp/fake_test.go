package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

const (
	testToken = "fake-mcp-test-token"
	testPath  = "/e2e-test-endpoint"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// rig serves the fake over tls to the server's own mcp client.
type rig struct {
	t      *testing.T
	srv    *httptest.Server
	logs   *syncBuf
	calls  *syncBuf
	mutate string
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, logs: &syncBuf{}, calls: &syncBuf{}, mutate: filepath.Join(t.TempDir(), "mutate")}
	f := newFake(config{
		Token: testToken, Path: testPath, Mutate: r.mutate, Calls: r.calls, Log: slog.New(slog.NewJSONHandler(r.logs, nil)),
	})
	r.srv = httptest.NewTLSServer(f)
	t.Cleanup(func() {
		f.mcp.Close()
		r.srv.Close()
	})
	// the log never carries the token, the path, a description, an argument or a result
	t.Cleanup(func() {
		for _, s := range []string{testToken, testPath, echoDescription, mutatedDescription, noteResult, "secret-text-41", "secret-note-41"} {
			if strings.Contains(r.logs.String(), s) {
				t.Errorf("log carries %q:\n%s", s, r.logs)
			}
		}
	})
	return r
}

// client is the server's client of the fake at path with authorization.
func (r *rig) client(path, authorization string) *mcp.Client {
	r.t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(r.srv.Certificate())
	c, err := mcp.NewClient(mcp.ClientConfig{URL: r.srv.URL + path, Authorization: authorization, HTTP: httpx.Client(roots)})
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(c.Close)
	return c
}

func (r *rig) connected() *mcp.Client {
	r.t.Helper()
	c := r.client(testPath, "Bearer "+testToken)
	if err := c.Connect(context.Background()); err != nil {
		r.t.Fatalf("connect: %v", err)
	}
	return c
}

func descriptions(t *testing.T, c *mcp.Client) map[string]string {
	t.Helper()
	tools, err := c.Refresh(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	out := map[string]string{}
	for _, tl := range tools {
		out[tl.Name] = tl.Description
	}
	return out
}

func answered(t *testing.T, c *mcp.Client, tool string, args map[string]any) string {
	t.Helper()
	res, err := c.CallTool(context.Background(), tool, args)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	if res.Structured != nil || len(res.Content) != 1 {
		t.Fatalf("%s answered %+v", tool, res)
	}
	tc, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("%s answered %T", tool, res.Content[0])
	}
	return tc.Text
}

func TestFakeServesEchoAndNoteAdd(t *testing.T) {
	r := newRig(t)
	c := r.connected()
	if st := c.Status(); st.Era != mcp.EraModern {
		t.Fatalf("status %+v", st)
	}
	if got := descriptions(t, c); len(got) != 2 || got["echo"] != echoDescription || got["note_add"] == "" {
		t.Fatalf("tools %v", got)
	}
	if got := answered(t, c, "echo", map[string]any{"text": "secret-text-41"}); got != "secret-text-41" {
		t.Fatalf("echo answered %q", got)
	}
	if got := answered(t, c, "note_add", map[string]any{"note": "secret-note-41"}); got != noteResult {
		t.Fatalf("note_add answered %q", got)
	}
	// a call the tool cannot read is the tool's error, and still recorded: it reached the tool
	if _, err := c.CallTool(context.Background(), "note_add", map[string]any{"note": 1}); mcp.Code(err) != mcp.CodeToolError {
		t.Fatalf("bad arguments: %v", err)
	}
	if got := r.calls.String(); got != "echo\nnote_add\nnote_add\n" {
		t.Fatalf("calls %q", got)
	}
}

func TestFakeChangesEchoOnceTheMutateFileExists(t *testing.T) {
	r := newRig(t)
	c := r.connected()
	if got := descriptions(t, c)["echo"]; got != echoDescription {
		t.Fatalf("echo %q", got)
	}
	if err := os.WriteFile(r.mutate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if got := descriptions(t, c); got["echo"] != mutatedDescription || len(got) != 2 {
			t.Fatalf("tools %v", got)
		}
	}
	if got := answered(t, c, "echo", map[string]any{"text": "secret-text-41"}); got != "secret-text-41" {
		t.Fatalf("the changed echo answered %q", got)
	}
	if n := strings.Count(r.logs.String(), `"msg":"mutated"`); n != 1 {
		t.Fatalf("mutated %d times:\n%s", n, r.logs)
	}
}

func TestFakeNeedsItsTokenAndPath(t *testing.T) {
	r := newRig(t)
	for _, auth := range []string{"", "Bearer wrong", testToken, "Bearer " + testToken + "x"} {
		if err := r.client(testPath, auth).Connect(context.Background()); mcp.Code(err) != mcp.CodeAuth {
			t.Fatalf("%q: %v", auth, err)
		}
	}
	if err := r.client("/mcp", "Bearer "+testToken).Connect(context.Background()); err == nil {
		t.Fatal("connected at another path")
	}
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := r.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("another path answered %d", resp.StatusCode)
	}
	if got := r.calls.String(); got != "" || !strings.Contains(r.logs.String(), `"msg":"unauthorized"`) {
		t.Fatalf("calls %q, log:\n%s", got, r.logs)
	}
}
