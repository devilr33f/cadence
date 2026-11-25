package t3code

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// NewID is a client-made id for a command or message: prefix, a dash and 16
// random bytes in hex, so a retried dispatch can carry the same id.
func NewID(prefix string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + hex.EncodeToString(b[:])
}

// SubscribeShell follows the shell (projects and thread shells, new threads
// too) from a snapshot, then its updates; fn runs on the subscription's own
// goroutine, one item at a time, and the next item waits for it. Items of
// kinds this package does not know arrive with only Kind and Sequence.
func (c *Client) SubscribeShell(fn func(ShellItem)) *Subscription {
	return c.subscribe(MethodSubscribeShell, func(cursor int64) any {
		if cursor > 0 {
			return map[string]any{"afterSequence": cursor}
		}
		return map[string]any{}
	}, func(v json.RawMessage) (int64, bool) {
		var it ShellItem
		if json.Unmarshal(v, &it) != nil || it.Kind == "" {
			return 0, false
		}
		fn(it)
		return it.cursor(), true
	})
}

// SubscribeThread follows one thread: a snapshot (its projection), then its
// events from the snapshot's sequence. Event types this package does not
// read still arrive (their payload raw); fn runs as for SubscribeShell. A
// thread T3 does not have ends the subscription with refused.
func (c *Client) SubscribeThread(threadID string, fn func(ThreadItem)) *Subscription {
	return c.subscribe(MethodSubscribeThread, func(cursor int64) any {
		p := map[string]any{"threadId": threadID}
		if cursor > 0 {
			p["afterSequence"] = cursor
		}
		return p
	}, func(v json.RawMessage) (int64, bool) {
		var it ThreadItem
		if json.Unmarshal(v, &it) != nil || it.Kind == "" {
			return 0, false
		}
		fn(it)
		return it.cursor(), true
	})
}

