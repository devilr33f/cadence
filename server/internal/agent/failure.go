package agent

import (
	"context"

	"git.shino.boo/devilreef/cadence/server/internal/conversation"
)

// outcome is the terminal failure as the turn stores and forwards it. The
// result's code wins over an interrupted step's provisional one.
func (r *Runner) outcome(tr *turn, res Result) *conversation.TurnFailure {
	if res.Code == "" {
		return nil
	}
	if tr.failure != nil {
		f := *tr.failure
		f.Code = res.Code
		if clean := conversation.SanitizeTurnFailure(&f); clean != nil {
			return clean
		}
	}
	f := &conversation.TurnFailure{Code: res.Code, Stage: conversation.StageLLM, Step: tr.steps, Provider: tr.v.LLM,
		WaitedMs: r.d.Now().Sub(tr.start).Milliseconds()}
	if f.Step == 0 {
		f.Stage = conversation.StageSetup
	}
	if f.Stage == conversation.StageLLM {
		f.Model = tr.mode.Model
		if f.Model == "" {
			f.Model = tr.v.Model
		}
	}
	if res.Code == CodeTimeout && tr.mode.MaxDuration > 0 {
		f.LimitMs = tr.mode.MaxDuration.Milliseconds()
	}
	return conversation.SanitizeTurnFailure(f)
}

// toolOutcome records a turn that ended while its calls ran. A tool error on
// an otherwise continuing turn is not a turn failure and never lands here.
func toolOutcome(tr *turn, done []called, code string) *conversation.TurnFailure {
	for _, d := range done {
		if d.err == nil {
			continue
		}
		f := &conversation.TurnFailure{Code: code, Stage: conversation.StageTool, Step: tr.steps, Tool: d.name, WaitedMs: d.ms}
		if f.Tool == "" {
			f.Tool = d.tool.Name
		}
		if d.err.Code == CodeTimeout {
			limit := d.tool.Timeout
			if limit <= 0 {
				limit = DefaultTimeout
			}
			f.LimitMs = limit.Milliseconds()
		}
		return f
	}
	return nil
}

// contextToolOutcome only replaces a model failure when the turn really ended
// in the calls, not while the model was streaming.
func contextToolOutcome(ctx context.Context, tr *turn, done []called) {
	if ctx.Err() != nil {
		tr.failure = toolOutcome(tr, done, ctxCode(ctx))
	}
}
