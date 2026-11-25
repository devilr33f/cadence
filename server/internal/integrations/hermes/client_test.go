package hermes_test

import (
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/hermes"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if got := hermes.Code(err); got != code {
		t.Fatalf("code %q (%v), want %q", got, err, code)
	}
}

func TestNewClientTakesOnlyPlainHTTPSBases(t *testing.T) {
	for _, ok := range []string{"https://h.example", "https://h.example/p/", "https://h.example:8443/a/b"} {
		if _, err := hermes.NewClient(hermes.ClientConfig{BaseURL: ok, APIKey: "k"}); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "http://h.example", "ftp://h.example", "https://", "https://u:pw@h.example", "https://h.example?x=1",
		"https://h.example/#f", "https://h.example/a/../b", "https://h.example/./b", "h.example",
	} {
		_, err := hermes.NewClient(hermes.ClientConfig{BaseURL: bad, APIKey: "k"})
		if !errors.Is(err, hermes.ErrBadURL) || (bad != "" && strings.Contains(err.Error(), bad)) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := hermes.NewClient(hermes.ClientConfig{BaseURL: "https://h.example"}); err == nil {
		t.Error("no key accepted")
	}
}

func TestProbe(t *testing.T) {
	f := newFake(t)
	c := f.client(t, time.Second)
	if err := c.Probe(bg()); err != nil {
		t.Fatal(err)
	}
	if got := f.callsTo("GET", "/v1/capabilities"); len(got) != 1 || got[0].Path != "/v1/capabilities" {
		t.Fatalf("calls %+v", got)
	}
	vetoes := map[string]string{
		"no status route":       strings.Replace(capsOK, `"run_status"`, `"run_state"`, 1),
		"wrong stop method":     strings.Replace(capsOK, `{"method":"POST","path":"/v1/runs/{run_id}/stop"}`, `{"method":"GET","path":"/v1/runs/{run_id}/stop"}`, 1),
		"no unknown resolution": strings.Replace(capsOK, `"run_unknown_resolution":true`, `"run_unknown_resolution":"yes"`, 1),
		"keys not durable":      strings.Replace(capsOK, `"durable":true`, `"durable":false`, 1),
		"approvals off":         strings.Replace(capsOK, `"run_approval_response":true`, `"run_approval_response":false`, 1),
		"clarify off":           strings.Replace(capsOK, `"features":{`, `"features":{"run_clarify":false,`, 1),
		"clarify moved":         strings.Replace(capsOK, `"/v1/runs/{run_id}/clarify"`, `"/v1/clarify"`, 1),
		"no idempotency":        strings.Replace(capsOK, `"runs_idempotency":{"supported":true,"durable":true,"retention_seconds":86400}`, `"x":1`, 1),
		"not json":              leak,
	}
	for name, doc := range vetoes {
		f.mu.Lock()
		f.caps = doc
		f.mu.Unlock()
		err := c.Probe(bg())
		if hermes.Code(err) != hermes.CodeIncompatible {
			t.Errorf("%s: %v", name, err)
		}
	}
	f.setOverride(func(string, string) (int, string, bool) { return http.StatusServiceUnavailable, leak, true })
	wantCode(t, c.Probe(bg()), hermes.CodeUnavailable)
	f.setOverride(func(string, string) (int, string, bool) { return http.StatusNotFound, leak, true })
	wantCode(t, c.Probe(bg()), hermes.CodeIncompatible)
}

func TestRedirectRefused(t *testing.T) {
	var hit atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit.Add(1) }))
	defer other.Close()
	f := newFake(t)
	f.setOverride(func(string, string) (int, string, bool) { return 0, "", false })
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	roots := f.roots()
	roots.AddCert(srv.Certificate())
	roots.AddCert(other.Certificate())
	c, err := hermes.NewClient(hermes.ClientConfig{BaseURL: srv.URL, APIKey: apiKey, HTTP: httpx.Client(roots)})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Probe(bg())
	wantCode(t, err, hermes.CodeUnavailable)
	if _, err := c.GetRun(bg(), "run-1"); err == nil {
		t.Fatal("redirected get accepted")
	}
	if hit.Load() != 0 {
		t.Fatal("the redirect was followed")
	}
	if strings.Contains(err.Error(), other.URL) || strings.Contains(err.Error(), apiKey) {
		t.Fatalf("error names the target: %v", err)
	}
}

