package agent

import (
	"context"
	"encoding/json"
	"slices"
	"sync"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	// removeAfter is how often a tool may fail with the same code in a turn
	// before it is no longer offered (spec §3.6).
	removeAfter = 2
	// maxCalls bounds the calls of one step that run.
	maxCalls = 8
)

type called struct {
	tool    Tool
	offered bool   // offered by the step, run or refused by its schema
	name    string // the registered tool's name, safe to log; "" for a name the model invented
	out     json.RawMessage
	err     *ToolError
	ms      int64
	// target is the device the call resolved to act on, "" for none
	target string
}

// runCalls runs a step's calls and appends the assistant message and one
// tool message per call, in call order, to the context and the commit.
func (r *Runner) runCalls(ctx context.Context, tr *turn, offered []Tool, rep reply) []called {
	asked := providers.Message{Role: roleAssistant, Content: rep.text, ToolCalls: rep.calls}
	tr.msgs = append(tr.msgs, asked)
	tr.kept = append(tr.kept, asked)
	for _, c := range rep.calls {
		r.publish(tr, "tool_call", map[string]any{"step": tr.steps, "id": c.ID, "name": c.Name, "args": c.Arguments})
	}
	done := r.execute(ctx, tr, offered, rep.calls)
	// a failure counts once per step: calls the model sent together are one try
	counted := map[failure]bool{}
	for i, c := range rep.calls {
		d := &done[i]
		content, e := resultContent(d.out, d.err, d.target)
		d.err = e
		code := ""
		if e != nil {
			code = e.Code
		}
		r.publish(tr, "tool_result", map[string]any{
			"step": tr.steps, "id": c.ID, "name": c.Name, "ok": e == nil, "code": code,
			"result": json.RawMessage(content), "ms": d.ms, "target": d.target,
		})
		msg := providers.Message{Role: roleTool, ToolCallID: c.ID, Content: content}
		tr.msgs = append(tr.msgs, msg)
		tr.kept = append(tr.kept, msg)
		if d.name != "" && !slices.Contains(tr.tools, d.name) {
			tr.tools = append(tr.tools, d.name)
		}
		if d.name == ConversationReset && d.offered && e == nil {
			tr.reset = true
		}
		switch {
		case e != nil && d.offered:
			f := failure{d.tool.Name, e.Code}
			if counted[f] {
				break
			}
			counted[f] = true
			if tr.failed[f]++; tr.failed[f] >= removeAfter {
				tr.removed[d.tool.Name] = true
			}
		case e == nil && d.tool.Taints():
			tr.tainted = true
		}
	}
	tr.calls += len(rep.calls)
	return done
}

// execute checks each call against the step's tool list and its schema, then
// runs Read calls concurrently and Write calls one at a time in the model's
// order beside them: writes change what later calls see.
func (r *Runner) execute(ctx context.Context, tr *turn, offered []Tool, calls []providers.ToolCall) []called {
	done := make([]called, len(calls))
	var writes []int
	var wg sync.WaitGroup
	for i, c := range calls {
		d := &done[i]
		if reg, ok := find(tr.toolset, c.Name); ok {
			d.name = reg.Name
		}
		d.tool, d.offered = find(offered, c.Name)
		switch {
		case i >= maxCalls:
			// refused unrun, so it counts toward no removal
			d.offered, d.err = false, tooManyCalls()
		case !d.offered && tr.removed[c.Name]:
			d.err = removedTool(offered)
		case !d.offered:
			d.err = notOffered(offered)
		default:
			d.err = Validate(d.tool, c.Arguments)
		}
		switch {
		case d.err != nil:
			continue
		case d.tool.Effects == Read:
			wg.Go(func() { r.call(ctx, tr, d, c) })
		default:
			writes = append(writes, i)
		}
	}
	wg.Go(func() {
		for _, i := range writes {
			r.call(ctx, tr, &done[i], calls[i])
		}
	})
	wg.Wait()
	return done
}

// call runs one tool unless the step already ended.
func (r *Runner) call(ctx context.Context, tr *turn, d *called, c providers.ToolCall) {
	if ctx.Err() != nil {
		d.err = ended(ctx)
		return
	}
	start := r.d.Now()
	ctx, slot := withTargetSlot(ctx)
	defer func() { d.target = slot.get() }()
	d.out, d.err = Run(ctx, d.tool, Call{
		DeviceID: tr.DeviceID, SessionID: tr.SessionID, TurnID: tr.id, CallID: c.ID, Trigger: tr.Trigger, Text: tr.Text,
		TaskID: tr.TaskID, Args: c.Arguments,
	})
	d.ms = r.d.Now().Sub(start).Milliseconds()
}

// allSilent reports a step whose calls were all SilentOnSuccess and all
// succeeded (spec §3.8).
func allSilent(done []called) bool {
	for _, d := range done {
		var out struct {
			Pending bool `json:"pending"`
		}
		_ = json.Unmarshal(d.out, &out)
		if out.Pending || !d.offered || d.err != nil || !d.tool.SilentOnSuccess {
			return false
		}
	}
	return len(done) > 0
}

func find(tools []Tool, name string) (Tool, bool) {
	for _, t := range tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// notOffered refuses a call of a tool the step did not offer: unknown,
// disabled, removed or, once tainted, a Write tool.
func notOffered(offered []Tool) *ToolError {
	return &ToolError{
		Code:       "unknown_tool",
		Message:    "No tool of that name is offered now.",
		Hint:       "Call one of the candidates, or answer without a tool.",
		Candidates: toolNames(offered),
	}
}

// removedTool refuses a call of a tool removed for failing twice the same way.
func removedTool(offered []Tool) *ToolError {
	e := notOffered(offered)
	e.Message = "This tool was removed after failing twice: answer the user without it."
	return e
}

func tooManyCalls() *ToolError {
	return &ToolError{
		Code:      "too_many_calls",
		Message:   "Only the first 8 calls of a step run.",
		Hint:      "Call this again in the next step if it is still needed.",
		Retryable: true,
	}
}
