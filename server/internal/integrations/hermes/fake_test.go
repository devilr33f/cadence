package hermes_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/hermes"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

const (
	apiKey = "hermes-test-key-do-not-print"
	leak   = "LEAKY-UPSTREAM-TEXT"
	prefix = "/p/cadence"
)

const capsOK = `{"object":"hermes.api_server.capabilities","features":{"run_unknown_resolution":true,` +
	`"run_approval_response":true,"runs_idempotency":{"supported":true,"durable":true,"retention_seconds":86400}},` +
	`"endpoints":{"runs":{"method":"POST","path":"/v1/runs"},"run_status":{"method":"GET","path":"/v1/runs/{run_id}"},` +
	`"run_stop":{"method":"POST","path":"/v1/runs/{run_id}/stop"},` +
	`"run_approval":{"method":"POST","path":"/v1/runs/{run_id}/approval"},` +
	`"run_clarify":{"method":"POST","path":"/v1/runs/{run_id}/clarify"}}}`

type fakeRun struct {
	ID, Session, Status, Output string
	Controls                    []map[string]any
	// Extra are more fields of the poll's answer (tool, progress_at)
	Extra map[string]any
}

type call struct {
	Method, Path, Key string
	Body              map[string]any
}

// fakeHermes is a Runs API server over TLS with scripted run lifecycles: a
// test moves a run with set; stops, clarify and approval answers land the way
// Hermes takes them.
type fakeHermes struct {
	t   *testing.T
	srv *httptest.Server

	mu    sync.Mutex
	caps  string
	runs  map[string]*fakeRun
	byKey map[string]string
	calls []call
	n     int
	order []string
	// the first stallCreates creates make their run, then answer after stall
	stallCreates int
	stall        time.Duration
	// while gate is set, a GET waits on it
	gate                  chan struct{}
	inflight, maxInflight int
	// override answers a request first when it returns ok
	override func(method, path string) (status int, body string, ok bool)
	// stopTo is the status a stop leaves; "" keeps the run going
	stopTo string
}

func newFake(t *testing.T) *fakeHermes {
	f := &fakeHermes{t: t, caps: capsOK, runs: map[string]*fakeRun{}, byKey: map[string]string{}, stopTo: "cancelled"}
	f.srv = httptest.NewTLSServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHermes) url() string { return f.srv.URL + prefix }

func (f *fakeHermes) roots() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(f.srv.Certificate())
	return p
}

func (f *fakeHermes) client(t *testing.T, timeout time.Duration) *hermes.Client {
	t.Helper()
	c, err := hermes.NewClient(hermes.ClientConfig{BaseURL: f.url(), APIKey: apiKey, HTTP: httpx.Client(f.roots()), Timeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *fakeHermes) set(id string, fn func(r *fakeRun)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[id]
	if !ok {
		f.t.Errorf("no run %s", id)
		return
	}
	fn(r)
}

func (f *fakeHermes) forget(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.runs, id)
}

func (f *fakeHermes) setOverride(o func(method, path string) (int, string, bool)) {
	f.mu.Lock()
	f.override = o
	f.mu.Unlock()
}

// callsTo lists the recorded calls whose path ends in suffix, by method.
func (f *fakeHermes) callsTo(method, suffix string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.Method == method && strings.HasSuffix(c.Path, suffix) {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeHermes) runCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

func (f *fakeHermes) peak() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxInflight
}

func reply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func (f *fakeHermes) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	path := strings.TrimPrefix(r.URL.Path, prefix)
	f.mu.Lock()
	f.calls = append(f.calls, call{Method: r.Method, Path: path, Key: r.Header.Get("Idempotency-Key"), Body: body})
	override := f.override
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+apiKey {
		reply(w, http.StatusUnauthorized, `{"error":"`+leak+`"}`)
		return
	}
	if override != nil {
		if st, b, ok := override(r.Method, path); ok {
			reply(w, st, b)
			return
		}
	}
	parts := strings.Split(strings.TrimPrefix(path, "/v1/"), "/")
	switch {
	case r.Method == http.MethodGet && path == "/v1/capabilities":
		f.mu.Lock()
		caps := f.caps
		f.mu.Unlock()
		reply(w, http.StatusOK, caps)
	case r.Method == http.MethodPost && path == "/v1/runs":
		f.create(w, r.Header.Get("Idempotency-Key"), body)
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "runs":
		f.get(w, parts[1])
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "runs" && parts[2] == "stop":
		f.stop(w, parts[1])
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "runs" && (parts[2] == "clarify" || parts[2] == "approval"):
		f.answer(w, parts[1], body)
	default:
		reply(w, http.StatusNotFound, `{"error":"`+leak+`"}`)
	}
}