// GetThreadProjection reads one thread's whole projection.
func (c *Client) GetThreadProjection(ctx context.Context, threadID string) (*ThreadProjection, error) {
	var p ThreadProjection
	if err := c.Call(ctx, MethodGetThreadProjection, map[string]string{"threadId": threadID}, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Command is one orchestration command dispatchCommand takes.
type Command interface{ command() (typ, threadID string) }

// commandWire adds the type and the commandId to a command's fields.
func commandWire(cmd Command, commandID string) (map[string]any, error) {
	typ, threadID := cmd.command()
	if threadID == "" {
		return nil, errors.New("t3code: command needs a thread")
	}
	b, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	m["type"] = typ
	m["commandId"] = commandID
	return m, nil
}

// approval decisions (ProviderApprovalDecision) a voice answer may send
const (
	DecisionAccept           = "accept"
	DecisionAcceptForSession = "acceptForSession"
	DecisionDecline          = "decline"
	DecisionCancel           = "cancel"
)

// RespondRuntimeRequest is runtime-request.respond: Decision for an
// approval, Answers (keyed by question id, a string or a list of strings)
// for user input.
type RespondRuntimeRequest struct {
	ThreadID  string         `json:"threadId"`
	RequestID string         `json:"requestId"`
	Decision  string         `json:"decision,omitempty"`
	Feedback  string         `json:"feedback,omitempty"`
	Answers   map[string]any `json:"answers,omitempty"`
}

func (r RespondRuntimeRequest) command() (string, string) {
	return "runtime-request.respond", r.ThreadID
}

// dispatch modes of message.dispatch
const (
	DispatchStartImmediately = "start_immediately"
	DispatchQueueAfterActive = "queue_after_active"
	DispatchSteerActive      = "steer_active"
	DispatchRestartActive    = "restart_active"
)

// DispatchMode is message.dispatch's dispatchMode; TargetRunID only for
// steer_active and restart_active.
type DispatchMode struct {
	Type        string `json:"type"`
	TargetRunID string `json:"targetRunId,omitempty"`
}

// DispatchMessage is message.dispatch: a user message from Cadence (created
// by the user, from the mcp surface, the closest of T3's creation sources to
// a voice client). MessageID empty gets a new one.
type DispatchMessage struct {
	ThreadID     string       `json:"threadId"`
	MessageID    string       `json:"messageId"`
	Text         string       `json:"text"`
	DispatchMode DispatchMode `json:"dispatchMode"`
}

func (m DispatchMessage) command() (string, string) { return "message.dispatch", m.ThreadID }

// MarshalJSON adds the fields T3 requires that Cadence always sets the same.
func (m DispatchMessage) MarshalJSON() ([]byte, error) {
	type plain DispatchMessage
	if m.MessageID == "" {
		m.MessageID = NewID("cadence-msg")
	}
	return json.Marshal(struct {
		plain
		CreatedBy      string `json:"createdBy"`
		CreationSource string `json:"creationSource"`
		Attachments    []any  `json:"attachments"`
	}{plain(m), "user", "mcp", []any{}})
}

// InterruptRun is run.interrupt.
type InterruptRun struct {
	ThreadID string `json:"threadId"`
	RunID    string `json:"runId"`
	Reason   string `json:"reason,omitempty"`
}

func (r InterruptRun) command() (string, string) { return "run.interrupt", r.ThreadID }

// StopThread is thread.stop.
type StopThread struct {
	ThreadID string `json:"threadId"`
	Reason   string `json:"reason,omitempty"`
}

func (s StopThread) command() (string, string) { return "thread.stop", s.ThreadID }

// DispatchCommand sends cmd with commandID (empty gets a new one) and
// returns the sequence T3 committed it at.
func (c *Client) DispatchCommand(ctx context.Context, cmd Command, commandID string) (int64, error) {
	if commandID == "" {
		commandID = NewID("cadence-cmd")
	}
	m, err := commandWire(cmd, commandID)
	if err != nil {
		return 0, fail(CodeBadResponse)
	}
	var r struct {
		Sequence int64 `json:"sequence"`
	}
	if err := c.Call(ctx, MethodDispatchCommand, m, &r); err != nil {
		return 0, err
	}
	return r.Sequence, nil
}

// workspace strategies of a launch
const (
	WorkspaceRoot     = "root"
	WorkspaceWorktree = "worktree"
)

// WorkspaceStrategy is a launch's workspaceStrategy: root, or a new worktree
// from BaseRef.
type WorkspaceStrategy struct {
	Type    string `json:"type"`
	BaseRef string `json:"baseRef,omitempty"`
	Branch  string `json:"branch,omitempty"`
}

// LaunchInput is launchThread's input. CommandID empty gets a new one;
// RuntimeMode and InteractionMode empty are approval-required and default
// (the least an agent may do on the owner's machine).
type LaunchInput struct {
	CommandID         string            `json:"commandId"`
	ProjectID         string            `json:"projectId"`
	Title             string            `json:"title"`
	GenerateTitle     bool              `json:"generateTitle,omitempty"`
	ModelSelection    ModelSelection    `json:"modelSelection"`
	RuntimeMode       string            `json:"runtimeMode"`
	InteractionMode   string            `json:"interactionMode"`
	WorkspaceStrategy WorkspaceStrategy `json:"workspaceStrategy"`
	// Text is the first message; empty starts no run
	Text string `json:"-"`
}

// LaunchResult is launchThread's answer.
type LaunchResult struct {
	ThreadID   string           `json:"threadId"`
	Projection ThreadProjection `json:"projection"`
	Resumed    bool             `json:"resumed"`
	Sequence   int64            `json:"sequence"`
}

// LaunchThread creates a thread in a project, with a first message when
// in.Text is set.
func (c *Client) LaunchThread(ctx context.Context, in LaunchInput) (*LaunchResult, error) {
	if in.CommandID == "" {
		in.CommandID = NewID("cadence-cmd")
	}
	if in.RuntimeMode == "" {
		in.RuntimeMode = "approval-required"
	}
	if in.InteractionMode == "" {
		in.InteractionMode = "default"
	}
	if in.WorkspaceStrategy.Type == "" {
		in.WorkspaceStrategy.Type = WorkspaceRoot
	}
	b, err := json.Marshal(in)
	if err != nil {
		return nil, fail(CodeBadResponse)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fail(CodeBadResponse)
	}
	m["creationSource"] = "mcp"
	if in.Text != "" {
		m["initialMessage"] = map[string]any{"messageId": NewID("cadence-msg"), "text": in.Text, "attachments": []any{}}
	}
	var r LaunchResult
	if err := c.Call(ctx, MethodLaunchThread, m, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
