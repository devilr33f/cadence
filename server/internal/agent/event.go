package agent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/subscriptions"
	"git.shino.boo/devilreef/cadence/server/internal/tasks"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

// eventTool is the call an event turn's task data comes as: what a task wrote
// reaches the model only as untrusted tool output, so it taints the turn,
// later turns read it withheld (RedactHistory: task_inspect is untrusted, and
// a tool not registered is withheld too) and the summaries and learning treat
// it as any untrusted result.
const eventTool = "task_inspect"

// seed is a tool call the server made for a turn before it began, and its
// output.
type seed struct {
	call   providers.ToolCall
	output json.RawMessage
}

// plant puts a seed's call and output after the turn's input, for the model
// and the commit alike, and taints the turn.
func (r *Runner) plant(tr *turn, sd *seed) {
	if sd == nil {
		return
	}
	content, _ := resultContent(sd.output, nil, "")
	tr.kept = append(tr.kept,
		providers.Message{Role: roleAssistant, ToolCalls: []providers.ToolCall{sd.call}},
		providers.Message{Role: roleTool, ToolCallID: sd.call.ID, Content: content})
	tr.tainted = true
	r.publish(tr, "tool_call", map[string]any{"step": 0, "id": sd.call.ID, "name": sd.call.Name, "args": sd.call.Arguments})
	r.publish(tr, "tool_result", map[string]any{
		"step": 0, "id": sd.call.ID, "name": sd.call.Name, "ok": true, "code": "", "result": json.RawMessage(content), "ms": 0,
	})
}

// TurnError is a failed event turn: its code, never text.
type TurnError struct{ Code string }

func (e *TurnError) Error() string     { return "agent: event turn failed: " + e.Code }
func (e *TurnError) ErrorCode() string { return e.Code }

// RunEvent runs an event turn (M5b-D6) on e's device conversation: e.Text,
// our own framing, is the input, and e.Data, what the task wrote, follows as
// the output of a task_inspect call. The turn is not woken and reads only
// Read tools and those marked for event turns, acting on e.TaskID alone. It
// returns all it said, every step's speech it kept, "" when the model chose
// to say nothing, or a *TurnError.
func (r *Runner) RunEvent(ctx context.Context, e tasks.EventTurn) (string, error) {
	args, err := json.Marshal(struct {
		ID     string       `json:"id"`
		Detail tasks.Detail `json:"detail"`
	}{e.TaskID, tasks.DetailSummary})
	if err != nil {
		return "", &TurnError{Code: CodeInternal}
	}
	sd := &seed{call: providers.ToolCall{ID: "event_" + rand.Text(), Name: eventTool, Arguments: args}, output: e.Data}
	return r.runEvent(ctx, Turn{DeviceID: e.DeviceID, Trigger: cadencev2.Trigger_TRIGGER_EVENT, Text: e.Text, TaskID: e.TaskID}, sd)
}

// updateTool is the call an agent delivery's updates come as: no tool of that
// name is registered, so later turns read its output withheld, and the turn
// is tainted from its start as every event turn.
const updateTool = "subscription_updates"

// RunUpdate runs an event turn for updates of followed outside work (the
// subscriptions hub's agent delivery): e.Text, our own framing, is the input,
// and e.Data, what the outside systems wrote, follows as the output of a
// subscription_updates call. Like RunEvent it is not woken, reads only Read
// tools and those marked for event turns, and acts on no task; it returns
// all it said, "" for silence, or a *TurnError.
func (r *Runner) RunUpdate(ctx context.Context, e subscriptions.EventTurn) (string, error) {
	sd := &seed{call: providers.ToolCall{ID: "event_" + rand.Text(), Name: updateTool, Arguments: json.RawMessage(`{}`)}, output: e.Data}
	// the hub's delivery loop holds no trace: the update's turn is one of its own
	if !trace.SpanFromContext(ctx).IsRecording() {
		var span trace.Span
		ctx, span = tracing.Root(ctx, traceScope, "subscription.update_turn", attribute.String(tracing.AttrDeviceID, e.DeviceID))
		defer span.End()
	}
	return r.runEvent(ctx, Turn{DeviceID: e.DeviceID, Trigger: cadencev2.Trigger_TRIGGER_EVENT, Text: e.Text}, sd)
}

// runEvent runs an event turn with its seed, which nobody listens to as it
// speaks: what it says is returned whole.
func (r *Runner) runEvent(ctx context.Context, t Turn, sd *seed) (string, error) {
	out := make(chan Out)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range out {
		}
	}()
	res, spoken := r.run(ctx, t, sd, out)
	close(out)
	<-drained
	if res.Code != "" {
		return "", &TurnError{Code: res.Code}
	}
	return strings.TrimSpace(spoken), nil
}

// RunTyped runs t, a turn the owner typed in the dashboard, which nobody
// listens to as it speaks: it returns the turn's result and all it said,
// every step's speech it kept, trimmed.
func (r *Runner) RunTyped(ctx context.Context, t Turn) (Result, string) {
	t.Typed = true
	out := make(chan Out)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range out {
		}
	}()
	res, spoken := r.run(ctx, t, nil, out)
	close(out)
	<-drained
	return res, strings.TrimSpace(spoken)
}
