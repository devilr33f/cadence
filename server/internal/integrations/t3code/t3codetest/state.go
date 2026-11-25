package t3codetest

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// seeded ids: one project and a thread per script, each idle until a message
// starts it (see script)
const (
	ProjectID     = "project-cadence"
	ThreadFinish  = "thread-finish"
	ThreadAsk     = "thread-ask"
	ThreadApprove = "thread-approve"
	ThreadFail    = "thread-fail"
	ThreadHold    = "thread-hold"
	QuestionID    = "q-color"
)

type obj = map[string]any

type logged struct {
	seq    int64
	thread string
	item   obj // a shell item or a thread stream item
}

type thread struct {
	shell    obj
	runs     []obj
	requests []obj
	messages []obj
	items    []obj
	script   string
	activeID string
}

// state is the fake's whole orchestration: a global sequence, the shell and
// thread event logs, and a channel closed on every append.
type state struct {
	mu       sync.Mutex
	seq      int64
	ids      int
	now      func() time.Time
	projects []obj
	threads  map[string]*thread
	order    []string
	shellLog []logged
	events   []logged
	changed  chan struct{}
}

func newState(now func() time.Time) *state {
	s := &state{now: now, threads: map[string]*thread{}, changed: make(chan struct{})}
	ts := s.stamp()
	s.projects = []obj{{
		"id": ProjectID, "title": "Cadence", "workspaceRoot": "/work/cadence", "scripts": []any{},
		"defaultModelSelection": obj{"instanceId": "claude", "model": "claude-opus-5-5"},
		"createdAt":             ts, "updatedAt": ts, "futureProjectField": true,
	}}
	for _, id := range []string{ThreadFinish, ThreadAsk, ThreadApprove, ThreadFail, ThreadHold} {
		s.addThread(id, ProjectID, strings.TrimPrefix(id, "thread-"))
	}
	return s
}

func (s *state) stamp() string { return s.now().UTC().Format(time.RFC3339Nano) }

func (s *state) newID(prefix string) string {
	s.ids++
	return fmt.Sprintf("%s-%d", prefix, s.ids)
}

func (s *state) addThread(id, project, title string) *thread {
	ts := s.stamp()
	t := &thread{shell: obj{
		"id": id, "projectId": project, "title": title, "status": "idle",
		"providerInstanceId": "claude", "modelSelection": obj{"instanceId": "claude", "model": "claude-opus-5-5"},
		"runtimeMode": "approval-required", "interactionMode": "default", "branch": nil, "worktreePath": nil,
		"lineage": obj{"rootThreadId": id}, "forkedFrom": nil, "activeProviderThreadId": nil,
		"latestRunId": nil, "activeRunId": nil, "pendingRuntimeRequest": nil, "latestVisibleMessage": nil,
		"latestUserMessageAt": nil, "hasActionableProposedPlan": false, "itemCount": 0, "visibleItemCount": 0,
		"createdAt": ts, "updatedAt": ts, "archivedAt": nil, "settledOverride": nil, "settledAt": nil, "deletedAt": nil,
		"createdBy": "user", "creationSource": "web", "futureShellField": obj{"x": 1},
	}}
	s.threads[id] = t
	s.order = append(s.order, id)
	return t
}

// notify wakes every stream; call with mu held.
func (s *state) notify() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// shellUpdate logs a thread.updated item for t's shell; mu held.
func (s *state) shellUpdate(t *thread) {
	s.seq++
	t.shell["updatedAt"] = s.stamp()
	s.shellLog = append(s.shellLog, logged{seq: s.seq, item: obj{
		"kind": "thread.updated", "sequence": s.seq, "location": "active", "thread": clone(t.shell),
	}})
}

// event logs a domain event of thread id; mu held.
func (s *state) event(id, typ string, payload obj) {
	s.seq++
	s.events = append(s.events, logged{seq: s.seq, thread: id, item: obj{
		"kind": "event", "sequence": s.seq,
		"event": obj{"id": s.newID("event"), "threadId": id, "type": typ, "payload": clone(payload), "occurredAt": s.stamp()},
	}})
}

func (s *state) shellSnapshot() obj {
	threads := make([]any, 0, len(s.order))
	for _, id := range s.order {
		threads = append(threads, clone(s.threads[id].shell))
	}
	projects := make([]any, 0, len(s.projects))
	for _, p := range s.projects {
		projects = append(projects, clone(p))
	}
	return obj{"schemaVersion": 1, "snapshotSequence": s.seq, "threads": threads, "archivedThreads": []any{}, "projects": projects}
}

func (s *state) projection(t *thread) obj {
	app := obj{}
	for _, k := range []string{"id", "projectId", "title", "modelSelection", "runtimeMode", "interactionMode", "createdAt", "updatedAt"} {
		app[k] = t.shell[k]
	}
	return obj{
		"thread": app, "runs": list(t.runs), "attempts": []any{}, "nodes": []any{}, "subagents": []any{},
		"providerSessions": []any{}, "providerThreads": []any{}, "providerTurns": []any{},
		"runtimeRequests": list(t.requests), "messages": list(t.messages), "plans": []any{},
		"turnItems": list(t.items), "checkpointScopes": []any{}, "checkpoints": []any{},
		"contextHandoffs": []any{}, "contextTransfers": []any{}, "visibleTurnItems": []any{},
		"updatedAt": s.stamp(),
	}
}

func list(xs []obj) []any {
	out := make([]any, 0, len(xs))
	for _, x := range xs {
		out = append(out, clone(x))
	}
	return out
}

// clone deep-copies through JSON, so logged items never change later.
func clone(v obj) obj {
	b, _ := json.Marshal(v)
	var out obj
	_ = json.Unmarshal(b, &out)
	return out
}

// upsert replaces the element of xs with x's id, or appends x.
func upsert(xs []obj, x obj) []obj {
	for i, e := range xs {
		if e["id"] == x["id"] {
			xs[i] = x
			return xs
		}
	}
	return append(xs, x)
}
