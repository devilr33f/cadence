package agent

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

// the agent's spans carry ids, names, codes and counts: never a prompt, a
// message, a tool's arguments or its result (rule 8). Token counts are
// cadence.usage.*: the guard drops any key holding "token" or "prompt".
const (
	traceScope = "agent"

	attrStep         = "cadence.step"
	attrMode         = "cadence.mode"
	attrTrigger      = "cadence.trigger"
	attrCode         = "cadence.end_code"
	attrSteps        = "cadence.steps"
	attrToolCalls    = "cadence.tool_calls"
	attrToolsOffered = "cadence.tools_offered"
	attrForced       = "cadence.forced"
	attrTool         = "cadence.tool"
	attrToolClass    = "cadence.tool_class"
	attrOutcome      = "cadence.outcome"
	attrTTFTMs       = "cadence.ttft_ms"
	attrChunks       = "cadence.chunks"
	attrModel        = "gen_ai.request.model"
	attrUsageIn      = "cadence.usage.in"
	attrUsageOut     = "cadence.usage.out"
	attrFinish       = "gen_ai.response.finish_reason"
)

// startTurnSpan opens agent.turn under the caller's span (voice.session, an
// event turn's root, a typed turn's request).
func startTurnSpan(ctx context.Context, tr *turn) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{
		attribute.String(tracing.AttrDeviceID, tr.DeviceID), attribute.String(tracing.AttrTurnID, tr.id),
	}
	if tr.SessionID != "" {
		attrs = append(attrs, attribute.String(tracing.AttrSessionID, tr.SessionID))
	}
	if name := triggerName(tr.Turn); name != "" {
		attrs = append(attrs, attribute.String(attrTrigger, name))
	}
	return tracing.Start(ctx, traceScope, "agent.turn", attrs...)
}

func endTurnSpan(span trace.Span, tr *turn, res Result) {
	// a step a panic cut never logged
	if tr.stepSpan != nil {
		endStepSpan(tr, reply{code: CodeInternal}, 0)
	}
	code := res.Code
	if code == "" {
		code = "ok"
	}
	span.SetAttributes(attribute.String(attrCode, code), attribute.Int(attrSteps, tr.steps),
		attribute.Int(attrToolCalls, tr.calls), attribute.Bool("cadence.silent", res.Silent),
		attribute.Int(attrUsageIn, tr.usage.PromptTokens), attribute.Int(attrUsageOut, tr.usage.CompletionTokens))
	if tr.mode.Name != "" {
		span.SetAttributes(attribute.String(attrMode, tr.mode.Name))
	}
	if res.Code != "" {
		tracing.Fail(span, res.Code)
	}
	span.End()
}

// startStep opens agent.step, the model's call and the tools it asked for;
// logStep ends it, as every step path logs once.
func (r *Runner) startStep(ctx context.Context, tr *turn, offered int, forced bool) context.Context {
	ctx, span := tracing.Start(ctx, traceScope, "agent.step",
		attribute.Int(attrStep, tr.steps+1), attribute.String(attrMode, tr.mode.Name),
		attribute.Int(attrToolsOffered, offered), attribute.Bool(attrForced, forced))
	tr.stepSpan = span
	return ctx
}

func endStepSpan(tr *turn, rep reply, calls int) {
	span := tr.stepSpan
	if span == nil {
		return
	}
	tr.stepSpan = nil
	span.SetAttributes(attribute.Int(attrToolCalls, calls),
		attribute.Int(attrUsageIn, rep.usage.PromptTokens), attribute.Int(attrUsageOut, rep.usage.CompletionTokens))
	if rep.code != "" {
		span.SetAttributes(attribute.String(attrCode, rep.code))
		tracing.Fail(span, rep.code)
	}
	span.End()
}

// llmSpan is one model call: llm.request, its time to the first chunk, its
// tokens and how it finished.
type llmSpan struct {
	span   trace.Span
	chunks int
}

func startLLMSpan(ctx context.Context, tr *turn, model string, tools int) (context.Context, *llmSpan) {
	ctx, span := tracing.StartKind(ctx, traceScope, "llm.request", trace.SpanKindClient,
		attribute.String(tracing.AttrProvider, tr.v.LLM), attribute.String(attrModel, model),
		attribute.Int(attrStep, tr.steps), attribute.Int(attrToolsOffered, tools))
	return ctx, &llmSpan{span: span}
}

// chunk marks the first chunk, the model's time to first token
func (l *llmSpan) chunk(since int64) {
	if l.chunks == 0 {
		l.span.SetAttributes(attribute.Int64(attrTTFTMs, since))
		l.span.AddEvent("first_chunk")
	}
	l.chunks++
}

func (l *llmSpan) end(rep reply) {
	finish := "stop"
	switch {
	case rep.code != "":
		finish = rep.code
	case len(rep.calls) > 0:
		finish = "tool_calls"
	}
	l.span.SetAttributes(attribute.String(attrFinish, finish), attribute.Int(attrChunks, l.chunks),
		attribute.Int(attrToolCalls, len(rep.calls)))
	if rep.metered {
		l.span.SetAttributes(attribute.Int(attrUsageIn, rep.usage.PromptTokens),
			attribute.Int(attrUsageOut, rep.usage.CompletionTokens))
	}
	if rep.code != "" {
		tracing.Fail(l.span, rep.code)
	}
	l.span.End()
}

// startToolSpan opens tool.call for a registered tool, named by the
// registry, its class read or write.
func startToolSpan(ctx context.Context, t Tool) (context.Context, trace.Span) {
	class := "write"
	if t.Effects == Read {
		class = "read"
	}
	return tracing.Start(ctx, traceScope, "tool.call",
		attribute.String(attrTool, t.Name), attribute.String(attrToolClass, class))
}

func endToolSpan(span trace.Span, err *ToolError) {
	// a failure's code goes on as error.type, through the guard's code check
	outcome := "ok"
	if err != nil {
		outcome = "failed"
		tracing.Fail(span, err.Code)
	}
	span.SetAttributes(attribute.String(attrOutcome, outcome))
	span.End()
}

// prepareSpan is prepare under agent.prepare: the settings, the mode, the
// history, recall, home and task reads before the first step.
func (r *Runner) prepareSpan(ctx context.Context, tr *turn) string {
	ctx, span := tracing.Start(ctx, traceScope, "agent.prepare")
	code := r.prepare(ctx, tr)
	if code != "" {
		tracing.Fail(span, code)
	}
	span.SetAttributes(attribute.Bool("cadence.history_failed", tr.historyFailed))
	span.End()
	return code
}
