package tools

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/tasks"
)

const replyTimeout = 30 * time.Second

// "message, or decision with userWords" is Run's check: a oneOf would refuse
// in words the model cannot act on
const replyInput = `{
	"type": "object",
	"properties": {
		"id": ` + idInput + `,
		"token": {"type": "string", "maxLength": 64},
		"message": {"type": "string", "minLength": 1, "maxLength": 2000},
		"decision": {"type": "string", "enum": ["once", "deny"]},
		"userWords": {"type": "string", "minLength": 1, "maxLength": 500}
	},
	"required": ["id"],
	"additionalProperties": false
}`

type replyArgs struct {
	ID        string  `json:"id"`
	Token     string  `json:"token"`
	Message   *string `json:"message"`
	Decision  string  `json:"decision"`
	UserWords *string `json:"userWords"`
}

// taskReply answers a task's question by the token of the prompt the model
// read (Ruling M5b-R2). It is offered in event turns, where it answers only
// the event's own task's clarify question, and stays in a tainted turn
// (Ruling M5b-R4): an approval takes a woken turn and the user's own words
// (M5b-D8).
func taskReply(d TaskDeps) agent.Tool {
	return agent.Tool{
		Name: "task_reply",
		Description: "Answer a background task's question, with the token shown beside it. A clarify question " +
			"takes message: the answer, from what the user said or what you know; never make one up. An approval " +
			"question takes decision (once or deny) and userWords: the user's own words from this turn that gave " +
			"the decision, verbatim; never decide for the user, and a bare yes is not enough. An approval needs a " +
			"session the user began with the wake word or a press, and a question the user heard on this device " +
			"before this turn: if they have not, read it to them and let them answer when they next wake you. If " +
			"the task's kind takes it, message also tells a running task something more.",
		Input:   replyInput,
		Effects: agent.Write, Output: agent.Trusted,
		EventTurns: true, TaintExempt: true,
		// a kind may poll before it posts, 10 s each: an answer that landed
		// is not reported as timed out
		Timeout: replyTimeout,
		Run: func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			var a replyArgs
			if err := json.Unmarshal(c.Args, &a); err != nil {
				return nil, badArgs("The arguments are not a JSON object.")
			}
			switch {
			case (a.Message == nil) == (a.Decision == ""):
				return nil, badArgs("Give message for a clarify question, or decision and userWords for an approval.")
			case a.Decision == "" && a.UserWords != nil:
				return nil, badArgs("userWords goes with decision only.")
			case a.Decision != "" && (a.UserWords == nil || strings.TrimSpace(*a.UserWords) == ""):
				return nil, badArgs("An approval takes userWords: the user's own words from this turn that gave the decision.")
			}
			id := taskID(a.ID)
			// an event turn answers its own task's clarify question and nothing
			// else: no approval, no other task, none when it names no task
			if agent.EventTurn(c.Trigger) && (a.Decision != "" || id == "" || id != taskID(c.TaskID)) {
				return nil, agent.NotInEventTurn()
			}
			if id == "" {
				return nil, d.notFound(ctx)
			}
			t, err := d.Manager.Get(ctx, id)
			if err != nil {
				return nil, d.refusal(ctx, err, internal())
			}
			if a.Decision != "" {
				return d.approve(ctx, c, t, a)
			}
			return d.answer(ctx, c, t, a)
		},
	}
}

func asks(t tasks.Task, kind string) bool {
	return t.Status == tasks.StatusWaitingInput && t.Question != nil && t.Question.Kind == kind
}

// answer sends a message: an answer to a clarify question, or in a turn the
// user spoke, a word to a running task.
func (d TaskDeps) answer(ctx context.Context, c agent.Call, t tasks.Task, a replyArgs) (json.RawMessage, *agent.ToolError) {
	switch {
	case agent.EventTurn(c.Trigger) && !asks(t, tasks.QuestionClarify):
		return nil, agent.NotInEventTurn()
	case asks(t, tasks.QuestionApproval):
		return nil, approvalNeeded()
	case asks(t, tasks.QuestionClarify) && a.Token == "":
		return nil, tokenNeeded()
	}
	got, err := d.Manager.Send(ctx, t.ShortID, a.Token, *a.Message)
	if err != nil {
		return nil, d.refusal(ctx, err, replyFailed())
	}
	return encode(taskAck{ID: got.ShortID, Status: got.Status})
}

// approve passes the user's decision on, with their words as its evidence,
// once the policy allows it.
func (d TaskDeps) approve(ctx context.Context, c agent.Call, t tasks.Task, a replyArgs) (json.RawMessage, *agent.ToolError) {
	switch {
	case asks(t, tasks.QuestionClarify):
		return nil, badArgs("This is a clarify question: give message, not decision.")
	case !asks(t, tasks.QuestionApproval):
		return nil, noApproval()
	case a.Token == "":
		return nil, tokenNeeded()
	case a.Token != t.Question.Token:
		// an old token names a changed question, not an untold one; the manager checks it too
		return nil, questionChanged()
	}
	words := strings.TrimSpace(*a.UserWords)
	if e := d.approvalRefusal(ctx, c, t, a.Token, a.Decision, words); e != nil {
		return nil, e
	}
	got, err := d.Manager.Approve(ctx, t.ShortID, a.Token, a.Decision, tasks.Evidence{Via: tasks.ViaVoice, Device: c.DeviceID, Words: words})
	if err != nil {
		return nil, d.refusal(ctx, err, replyFailed())
	}
	return encode(taskAck{ID: got.ShortID, Status: got.Status})
}

func tokenNeeded() *agent.ToolError {
	return badArgs("Give the question's token, as the background tasks note or task_list shows it.")
}

func replyFailed() *agent.ToolError {
	return &agent.ToolError{Code: "reply_failed", Message: "The task did not take the answer."}
}
