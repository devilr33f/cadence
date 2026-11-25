package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// codeExpired marks a step its time bound cut; never a turn's code.
const codeExpired = "expired"

type reply struct {
	text      string
	calls     []providers.ToolCall
	usage     providers.Usage
	metered   bool // the model reported its usage
	code      string
	err       error // read for its explicit code/status interfaces alone
	sent      int   // bytes of text forwarded as speech, always a prefix
	discarded bool
}

// request builds a step's llm request. Tool choice goes out only with tools,
// since providers reject it alone. A step that may not call tools keeps the
// last tool list with choice none while the context holds tool messages: the
// cached prefix holds and those calls stay defined.
func (r *Runner) request(tr *turn, tools []Tool, forced bool) providers.LLMRequest {
	model := tr.mode.Model
	if model == "" {
		model = tr.v.Model
	}
	req := providers.LLMRequest{
		Model: model, Messages: slices.Clip(tr.msgs), Reasoning: tr.mode.Reasoning, MaxTokens: maxTokens,
	}
	switch {
	case tr.mode.MaxTokens > 0:
		req.MaxTokens = tr.mode.MaxTokens
	case tr.mode.Reasoning != "" && tr.mode.Reasoning != "none":
		req.MaxTokens = reasoningMaxTokens
	}
	listed := tools
	if len(listed) == 0 && hasToolMessages(tr.msgs) {
		listed = tr.lastOffered
	}
	if len(listed) > 0 {
		req.Tools, req.ToolChoice = toolSpecs(listed), "none"
		if len(tools) > 0 && !forced {
			req.ToolChoice = "auto"
		}
	}
	return req
}

// stream runs one llm step under sctx, a child of the turn's ctx, and
// forwards its text as speech as it arrives (spec §3.7, M3-R7): all of it
// in speak mode or when the step cannot call tools; in drop mode only until
// the model turns to tools, and then what was forwarded is discarded. The
// full text is kept for the context, the commit and the live feed.
func (r *Runner) stream(ctx, sctx context.Context, tr *turn, tools []Tool, forced bool, out chan<- Out) (rep reply) {
	started := r.d.Now()
	req := r.request(tr, tools, forced)
	tr.steps++
	tr.stepMark = len(tr.spoken)
	wait := r.lim.firstChunk
	if tr.mode.FirstTokenTimeout > 0 {
		wait = tr.mode.FirstTokenTimeout
	}
	// what a failure reports: the wait, or less where sctx (the forced call's
	// reserve, the mode's budget) ends first and cuts the step there
	limit := wait
	if dl, ok := sctx.Deadline(); ok {
		limit = min(limit, time.Until(dl))
	}
	defer func() {
		if rep.code == "" {
			return
		}
		code, status := providers.ErrorFacts(rep.err, rep.code)
		if rep.code == codeExpired {
			code = CodeTimeout
		}
		f := &conversation.TurnFailure{Code: code, Stage: conversation.StageLLM, Step: tr.steps, Provider: tr.v.LLM,
			Model: req.Model, HTTPStatus: status, WaitedMs: r.d.Now().Sub(started).Milliseconds()}
		if code == CodeTimeout || rep.code == codeExpired {
			f.LimitMs = limit.Milliseconds()
		}
		tr.failure = f
	}()
	listed := make([]string, len(req.Tools))
	for i, t := range req.Tools {
		listed[i] = t.Name
	}
	r.publish(tr, "llm_request", map[string]any{
		"step": tr.steps, "mode": tr.mode.Name, "model": req.Model, "tools": listed, "toolChoice": req.ToolChoice,
	})
	sctx, cancel := context.WithCancel(sctx)
	defer cancel()
	sctx, ls := startLLMSpan(sctx, tr, req.Model, len(req.Tools))
	defer func() { ls.end(rep) }()
	chunks, err := tr.llm.Stream(sctx, req)
	if err != nil {
		rep.err = err
		code, _ := providers.ErrorFacts(err, providers.CodeLLMFailed)
		rep.code = cutCode(ctx, sctx, code)
		return rep
	}
	speakAll := len(tools) == 0 || forced || tr.mode.Interim == InterimSpeak
	forwarding := true
	var text strings.Builder
	stall := time.NewTimer(wait)
	defer stall.Stop()
	first := stall.C
	for chunks != nil {
		select {
		case <-first:
			rep.code = cutCode(ctx, sctx, CodeTimeout)
			return rep
		case <-sctx.Done():
			rep.code = cutCode(ctx, sctx, "")
			return rep
		case c, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			first = nil
			ls.chunk(r.d.Now().Sub(started).Milliseconds())
			if c.Err != nil {
				rep.err = c.Err
				code, _ := providers.ErrorFacts(c.Err, providers.CodeLLMFailed)
				rep.code = cutCode(ctx, sctx, code)
				return rep
			}
			if c.ToolCallStart && !speakAll && forwarding {
				forwarding = false
				if !r.discard(ctx, tr, &rep, out) {
					rep.code = ctxCode(ctx)
					return rep
				}
			}
			if c.Text != "" {
				text.WriteString(c.Text)
				if forwarding {
					if !r.say(sctx, tr, out, Out{Speech: c.Text}) {
						rep.code = cutCode(ctx, sctx, "")
						return rep
					}
					rep.sent += len(c.Text)
				}
			}
			rep.calls = append(rep.calls, c.ToolCalls...)
			if c.Usage != nil {
				rep.usage, rep.metered = *c.Usage, true
			}
		}
	}
	// a provider ends its stream early once its ctx is done
	if sctx.Err() != nil {
		rep.code = cutCode(ctx, sctx, "")
		return rep
	}
	if forced {
		// told to call none, so any it calls are not run
		rep.calls = nil
	}
	// a provider that does not mark where its tool calls start
	if len(rep.calls) > 0 && !speakAll && forwarding && !r.discard(ctx, tr, &rep, out) {
		rep.code = ctxCode(ctx)
		return rep
	}
	tr.usage.PromptTokens += rep.usage.PromptTokens
	tr.usage.CompletionTokens += rep.usage.CompletionTokens
	tr.metered = tr.metered || rep.metered
	rep.text = text.String()
	for i := range rep.calls {
		// a tool message must name its call, uniquely across the stored turns
		if rep.calls[i].ID == "" {
			rep.calls[i].ID = fmt.Sprintf("call_%s_%d_%d", tr.id, tr.steps, i)
		}
	}
	if strings.TrimSpace(rep.text) != "" {
		r.publish(tr, "llm_text", map[string]any{
			"step": tr.steps, "text": rep.text, "interim": len(rep.calls) > 0,
			"spoken": len(rep.calls) == 0 || (rep.sent > 0 && !rep.discarded),
		})
	}
	return rep
}

// discard drops the step's forwarded speech, if any.
func (r *Runner) discard(ctx context.Context, tr *turn, rep *reply, out chan<- Out) bool {
	if rep.sent == 0 || rep.discarded {
		return true
	}
	rep.discarded = true
	return r.say(ctx, tr, out, Out{Discard: true})
}

// cutCode prefers the turn's own end, then the step's bound, over code.
func cutCode(ctx, sctx context.Context, code string) string {
	switch {
	case ctx.Err() != nil:
		return ctxCode(ctx)
	case sctx.Err() != nil:
		return codeExpired
	case code == "":
		return CodeInternal
	}
	return code
}

func hasToolMessages(msgs []providers.Message) bool {
	return slices.ContainsFunc(msgs, func(m providers.Message) bool { return m.Role == roleTool })
}
