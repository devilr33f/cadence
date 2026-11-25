package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/tasks"
)

func startInput(kinds []string) string {
	enum, _ := json.Marshal(kinds)
	return `{
	"type": "object",
	"properties": {
		"kind": {"type": "string", "enum": ` + string(enum) + `},
		"instructions": {"type": "string", "minLength": 1, "maxLength": 2000}
	},
	"required": ["kind", "instructions"],
	"additionalProperties": false
}`
}

// taskStart hands work too long for a turn to a kind; the ack is the
// server's own data. An event turn starts nothing: nobody asked.
func taskStart(d TaskDeps) agent.Tool {
	kinds := d.Manager.Kinds()
	return agent.Tool{
		Name: "task_start",
		Description: "Start a background task for work too long for this turn (research, coding, anything that " +
			"takes minutes). kind is who does it (" + strings.Join(kinds, ", ") + "); instructions say what to do, " +
			"with every detail the user gave. Then tell the user in one short sentence that it is under way: the " +
			"result arrives later.",
		Input:   startInput(kinds),
		Effects: agent.Write, Output: agent.Trusted,
		Timeout: 10 * time.Second,
		Run: func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			if agent.EventTurn(c.Trigger) {
				return nil, agent.NotInEventTurn()
			}
			var a struct {
				Kind         string `json:"kind"`
				Instructions string `json:"instructions"`
			}
			if err := json.Unmarshal(c.Args, &a); err != nil {
				return nil, badArgs("The arguments are not a JSON object.")
			}
			if strings.TrimSpace(a.Instructions) == "" {
				return nil, badArgs("Give the instructions: what the task should do.")
			}
			origin, _ := uuid.Parse(c.DeviceID)
			t, err := d.Manager.Start(ctx, a.Kind, tasks.Spec{Instructions: a.Instructions, Origin: origin})
			if err != nil {
				if errors.Is(err, tasks.ErrStartFailed) && t.ShortID != "" {
					// this refusal tells the user now, so the failed task is no news later
					_ = d.Manager.MarkReported(context.WithoutCancel(ctx), t.ShortID)
				}
				e := d.refusal(ctx, err, &agent.ToolError{Code: "task_start_failed", Message: "The task could not start."})
				// every try leaves a task behind
				e.Retryable = false
				return nil, e
			}
			return encode(taskAck{ID: t.ShortID, Status: t.Status})
		},
	}
}

func taskCancel(d TaskDeps) agent.Tool {
	return agent.Tool{
		Name:        "task_cancel",
		Description: "Stop a background task the user wants stopped.",
		Input:       `{"type":"object","properties":{"id":` + idInput + `},"required":["id"],"additionalProperties":false}`,
		Effects:     agent.Write, Output: agent.Trusted,
		// the user's way to stop a task stays after its text was read (Ruling M5b-R4)
		TaintExempt: true,
		Run: func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			if agent.EventTurn(c.Trigger) {
				return nil, agent.NotInEventTurn()
			}
			var a struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(c.Args, &a); err != nil {
				return nil, badArgs("The arguments are not a JSON object.")
			}
			id := taskID(a.ID)
			if id == "" {
				return nil, d.notFound(ctx)
			}
			t, err := d.Manager.Cancel(ctx, id)
			if err != nil {
				return nil, d.refusal(ctx, err, &agent.ToolError{
					Code: "cancel_failed", Message: "The task could not be stopped.", Retryable: true,
				})
			}
			return encode(taskAck{ID: t.ShortID, Status: t.Status})
		},
	}
}