func TestUntrustedTLS(t *testing.T) {
	f := newFake(t)
	c, err := hermes.NewClient(hermes.ClientConfig{BaseURL: f.url(), APIKey: apiKey, HTTP: httpx.Client(nil)})
	if err != nil {
		t.Fatal(err)
	}
	wantCode(t, c.Probe(bg()), hermes.CodeUnavailable)
	if len(f.callsTo("GET", "/v1/capabilities")) != 0 {
		t.Fatal("request reached an untrusted server")
	}
}

func TestCreateRun(t *testing.T) {
	f := newFake(t)
	c := f.client(t, time.Second)
	body := []byte(`{"input":"x","instructions":"y","session_id":"cadence-1"}`)
	id, err := c.CreateRun(bg(), body, "key-1")
	if err != nil || id != "run-1" {
		t.Fatalf("%q %v", id, err)
	}
	again, err := c.CreateRun(bg(), body, "key-1")
	if err != nil || again != id || f.runCount() != 1 {
		t.Fatalf("replay %q %v runs %d", again, err, f.runCount())
	}
	got := f.callsTo("POST", "/v1/runs")
	if len(got) != 2 || got[0].Key != "key-1" || got[0].Body["session_id"] != "cadence-1" {
		t.Fatalf("calls %+v", got)
	}
	codes := map[int]string{
		http.StatusConflict: hermes.CodeConflict, http.StatusBadRequest: hermes.CodeRefused,
		http.StatusUnprocessableEntity: hermes.CodeRefused, http.StatusTooManyRequests: hermes.CodeUnavailable,
		http.StatusBadGateway: hermes.CodeUnavailable, http.StatusOK: hermes.CodeUnavailable,
	}
	for st, code := range codes {
		f.setOverride(func(string, string) (int, string, bool) { return st, `{"detail":"` + leak + `"}`, true })
		_, err := c.CreateRun(bg(), body, "key-2")
		wantCode(t, err, code)
	}
}

func TestCreateRunTimeout(t *testing.T) {
	f := newFake(t)
	f.stallCreates, f.stall = 1, 300*time.Millisecond
	_, err := f.client(t, 50*time.Millisecond).CreateRun(bg(), []byte(`{}`), "k")
	wantCode(t, err, hermes.CodeTimeout)
}

// W0-C item 9b stage 1: the poll's current tool, read when it is a plain name
func TestGetRunReadsTheCurrentTool(t *testing.T) {
	f := newFake(t)
	c := f.client(t, time.Second)
	id, _ := c.CreateRun(bg(), []byte(`{"session_id":"cadence-9"}`), "k")
	f.set(id, func(r *fakeRun) {
		r.Status, r.Extra = "running", map[string]any{"tool": "terminal", "progress_at": 1763123456.5}
	})
	run, err := c.GetRun(bg(), id)
	if err != nil || run.Tool != "terminal" || !run.ProgressAt.Equal(time.Unix(1763123456, 5e8)) {
		t.Fatalf("run %+v %v", run, err)
	}
	for _, odd := range []any{"rm -rf /", map[string]any{}, 7, ""} {
		f.set(id, func(r *fakeRun) { r.Extra = map[string]any{"tool": odd, "progress_at": "soon"} })
		run, err := c.GetRun(bg(), id)
		if err != nil || run.Tool != "" || !run.ProgressAt.IsZero() {
			t.Fatalf("tool %v: %+v %v", odd, run, err)
		}
	}
}

