package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/opsplan"
)

// fakeServer serves the plan and its one file, recording what was asked.
type fakeServer struct {
	mu    sync.Mutex
	asked []string
	plan  *opsplan.Plan
	file  []byte
	srv   *httptest.Server
}

func newFakeServer(t *testing.T, plan *opsplan.Plan, file []byte) *fakeServer {
	t.Helper()
	f := &fakeServer{plan: plan, file: file}
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/device-plan/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.asked = append(f.asked, r.URL.Path)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(plan)
	})
	mux.HandleFunc("/v2/device-artifact/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.asked = append(f.asked, r.URL.Path)
		f.mu.Unlock()
		_, _ = w.Write(file)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) Get(ctx context.Context, path string) (io.ReadCloser, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.srv.URL+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		_ = resp.Body.Close()
		return nil, io.ErrUnexpectedEOF
	}
	return resp.Body, nil
}

type frames struct {
	mu   sync.Mutex
	seen []*cadencev2.Envelope
}

func (f *frames) send(env *cadencev2.Envelope) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, env)
	return nil
}

func (f *frames) result() *cadencev2.OpsResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.seen {
		if r := e.GetOpsResult(); r != nil {
			return r
		}
	}
	return nil
}

func (f *frames) progressOf(i int) *cadencev2.OpsProgress {
	f.mu.Lock()
	defer f.mu.Unlock()
	var last *cadencev2.OpsProgress
	for _, e := range f.seen {
		if p := e.GetOpsProgress(); p != nil && p.GetIndex() == uint32(i) {
			last = p
		}
	}
	return last
}

func fileSHA(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return hashOf(b)
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func testExecutor(f *fakeServer, out *frames) *Executor {
	e := New(Deps{Get: f, Shell: "/bin/sh", Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	e.SetSend(out.send)
	return e
}

func TestRunFetchesSwapsAndRestarts(t *testing.T) {
	dir := t.TempDir()
	file := []byte("the new daemon\n")
	plan := &opsplan.Plan{
		ID: "yandex-hotswap", RunID: "r1", Board: "sim", TargetVersion: "v2",
		Steps: []opsplan.Step{
			{Name: "stage", Fetch: &opsplan.FetchStep{Src: "artifact:bin/cadenced", Dst: filepath.Join(dir, "bin", "cadenced.new"),
				SHA256: hashOf(file), Mode: 0o755, Size: int64(len(file))}},
			{Name: "mkdir", Exec: &opsplan.ExecStep{Cmd: "mkdir -p " + filepath.Join(dir, "bin") + " && echo made"}},
			{Name: "swap", Exec: &opsplan.ExecStep{Cmd: "mv " + filepath.Join(dir, "bin", "cadenced.new") + " " + filepath.Join(dir, "bin", "cadenced") + " && echo swapped"}},
		},
	}
	// the destination dir must exist before the fetch writes into it: the plan's own first step
	// cannot make it, so the test does (the real profiles stage under an existing tree)
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFakeServer(t, plan, file)
	out := &frames{}
	e := testExecutor(f, out)
	e.Start(context.Background(), "r1")
	deadline := time.Now().Add(5 * time.Second)
	for out.result() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if out.result() == nil {
		t.Fatal("no result frame")
	}
	if code := out.result().GetCode(); code != "" {
		t.Fatalf("result code %q, progress %v", code, out.progressOf(2))
	}
	if got := fileSHA(t, filepath.Join(dir, "bin", "cadenced")); got != hashOf(file) {
		t.Fatalf("swapped file hash %s", got)
	}
	if p := out.progressOf(0); p.GetState() != opsplan.StateDone {
		t.Fatalf("fetch state %s", p.GetState())
	}
	if p := out.progressOf(2); p.GetState() != opsplan.StateDone || !strings.Contains(string(p.GetTail()), "swapped") {
		t.Fatalf("swap progress %+v", p)
	}
}

func TestHashMismatchRollsBack(t *testing.T) {
	dir := t.TempDir()
	bad := []byte("not what the plan says")
	plan := &opsplan.Plan{
		ID: "yandex-hotswap", RunID: "r2", Board: "sim", TargetVersion: "v2",
		Steps: []opsplan.Step{
			{Name: "stage", Fetch: &opsplan.FetchStep{Src: "artifact:bin/cadenced", Dst: filepath.Join(dir, "x.new"),
				SHA256: strings.Repeat("a", 64), Size: int64(len(bad))}},
		},
		Rollback: []opsplan.Step{{Name: "undo", Exec: &opsplan.ExecStep{Cmd: "echo undone"}}},
	}
	f := newFakeServer(t, plan, bad)
	out := &frames{}
	testExecutor(f, out).Start(context.Background(), "r2")
	deadline := time.Now().Add(5 * time.Second)
	for out.result() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	r := out.result()
	if r == nil || r.GetCode() != "rolled_back" {
		t.Fatalf("result %+v", r)
	}
	if p := out.progressOf(0); p.GetState() != opsplan.StateFailed || p.GetErrorCode() != opsplan.ErrHashMismatch {
		t.Fatalf("fetch progress %+v", p)
	}
	if p := out.progressOf(1); p.GetState() != opsplan.StateDone || !strings.Contains(string(p.GetTail()), "undone") {
		t.Fatalf("rollback progress %+v", p)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.new")); !os.IsNotExist(err) {
		t.Fatal("the bad file stayed")
	}
}

func TestSkipIfSameStagesFromTheTree(t *testing.T) {
	dir := t.TempDir()
	cur := []byte("already the target build")
	dst := filepath.Join(dir, "bin", "cadenced")
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, cur, 0o755); err != nil {
		t.Fatal(err)
	}
	plan := &opsplan.Plan{
		ID: "yandex-hotswap", RunID: "r3", Board: "sim", TargetVersion: "v2",
		Steps: []opsplan.Step{
			{Name: "stage", Fetch: &opsplan.FetchStep{Src: "artifact:bin/cadenced", Dst: dst + ".new",
				SHA256: hashOf(cur), SkipIfSame: true}},
		},
	}
	f := newFakeServer(t, plan, []byte("something else"))
	out := &frames{}
	testExecutor(f, out).Start(context.Background(), "r3")
	deadline := time.Now().Add(5 * time.Second)
	for out.result() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if out.result() == nil || out.result().GetCode() != "" {
		t.Fatalf("result %+v", out.result())
	}
	if p := out.progressOf(0); p.GetState() != opsplan.StateSkipped {
		t.Fatalf("state %s", p.GetState())
	}
	if got := fileSHA(t, dst+".new"); got != hashOf(cur) {
		t.Fatal("the staged copy is not the tree's file")
	}
	f.mu.Lock()
	for _, asked := range f.asked {
		if strings.Contains(asked, "device-artifact") {
			t.Fatal("a skipped fetch still downloaded")
		}
	}
	f.mu.Unlock()
}

func TestBusyRefuses(t *testing.T) {
	plan := &opsplan.Plan{ID: "p", RunID: "r4", Board: "sim", TargetVersion: "v",
		Steps: []opsplan.Step{{Name: "x", Exec: &opsplan.ExecStep{Cmd: "true"}}}}
	f := newFakeServer(t, plan, nil)
	out := &frames{}
	e := New(Deps{Get: f, Shell: "/bin/sh", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Busy: func() (string, bool) { return "ring", true }})
	e.SetSend(out.send)
	e.Start(context.Background(), "r4")
	deadline := time.Now().Add(5 * time.Second)
	for out.result() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if r := out.result(); r == nil || r.GetCode() != opsplan.ErrRefusedBusy {
		t.Fatalf("result %+v", r)
	}
}
