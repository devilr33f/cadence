package t3codetest

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// scripts, picked by the first word of a run's message: finish (any other
// word too) ends completed with an assistant message; ask opens a user input
// question (QuestionID, options Red and Blue) and completes once answered;
// approve opens a command approval and completes once decided; fail adds an
// error item and fails the run; hold keeps running until run.interrupt
// (interrupted) or thread.stop (cancelled).
const (
	scriptFinish  = "finish"
	scriptAsk     = "ask"
	scriptApprove = "approve"
	scriptFail    = "fail"
	scriptHold    = "hold"
)

func scriptOf(text string) string {
	w, _, _ := strings.Cut(strings.TrimSpace(text), " ")
	switch w = strings.ToLower(w); w {
	case scriptAsk, scriptApprove, scriptFail, scriptHold:
		return w
	}
	return scriptFinish
}

var errRefused = errors.New("refused")

// start begins a run of text on t; mu held. Its script's next step runs
// after the step delay.
func (s *state) start(t *thread, text string, step time.Duration) {
	id := t.shell["id"].(string)
	runID := s.newID("run")
	msgID := s.newID("message")
	ts := s.stamp()
	run := obj{
		"id": runID, "threadId": id, "ordinal": len(t.runs) + 1, "providerInstanceId": "claude",
		"modelSelection": t.shell["modelSelection"], "providerThreadId": nil, "userMessageId": msgID,
		"rootNodeId": nil, "activeAttemptId": nil, "status": "running", "requestedAt": ts, "startedAt": ts,
		"completedAt": nil, "checkpointId": nil, "contextHandoffId": nil, "futureRunField": "x",
	}
	t.runs = append(t.runs, run)
	t.activeID, t.script = runID, scriptOf(text)
	s.event(id, "run.created", run)
	user := s.message(t, runID, "user", text)
	s.event(id, "message.updated", user)
	// what a newer server may send: an unknown event and an unknown item type
	s.event(id, "future.event-type", obj{"anything": []any{1, "two"}})
	s.event(id, "turn-item.updated", s.item(t, runID, "future_widget", obj{"widget": obj{"options": "not a list"}}))
	t.shell["status"], t.shell["latestRunId"], t.shell["activeRunId"] = "running", runID, runID
	s.shellUpdate(t)
	s.notify()
	script := t.script
	time.AfterFunc(step, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if t.activeID != runID {
			return
		}
		switch script {
		case scriptAsk:
			s.openRequest(t, runID, "user_input")
		case scriptApprove:
			s.openRequest(t, runID, "command")
		case scriptFail:
			s.event(id, "turn-item.updated", s.item(t, runID, "error", obj{"failure": obj{
				"class": "provider_error", "message": "the model is unavailable", "code": "overloaded", "retryable": false,
			}}))
			t.shell["lastErrorCode"], t.shell["lastErrorClass"] = "overloaded", "provider_error"
			s.endRun(t, "failed")
		case scriptHold:
		default:
			s.event(id, "message.updated", s.message(t, runID, "assistant", "Finished: "+text))
			s.endRun(t, "completed")
		}
		s.notify()
	})
}

func (s *state) message(t *thread, runID, role, text string) obj {
	ts := s.stamp()
	m := obj{
		"id": s.newID("message"), "threadId": t.shell["id"], "runId": runID, "nodeId": nil, "role": role,
		"text": text, "attachments": []any{}, "streaming": false, "createdAt": ts, "updatedAt": ts,
		"createdBy": "agent", "creationSource": "server",
	}
	if role == "user" {
		m["createdBy"] = "user"
	}
	t.messages = append(t.messages, m)
	return m
}

func (s *state) item(t *thread, runID, typ string, fields obj) obj {
	ts := s.stamp()
	it := obj{
		"id": s.newID("item"), "threadId": t.shell["id"], "runId": runID, "nodeId": nil, "providerThreadId": nil,
		"providerTurnId": nil, "nativeItemRef": nil, "parentItemId": nil, "ordinal": len(t.items),
		"status": "in_progress", "title": nil, "startedAt": ts, "completedAt": nil, "type": typ,
	}
	for k, v := range fields {
		it[k] = v
	}
	t.items = upsert(t.items, it)
	return it
}