func TestGetRun(t *testing.T) {
	f := newFake(t)
	c := f.client(t, time.Second)
	id, _ := c.CreateRun(bg(), []byte(`{"session_id":"cadence-9"}`), "k")
	f.set(id, func(r *fakeRun) {
		r.Status, r.Output = "waiting_for_approval", "half"
		r.Controls = []map[string]any{approval("p1", 2, "delete pod", "kubectl delete pod x"), clarify("p2", 0, "which?", "a", "b")}
	})
	run, err := c.GetRun(bg(), id)
	if err != nil {
		t.Fatal(err)
	}
	if run.ID != id || run.Status != "running" || run.Phase != "waiting_for_approval" || run.SessionID != "cadence-9" ||
		run.Output != "half" || len(run.Controls) != 2 {
		t.Fatalf("run %+v", run)
	}
	a, q := run.Controls[0], run.Controls[1]
	if a.Kind != "approval" || a.PromptID != "p1" || a.Generation != 2 || a.Command != "kubectl delete pod x" ||
		a.Description != "delete pod" || q.Kind != "clarify" || q.Question != "which?" || len(q.Choices) != 2 {
		t.Fatalf("controls %+v %+v", a, q)
	}
	for raw, want := range map[string]string{"queued": "running", "stopping": "running", "completed": "completed", "failed": "failed", "cancelled": "cancelled", "interrupted": "interrupted"} {
		f.set(id, func(r *fakeRun) { r.Status, r.Controls = raw, nil })
		if run, err := c.GetRun(bg(), id); err != nil || run.Status != want || run.Phase != raw {
			t.Errorf("%s: %+v %v", raw, run, err)
		}
	}
	f.set(id, func(r *fakeRun) { r.Status = "unknown" })
	_, err = c.GetRun(bg(), id)
	wantCode(t, err, hermes.CodeUnknown)
	f.set(id, func(r *fakeRun) {
		r.Status = "running"
		r.Controls = []map[string]any{{"kind": "clarify", "prompt_id": "p", "execution_generation": 1, "question": "q"}}
	})
	_, err = c.GetRun(bg(), id)
	wantCode(t, err, hermes.CodeIncompatible)
	f.set(id, func(r *fakeRun) { r.Controls = []map[string]any{approval("p", -1, "d", "c")} })
	_, err = c.GetRun(bg(), id)
	wantCode(t, err, hermes.CodeIncompatible)
	f.setOverride(func(_, path string) (int, string, bool) {
		return http.StatusOK, `{"run_id":"run-1","status":"completed","partial":true}`, strings.HasPrefix(path, "/v1/runs/")
	})
	_, err = c.GetRun(bg(), id)
	wantCode(t, err, hermes.CodeIncompatible)
	f.setOverride(func(string, string) (int, string, bool) {
		return http.StatusOK, `{"run_id":"run-2","status":"running"}`, true
	})
	_, err = c.GetRun(bg(), id)
	wantCode(t, err, hermes.CodeIncompatible)
	// a body past the cap is never parsed
	huge := `{"run_id":"run-1","status":"completed","output":"` + strings.Repeat("x", 300<<10) + `"}`
	f.setOverride(func(string, string) (int, string, bool) { return http.StatusOK, huge, true })
	_, err = c.GetRun(bg(), id)
	wantCode(t, err, hermes.CodeIncompatible)
	f.setOverride(nil)
	f.forget(id)
	_, err = c.GetRun(bg(), id)
	wantCode(t, err, hermes.CodeGone)
}

