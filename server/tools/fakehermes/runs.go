package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// what the runs say: the e2e looks for these in the admin api and in no log
const (
	resultText          = "Cadence E2E task result."
	approvedText        = "Cadence E2E task approved result."
	deniedText          = "Cadence E2E task denied result."
	answeredText        = "Cadence E2E task answered result."
	approvalDescription = "Cadence E2E task question: restart the e2e service."
	approvalCommand     = "Cadence E2E task command"
	clarifyQuestion     = "Cadence E2E task question: which one?"
)

const (
	finishAfter = 3 * time.Second
	// askAfter is when a prompting run asks, and when a failing one fails
	askAfter = time.Second
	// answerToEnd is how long a run goes on after its prompt was answered
	answerToEnd = 2 * time.Second
	maxBody     = 64 << 10
)

const capabilities = `{"object":"hermes.api_server.capabilities","features":{"run_unknown_resolution":true,` +
	`"run_clarify":true,"run_clarify_response":true,"run_approval_response":true,` +
	`"runs_idempotency":{"supported":true,"durable":true,"retention_seconds":86400}},` +
	`"endpoints":{"runs":{"method":"POST","path":"/v1/runs"},"run_status":{"method":"GET","path":"/v1/runs/{run_id}"},` +
	`"run_stop":{"method":"POST","path":"/v1/runs/{run_id}/stop"},` +
	`"run_approval":{"method":"POST","path":"/v1/runs/{run_id}/approval"},` +
	`"run_clarify":{"method":"POST","path":"/v1/runs/{run_id}/clarify"}}}`

type config struct {
	// Key is the bearer key every request must carry
	Key string
	// Release is the file whose existence ends hold runs; "" never
	Release string
	Now     func() time.Time
	Log     *slog.Logger
}

// run is one run, scripted by the first word of its input: finish ends after
// 3 s; approve asks an approval and ask a question after 1 s, and end 2 s
// after the answer; hold runs until the release file exists; any other word
// fails after 1 s.
type run struct {
	id, session, script string
	created             time.Time
	// answered is when its prompt was answered, choice the approval's answer
	answered time.Time
	choice   string
	released bool
	stopped  bool
}

func (r *run) prompt() string {
	switch r.script {
	case "approve":
		return "approval"
	case "ask":
		return "clarify"
	}
	return ""
}

func (r *run) promptID() string { return "prompt-" + r.id }

type server struct {
	c config

	mu    sync.Mutex
	runs  map[string]*run
	byKey map[string]idempotent
	n     int
}

// idempotent is what a key made: its run and the body's hash.
type idempotent struct {
	run  string
	body [sha256.Size]byte
}

func newServer(c config) *server {
	return &server{c: c, runs: map[string]*run{}, byKey: map[string]idempotent{}}
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func refuse(w http.ResponseWriter, status int, code string) {
	reply(w, status, map[string]string{"error": code})
}

// ServeHTTP logs each request by its route, its run id and its status only:
// inputs, prompts, answers and results never reach the log.
func (s *server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	route, id, status := s.serve(w, req)
	s.c.Log.Info("request", "method", req.Method, "route", route, "run", id, "status", status)
}

func (s *server) serve(w http.ResponseWriter, req *http.Request) (route, id string, status int) {
	if req.Header.Get("Authorization") != "Bearer "+s.c.Key {
		refuse(w, http.StatusUnauthorized, "unauthorized")
		return "", "", http.StatusUnauthorized
	}
	raw, err := io.ReadAll(io.LimitReader(req.Body, maxBody+1))
	if err != nil || len(raw) > maxBody {
		refuse(w, http.StatusBadRequest, "bad_body")
		return "", "", http.StatusBadRequest
	}
	parts := strings.Split(strings.TrimPrefix(req.URL.Path, "/v1/"), "/")
	switch {
	case req.Method == http.MethodGet && req.URL.Path == "/v1/capabilities":
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, capabilities)
		return "capabilities", "", http.StatusOK
	case req.Method == http.MethodPost && req.URL.Path == "/v1/runs":
		id, status := s.create(w, req.Header.Get("Idempotency-Key"), raw)
		return "create", id, status
	case len(parts) < 2 || parts[0] != "runs":
	case req.Method == http.MethodGet && len(parts) == 2:
		return "status", parts[1], s.status(w, parts[1])
	case req.Method == http.MethodPost && len(parts) == 3 && parts[2] == "stop":
		return "stop", parts[1], s.stop(w, parts[1])
	case req.Method == http.MethodPost && len(parts) == 3 && (parts[2] == "approval" || parts[2] == "clarify"):
		return parts[2], parts[1], s.answer(w, parts[1], parts[2], raw)
	}
	refuse(w, http.StatusNotFound, "not_found")
	return "unknown", "", http.StatusNotFound
}