func (f *fakeHermes) create(w http.ResponseWriter, key string, body map[string]any) {
	f.mu.Lock()
	id, replay := f.byKey[key]
	if !replay {
		f.n++
		id = fmt.Sprintf("run-%d", f.n)
		session, _ := body["session_id"].(string)
		f.runs[id] = &fakeRun{ID: id, Session: session, Status: "queued"}
		f.byKey[key] = id
		f.order = append(f.order, id)
	}
	status := f.runs[id].Status
	stall := time.Duration(0)
	if f.stallCreates > 0 {
		f.stallCreates--
		stall = f.stall
	}
	f.mu.Unlock()
	time.Sleep(stall)
	reply(w, http.StatusAccepted, mustJSON(map[string]any{"run_id": id, "status": status}))
}

func (f *fakeHermes) get(w http.ResponseWriter, id string) {
	f.mu.Lock()
	f.inflight++
	f.maxInflight = max(f.maxInflight, f.inflight)
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-time.After(5 * time.Second):
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inflight--
	run, ok := f.runs[id]
	if !ok {
		reply(w, http.StatusNotFound, `{"error":"no such run `+leak+`"}`)
		return
	}
	controls := run.Controls
	if controls == nil {
		controls = []map[string]any{}
	}
	body := map[string]any{
		"run_id": run.ID, "status": run.Status, "session_id": run.Session, "output": run.Output,
		"pending_controls": controls,
	}
	for k, v := range run.Extra {
		body[k] = v
	}
	reply(w, http.StatusOK, mustJSON(body))
}

func (f *fakeHermes) stop(w http.ResponseWriter, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[id]
	if !ok {
		reply(w, http.StatusNotFound, `{"error":"`+leak+`"}`)
		return
	}
	if f.stopTo != "" && run.Status != "completed" && run.Status != "failed" && run.Status != "cancelled" && run.Status != "interrupted" {
		run.Status = f.stopTo
		run.Controls = nil
	}
	reply(w, http.StatusAccepted, mustJSON(map[string]any{"status": run.Status, "run_id": run.ID}))
}

func (f *fakeHermes) answer(w http.ResponseWriter, id string, body map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[id]
	if !ok {
		reply(w, http.StatusNotFound, `{"error":"`+leak+`"}`)
		return
	}
	for i, c := range run.Controls {
		if c["prompt_id"] == body["request_id"] && fmt.Sprint(c["execution_generation"]) == fmt.Sprint(body["execution_generation"]) {
			run.Controls = append(run.Controls[:i:i], run.Controls[i+1:]...)
			reply(w, http.StatusOK, mustJSON(map[string]any{"status": "resolved", "prompt_id": c["prompt_id"], "run_id": id}))
			return
		}
	}
	reply(w, http.StatusConflict, `{"error":"`+leak+`"}`)
}

func clarify(prompt string, gen int, question string, choices ...string) map[string]any {
	if choices == nil {
		choices = []string{}
	}
	return map[string]any{
		"kind": "clarify", "prompt_id": prompt, "execution_generation": gen,
		"question": question, "choices": choices, "multi_select": false,
	}
}

func approval(prompt string, gen int, description, command string) map[string]any {
	return map[string]any{
		"kind": "approval", "prompt_id": prompt, "execution_generation": gen,
		"command": command, "description": description, "choices": []string{"once", "session", "deny"},
	}
}

// syncBuf collects logs from many goroutines.
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

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func bg() context.Context { return context.Background() }

// latest is the newest run of a session.
func (f *fakeHermes) latest(session string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.order) - 1; i >= 0; i-- {
		if r, ok := f.runs[f.order[i]]; ok && r.Session == session {
			return r.ID
		}
	}
	return ""
}
