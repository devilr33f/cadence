package tasks

import (
	"context"

	"github.com/google/uuid"
)

// Spec is what a kind starts with. The instructions go to the kind only: the
// store keeps the title. TaskID is the stored task's id, set by Manager.Start:
// a kind derives its idempotency keys from it.
type Spec struct {
	Title, Instructions string
	Origin              uuid.UUID
	TaskID              uuid.UUID
}

type Detail string

const (
	DetailSummary    Detail = "summary"
	DetailTranscript Detail = "transcript"
)

// Event is what a kind reports about one of its tasks, by its own ref. Kind
// is progress, waiting_input, done, failed, cancelled or unknown.
type Event struct {
	Ref       string
	Kind      string
	Progress  string
	Question  *Question
	Result    string
	ErrorCode string
}

// Snapshot is a kind's view of a task. Status is a Status* value; on a
// restart a kind that cannot resume a task reports StatusUnknown.
type Snapshot struct {
	Status     string
	Progress   string
	Question   *Question
	Result     string
	ErrorCode  string
	Transcript string // only with DetailTranscript
}

// Kind runs one sort of task. Events carries all of its tasks' events and is
// closed when the kind stops. Send carries the token of the question it
// answers (empty when it answers none); a kind refuses a token that no longer
// names the prompt it waits on. A kind's errors may carry a plain code through
// an ErrorCode() string method.
type Kind interface {
	Name() string
	Start(ctx context.Context, s Spec) (ref string, err error)
	Send(ctx context.Context, ref, token, msg string) error
	Cancel(ctx context.Context, ref string) error
	Inspect(ctx context.Context, ref string, d Detail) (Snapshot, error)
	Events() <-chan Event
}

const (
	DecisionOnce = "once"
	DecisionDeny = "deny"
)

// Approver is a kind that answers approval questions, by the token of the
// question shown. Only Manager.Approve calls it, after the caller's own policy
// checks: Send never approves.
type Approver interface {
	Approve(ctx context.Context, ref, token, decision string) error
}