func (s *server) create(w http.ResponseWriter, key string, raw []byte) (string, int) {
	var b struct {
		Input        *string `json:"input"`
		Instructions *string `json:"instructions"`
		SessionID    *string `json:"session_id"`
	}
	if key == "" || len(key) > 256 || json.Unmarshal(raw, &b) != nil || b.Input == nil || b.Instructions == nil ||
		b.SessionID == nil || strings.TrimSpace(*b.Input) == "" {
		refuse(w, http.StatusBadRequest, "bad_request")
		return "", http.StatusBadRequest
	}
	sum := sha256.Sum256(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	if made, ok := s.byKey[key]; ok {
		if made.body != sum {
			refuse(w, http.StatusConflict, "idempotency_conflict")
			return made.run, http.StatusConflict
		}
		r := s.runs[made.run]
		status, _, _ := s.snapshot(r)
		reply(w, http.StatusAccepted, map[string]string{"run_id": r.id, "status": status})
		return r.id, http.StatusAccepted
	}
	s.n++
	r := &run{id: fmt.Sprintf("run-%d", s.n), session: *b.SessionID, script: strings.ToLower(strings.Fields(*b.Input)[0]), created: s.c.Now()}
	s.runs[r.id] = r
	s.byKey[key] = idempotent{run: r.id, body: sum}
	reply(w, http.StatusAccepted, map[string]string{"run_id": r.id, "status": "queued"})
	return r.id, http.StatusAccepted
}

// snapshot is the run's status, output and pending controls now, under s.mu.
func (s *server) snapshot(r *run) (string, string, []map[string]any) {
	now := s.c.Now()
	switch {
	case r.stopped:
		return "cancelled", "", nil
	case r.script == "finish":
		if now.Sub(r.created) < finishAfter {
			return "running", "", nil
		}
		return "completed", resultText, nil
	case r.script == "hold":
		if !r.released && s.c.Release != "" {
			_, err := os.Stat(s.c.Release)
			r.released = err == nil
		}
		if !r.released {
			return "running", "", nil
		}
		return "completed", resultText, nil
	case now.Sub(r.created) < askAfter:
		return "running", "", nil
	case r.prompt() == "":
		return "failed", "", nil
	case r.answered.IsZero():
		return s.asking(r)
	case now.Sub(r.answered) < answerToEnd:
		return "running", "", nil
	case r.prompt() == "clarify":
		return "completed", answeredText, nil
	case r.choice == "deny":
		return "completed", deniedText, nil
	}
	return "completed", approvedText, nil
}

func (s *server) asking(r *run) (string, string, []map[string]any) {
	c := map[string]any{"kind": r.prompt(), "prompt_id": r.promptID(), "execution_generation": 1}
	if r.prompt() == "clarify" {
		c["question"], c["choices"], c["multi_select"] = clarifyQuestion, []string{"the first", "the second"}, false
		return "running", "", []map[string]any{c}
	}
	c["description"], c["command"], c["choices"] = approvalDescription, approvalCommand, []string{"once", "session", "deny"}
	return "waiting_for_approval", "", []map[string]any{c}
}

func (s *server) status(w http.ResponseWriter, id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		refuse(w, http.StatusNotFound, "not_found")
		return http.StatusNotFound
	}
	status, output, controls := s.snapshot(r)
	if controls == nil {
		controls = []map[string]any{}
	}
	reply(w, http.StatusOK, map[string]any{
		"run_id": r.id, "status": status, "session_id": r.session, "output": output, "pending_controls": controls,
	})
	return http.StatusOK
}

func (s *server) stop(w http.ResponseWriter, id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		refuse(w, http.StatusNotFound, "not_found")
		return http.StatusNotFound
	}
	switch status, _, _ := s.snapshot(r); status {
	case "completed", "failed", "cancelled":
	default:
		r.stopped = true
	}
	status, _, _ := s.snapshot(r)
	reply(w, http.StatusAccepted, map[string]string{"status": status, "run_id": r.id})
	return http.StatusAccepted
}

// answer takes the answer to the run's pending prompt of that kind and
// generation; an answer to a prompt answered already is already_resolved.
func (s *server) answer(w http.ResponseWriter, id, kind string, raw []byte) int {
	var b struct {
		RequestID  string  `json:"request_id"`
		Generation float64 `json:"execution_generation"`
		Choice     string  `json:"choice"`
		Answer     string  `json:"answer"`
	}
	if json.Unmarshal(raw, &b) != nil ||
		(kind == "approval" && b.Choice != "once" && b.Choice != "session" && b.Choice != "always" && b.Choice != "deny") ||
		(kind == "clarify" && strings.TrimSpace(b.Answer) == "") {
		refuse(w, http.StatusBadRequest, "bad_request")
		return http.StatusBadRequest
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		refuse(w, http.StatusNotFound, "not_found")
		return http.StatusNotFound
	}
	if r.prompt() != kind || b.RequestID != r.promptID() || b.Generation != 1 {
		refuse(w, http.StatusConflict, "no_such_prompt")
		return http.StatusConflict
	}
	if !r.answered.IsZero() {
		reply(w, http.StatusOK, map[string]string{"status": "already_resolved", "prompt_id": r.promptID(), "run_id": r.id})
		return http.StatusOK
	}
	if _, _, pending := s.snapshot(r); len(pending) == 0 {
		refuse(w, http.StatusConflict, "no_such_prompt")
		return http.StatusConflict
	}
	r.answered, r.choice = s.c.Now(), b.Choice
	reply(w, http.StatusOK, map[string]string{"status": "resolved", "prompt_id": r.promptID(), "run_id": r.id})
	return http.StatusOK
}
