package tools

import (
	"context"
	"errors"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/hermes"
	"git.shino.boo/devilreef/cadence/server/internal/tasks"
)

// TaskDeps are the task tools' collaborators. The tools log nothing: what
// they carry (instructions, questions, answers) reaches only the model, the
// conversation store, the live feed and the task store.
type TaskDeps struct {
	Manager  *tasks.Manager
	Settings SettingsFunc
}

// Tasks are the task tools (M5b-D4), for a manager with at least one kind.
func Tasks(d TaskDeps) []agent.Tool {
	return []agent.Tool{taskStart(d), taskList(d), taskInspect(d), taskReply(d), taskCancel(d)}
}

var openStatuses = []string{tasks.StatusQueued, tasks.StatusRunning, tasks.StatusWaitingInput}

func open(status string) bool {
	return status == tasks.StatusQueued || status == tasks.StatusRunning || status == tasks.StatusWaitingInput
}

// idInput takes a short id as a voice gives it: "t-1042", "T-1042", "1042".
const idInput = `{"type":"string","minLength":1,"maxLength":24}`

// taskID is the short id raw names, "" when it names none.
func taskID(raw string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(raw)), "t"), "-")
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return ""
	}
	return "t-" + s
}

type taskAck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// notFound offers the active tasks' ids. It is not retryable: guessing among
// them could act on a task the user did not mean.
func (d TaskDeps) notFound(ctx context.Context) *agent.ToolError {
	e := &agent.ToolError{
		Code: "task_not_found", Message: "No task has that id.",
		Hint: "Use an id from the background tasks note or the candidates, or ask the user which task they mean.",
	}
	if active, err := d.Manager.List(ctx, tasks.Filter{Statuses: openStatuses, Limit: 10}); err == nil {
		for _, t := range active {
			e.Candidates = append(e.Candidates, t.ShortID)
		}
	}
	return e
}

// refusal is the model's view of a manager error: never a kind's text, only
// what its code means; fallback for anything else.
func (d TaskDeps) refusal(ctx context.Context, err error, fallback *agent.ToolError) *agent.ToolError {
	var ke *tasks.KindError
	switch {
	case errors.Is(err, tasks.ErrNotFound):
		return d.notFound(ctx)
	case errors.Is(err, tasks.ErrStale):
		return questionChanged()
	case errors.Is(err, tasks.ErrNotActive):
		return &agent.ToolError{Code: "task_not_active", Message: "The task has already finished."}
	case errors.Is(err, tasks.ErrNoApproval):
		return noApproval()
	case errors.Is(err, tasks.ErrUnknownKind):
		return unavailable("The task's kind is not set up on the server.")
	case errors.As(err, &ke):
		if e := kindRefusal(ke.Code); e != nil {
			return e
		}
	}
	return fallback
}

// kindRefusal reads the codes kinds share with the model (Hermes's today);
// nil for any other.
func kindRefusal(code string) *agent.ToolError {
	switch code {
	case hermes.CodeStale:
		return questionChanged()
	case hermes.CodeBusy:
		return &agent.ToolError{Code: "task_busy", Message: "The task is busy right now.", Hint: "Try again in a moment.", Retryable: true}
	case hermes.CodePromptUnshowable, hermes.CodeSecretPrompt:
		return &agent.ToolError{
			Code: "answer_in_hermes", Message: "This question cannot be answered here.",
			Hint: "Tell the user to answer this one in Hermes itself.",
		}
	case hermes.CodeCapacity:
		return &agent.ToolError{
			Code: "too_many_tasks", Message: "Too many tasks are running.",
			Hint: "Tell the user to wait for one to finish, or to cancel one.",
		}
	case hermes.CodeNoQuestion:
		return &agent.ToolError{Code: "no_question", Message: "The task is not asking anything now."}
	case hermes.CodeApprovalPending:
		return approvalNeeded()
	case hermes.CodeNoApproval:
		return noApproval()
	}
	return nil
}

// questionChanged is not retryable: an answer, above all an approval, was
// meant for the question the user heard, never for its replacement.
func questionChanged() *agent.ToolError {
	return &agent.ToolError{
		Code: "question_changed", Message: "The task's question changed since it was read.",
		Hint: "Read the question again and ask the user anew; never reuse an answer meant for the old one.",
	}
}

func noApproval() *agent.ToolError {
	return &agent.ToolError{Code: "no_approval", Message: "The task is not waiting for an approval."}
}

func approvalNeeded() *agent.ToolError {
	return badArgs("This is an approval question: give decision (once or deny) and userWords, the user's own words.")
}