func (s *state) openRequest(t *thread, runID, kind string) {
	id := t.shell["id"].(string)
	reqID := s.newID("request")
	ts := s.stamp()
	req := obj{
		"id": reqID, "nodeId": "node-1", "providerTurnId": nil, "nativeRequestRef": nil, "kind": kind,
		"status": "pending", "responseCapability": obj{"type": "live", "providerSessionId": "session-1"},
		"createdAt": ts, "resolvedAt": nil,
	}
	t.requests = upsert(t.requests, req)
	s.event(id, "runtime-request.updated", req)
	if kind == "user_input" {
		s.event(id, "turn-item.updated", s.item(t, runID, "user_input_request", obj{"requestId": reqID, "questions": []any{obj{
			"id": QuestionID, "header": "Color", "question": "Which color should the button be?",
			"options":           []any{obj{"label": "Red", "description": "warm"}, obj{"label": "Blue", "description": "cool"}},
			"allowCustomAnswer": true,
		}}}))
	} else {
		s.event(id, "turn-item.updated", s.item(t, runID, "approval_request", obj{
			"requestId": reqID, "requestKind": kind, "prompt": "Run make test?", "reason": "checks the change",
			"options": []any{
				obj{"decision": "accept", "label": "Allow"}, obj{"decision": "acceptForSession", "label": "Allow for this session"},
				obj{"decision": "decline", "label": "Deny"},
			},
		}))
	}
	s.setRun(t, runID, "waiting")
	t.shell["status"] = "waiting"
	t.shell["pendingRuntimeRequest"] = obj{"id": reqID, "kind": kind, "createdAt": ts, "blocking": true}
	s.shellUpdate(t)
}

// respond resolves a pending request and completes its run; mu held.
func (s *state) respond(t *thread, reqID, decision string, answers map[string]any) error {
	var req obj
	for _, r := range t.requests {
		if r["id"] == reqID {
			req = r
		}
	}
	if req == nil || req["status"] != "pending" {
		return errRefused
	}
	if req["kind"] == "user_input" {
		if _, ok := answers[QuestionID]; !ok {
			return errRefused
		}
		req["answers"] = answers
	} else {
		switch decision {
		case "accept", "acceptForSession", "decline", "cancel":
		default:
			return errRefused
		}
		req["decision"] = decision
	}
	req["status"], req["resolvedAt"] = "resolved", s.stamp()
	id := t.shell["id"].(string)
	s.event(id, "runtime-request.updated", req)
	t.shell["pendingRuntimeRequest"] = nil
	text := "Answered"
	if decision == "decline" || decision == "cancel" {
		text = "Declined"
	}
	s.event(id, "message.updated", s.message(t, t.activeID, "assistant", text))
	s.endRun(t, "completed")
	return nil
}

func (s *state) setRun(t *thread, runID, status string) {
	for _, r := range t.runs {
		if r["id"] == runID {
			r["status"] = status
			if status != "waiting" && status != "running" {
				r["completedAt"] = s.stamp()
			}
			s.event(t.shell["id"].(string), "run.updated", r)
		}
	}
}

// endRun ends the active run with status; mu held.
func (s *state) endRun(t *thread, status string) {
	if t.activeID == "" {
		return
	}
	s.setRun(t, t.activeID, status)
	t.activeID = ""
	t.shell["status"], t.shell["activeRunId"], t.shell["pendingRuntimeRequest"] = status, nil, nil
	s.shellUpdate(t)
}

// Command is one dispatchCommand or launchThread the fake took: its type
// (launchThread for a launch), thread and command id, and the payload as sent.
type Command struct {
	Type, ThreadID, CommandID string
	Raw                       json.RawMessage
}
