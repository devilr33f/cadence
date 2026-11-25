package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/hermes"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

const (
	testKey      = "fake-hermes-test-key"
	instructions = "PRIVATE-INSTRUCTIONS"
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

// rig serves the fake over tls on a clock the test moves, to the server's own
// hermes client.
type rig struct {
	t       *testing.T
	srv     *httptest.Server
	logs    *syncBuf
	release string
	client  *hermes.Client

	mu  sync.Mutex
	now time.Time
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, logs: &syncBuf{}, release: filepath.Join(t.TempDir(), "release"), now: time.Unix(1_800_000_000, 0)}
	r.srv = httptest.NewTLSServer(newServer(config{
		Key: testKey, Release: r.release, Now: r.clock, Log: slog.New(slog.NewJSONHandler(r.logs, nil)),
	}))
	t.Cleanup(r.srv.Close)
	r.client = r.clientWith(testKey)
	// the log never carries the key, an input, a question or a result
	t.Cleanup(func() {
		for _, s := range []string{testKey, instructions, "Cadence E2E"} {
			if strings.Contains(r.logs.String(), s) {
				t.Errorf("log carries %q", s)
			}
		}
	})
	return r
}

func (r *rig) clock() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now
}

func (r *rig) advance(d time.Duration) {
	r.mu.Lock()
	r.now = r.now.Add(d)
	r.mu.Unlock()
}

func (r *rig) clientWith(key string) *hermes.Client {
	roots := x509.NewCertPool()
	roots.AddCert(r.srv.Certificate())
	c, err := hermes.NewClient(hermes.ClientConfig{BaseURL: r.srv.URL, APIKey: key, HTTP: httpx.Client(roots)})
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

func body(word string) []byte {
	b, _ := json.Marshal(map[string]string{
		"input": word + " " + instructions, "instructions": "fixed", "session_id": "cadence-" + word,
	})
	return b
}

func (r *rig) create(word string) string {
	r.t.Helper()
	id, err := r.client.CreateRun(context.Background(), body(word), "key-"+word)
	if err != nil {
		r.t.Fatalf("create %s: %v", word, err)
	}
	return id
}

func (r *rig) get(id string) hermes.Run {
	r.t.Helper()
	run, err := r.client.GetRun(context.Background(), id)
	if err != nil {
		r.t.Fatalf("get %s: %v", id, err)
	}
	return run
}

func (r *rig) want(id, status, output string) hermes.Run {
	r.t.Helper()
	run := r.get(id)
	if run.Status != status || run.Output != output {
		r.t.Fatalf("run %s: status %q output %q, want %q %q", id, run.Status, run.Output, status, output)
	}
	return run
}

func TestProbeTakesTheKeyOnly(t *testing.T) {
	r := newRig(t)
	if err := r.client.Probe(context.Background()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if err := r.clientWith("another-key").Probe(context.Background()); err == nil {
		t.Fatal("a wrong key passed the probe")
	}
	req, _ := http.NewRequest(http.MethodGet, r.srv.URL+"/v1/capabilities", nil)
	resp, err := r.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no key: %d", resp.StatusCode)
	}
}

func TestFinishIsIdempotent(t *testing.T) {
	r := newRig(t)
	id := r.create("finish")
	if again := r.create("finish"); again != id {
		t.Fatalf("replay made run %s besides %s", again, id)
	}
	if _, err := r.client.CreateRun(context.Background(), body("finish-other"), "key-finish"); hermes.Code(err) != hermes.CodeConflict {
		t.Fatalf("another body under the key: %v", err)
	}
	if run := r.want(id, "running", ""); run.SessionID != "cadence-finish" {
		t.Fatalf("session %q", run.SessionID)
	}
	r.advance(finishAfter)
	r.want(id, "completed", resultText)
}

func TestApprovalOnceOrDeny(t *testing.T) {
	for decision, output := range map[string]string{"once": approvedText, "deny": deniedText} {
		t.Run(decision, func(t *testing.T) {
			r := newRig(t)
			id := r.create("approve")
			r.want(id, "running", "")
			r.advance(askAfter)
			run := r.want(id, "running", "")
			if run.Phase != "waiting_for_approval" || len(run.Controls) != 1 {
				t.Fatalf("phase %q, controls %+v", run.Phase, run.Controls)
			}
			c := run.Controls[0]
			if c.Kind != "approval" || c.Description != approvalDescription || c.Command != approvalCommand {
				t.Fatalf("control %+v", c)
			}
			ctx := context.Background()
			if err := r.client.Clarify(ctx, id, c.PromptID, c.Generation, "words"); hermes.Code(err) != hermes.CodeConflict {
				t.Fatalf("clarify answer to an approval: %v", err)
			}
			if err := r.client.Approve(ctx, id, c.PromptID, c.Generation+1, decision); hermes.Code(err) != hermes.CodeConflict {
				t.Fatalf("another generation: %v", err)
			}
			if err := r.client.Approve(ctx, id, c.PromptID, c.Generation, decision); err != nil {
				t.Fatalf("approve: %v", err)
			}
			if err := r.client.Approve(ctx, id, c.PromptID, c.Generation, decision); hermes.Code(err) != hermes.CodeConflict {
				t.Fatalf("answered twice: %v", err)
			}
			if run := r.want(id, "running", ""); len(run.Controls) != 0 {
				t.Fatalf("answered prompt still pending: %+v", run.Controls)
			}
			r.advance(answerToEnd)
			r.want(id, "completed", output)
		})
	}
}

func TestClarifyAnswered(t *testing.T) {
	r := newRig(t)
	id := r.create("ask")
	r.advance(askAfter)
	run := r.want(id, "running", "")
	if len(run.Controls) != 1 || run.Controls[0].Kind != "clarify" || run.Controls[0].Question != clarifyQuestion {
		t.Fatalf("controls %+v", run.Controls)
	}
	c := run.Controls[0]
	if err := r.client.Approve(context.Background(), id, c.PromptID, c.Generation, "once"); hermes.Code(err) != hermes.CodeConflict {
		t.Fatalf("approval answer to a question: %v", err)
	}
	if err := r.client.Clarify(context.Background(), id, c.PromptID, c.Generation, "the first"); err != nil {
		t.Fatalf("clarify: %v", err)
	}
	r.advance(answerToEnd)
	r.want(id, "completed", answeredText)
}

func TestHoldUntilReleased(t *testing.T) {
	r := newRig(t)
	id := r.create("hold")
	r.advance(time.Hour)
	r.want(id, "running", "")
	if err := os.WriteFile(r.release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r.want(id, "completed", resultText)
}

func TestStopCancels(t *testing.T) {
	r := newRig(t)
	id := r.create("hold")
	status, err := r.client.StopRun(context.Background(), id)
	if err != nil || status != "cancelled" {
		t.Fatalf("stop: %q %v", status, err)
	}
	r.want(id, "cancelled", "")
	if _, err := r.client.StopRun(context.Background(), "run-404"); hermes.Code(err) != hermes.CodeGone {
		t.Fatalf("stop of no run: %v", err)
	}
	if _, err := r.client.GetRun(context.Background(), "run-404"); hermes.Code(err) != hermes.CodeGone {
		t.Fatalf("poll of no run: %v", err)
	}
}

func TestOtherWordsFail(t *testing.T) {
	r := newRig(t)
	id := r.create("explode")
	r.want(id, "running", "")
	r.advance(askAfter)
	r.want(id, "failed", "")
}