func TestStopAndControls(t *testing.T) {
	f := newFake(t)
	c := f.client(t, time.Second)
	id, _ := c.CreateRun(bg(), []byte(`{}`), "k")
	f.set(id, func(r *fakeRun) {
		r.Status = "running"
		r.Controls = []map[string]any{clarify("p1", 1, "which?"), approval("p2", 3, "d", "c")}
	})
	if err := c.Clarify(bg(), id, "p1", 1, "blue"); err != nil {
		t.Fatal(err)
	}
	got := f.callsTo("POST", "/clarify")
	if len(got) != 1 || got[0].Body["request_id"] != "p1" || got[0].Body["execution_generation"] != float64(1) || got[0].Body["answer"] != "blue" {
		t.Fatalf("clarify %+v", got)
	}
	wantCode(t, c.Clarify(bg(), id, "p1", 1, "blue"), hermes.CodeConflict)
	if err := c.Approve(bg(), id, "p2", 3, "deny"); err != nil {
		t.Fatal(err)
	}
	if got := f.callsTo("POST", "/approval"); len(got) != 1 || got[0].Body["choice"] != "deny" || got[0].Body["request_id"] != "p2" {
		t.Fatalf("approval %+v", got)
	}
	f.setOverride(func(string, string) (int, string, bool) {
		return http.StatusOK, `{"status":"already_resolved","prompt_id":"p9"}`, true
	})
	wantCode(t, c.Approve(bg(), id, "p9", 1, "once"), hermes.CodeConflict)
	f.setOverride(func(string, string) (int, string, bool) {
		return http.StatusOK, `{"status":"resolved","prompt_id":"other"}`, true
	})
	wantCode(t, c.Approve(bg(), id, "p9", 1, "once"), hermes.CodeUnavailable)
	f.setOverride(func(string, string) (int, string, bool) { return http.StatusBadRequest, leak, true })
	wantCode(t, c.Clarify(bg(), id, "p9", 1, "x"), hermes.CodeRefused)
	f.setOverride(nil)

	status, err := c.StopRun(bg(), id)
	if err != nil || status != "cancelled" {
		t.Fatalf("stop %q %v", status, err)
	}
	f.setOverride(func(string, string) (int, string, bool) { return http.StatusInternalServerError, leak, true })
	_, err = c.StopRun(bg(), id)
	wantCode(t, err, hermes.CodeUnknown)
	f.setOverride(nil)
	f.forget(id)
	_, err = c.StopRun(bg(), id)
	wantCode(t, err, hermes.CodeGone)
	wantCode(t, c.Clarify(bg(), id, "p1", 1, "x"), hermes.CodeGone)
}

func TestErrorsCarryNoUpstreamText(t *testing.T) {
	f := newFake(t)
	c := f.client(t, time.Second)
	for _, st := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusConflict, http.StatusInternalServerError, http.StatusOK} {
		f.setOverride(func(string, string) (int, string, bool) {
			return st, `{"error":"` + leak + `","run_id":"` + leak + `"}`, true
		})
		errs := []error{c.Probe(bg()), c.Clarify(bg(), "r", "p", 1, "a"), c.Approve(bg(), "r", "p", 1, "once")}
		_, err := c.CreateRun(bg(), []byte(`{}`), "k")
		errs = append(errs, err)
		_, err = c.GetRun(bg(), "r")
		errs = append(errs, err)
		_, err = c.StopRun(bg(), "r")
		errs = append(errs, err)
		for _, err := range errs {
			if err != nil && (strings.Contains(err.Error(), leak) || strings.Contains(err.Error(), apiKey) || strings.Contains(err.Error(), f.url())) {
				t.Fatalf("%d: error carries upstream text: %v", st, err)
			}
		}
	}
}

func TestBrokenBodyIsNoIncompatibility(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// the connection drops halfway through the declared body
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"run_id":"run-1","sta`))
	}))
	defer srv.Close()
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	c, err := hermes.NewClient(hermes.ClientConfig{BaseURL: srv.URL, APIKey: apiKey, HTTP: httpx.Client(roots)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.GetRun(bg(), "run-1")
	wantCode(t, err, hermes.CodeUnavailable)
	wantCode(t, c.Probe(bg()), hermes.CodeUnavailable)
}
