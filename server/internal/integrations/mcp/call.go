package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

const (
	// maxInFlight bounds one server's calls; a call past it waits for a slot
	maxInFlight = 4
	// callMargin ends a call this long before the agent's own deadline, so
	// the call's code answers, not the agent's timeout
	callMargin = 250 * time.Millisecond

	codeInvalidArguments = "invalid_arguments"
	codeCanceled         = "canceled"
)

// callee is a tool as a turn's snapshot offered it.
type callee struct {
	instance, local, remote, hash string
	effects                       agent.Effect
	timeout                       time.Duration
}

// delivery is how far a failed call got.
type delivery int

const (
	sent delivery = iota
	// unsent: the call never left, for want of a slot or a session
	unsent
	// stale: the catalog no longer offers the tool as the turn's snapshot
	// has it, so the call was not sent, and a retry in the turn fails alike
	stale
	// late: the call's deadline passed before it left
	late
)

// run is a tool's Run: one tools/call, never retried, and one log line of
// codes and counts. The agent validated the arguments against the schema.
func (s *Source) run(k callee) func(context.Context, agent.Call) (json.RawMessage, *agent.ToolError) {
	return func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
		start := time.Now()
		// the instance and the tool's own name, never its arguments or result
		ctx, span := tracing.StartKind(ctx, "mcp", "mcp.call", trace.SpanKindClient,
			attribute.String(tracing.AttrProvider, k.instance), attribute.String("cadence.tool", k.local))
		out, code, how := s.call(ctx, k, c.Args)
		span.SetAttributes(attribute.Int(tracing.AttrHTTPResponseSize, len(out)))
		if code != "" {
			tracing.Fail(span, code)
		}
		span.End()
		logged := code
		if logged == "" {
			logged = "ok"
		}
		s.log.Info("mcp_call", "instance", k.instance, "tool", k.local, "code", logged,
			"ms", time.Since(start).Milliseconds(), "bytes", len(out))
		if code != "" {
			return nil, toolError(code, k.effects, how)
		}
		return out, nil
	}
}

// call sends the call unless the catalog no longer offers the tool as the
// snapshot had it: a tool withdrawn or changed mid-turn never runs its new
// definition. It waits for one of the server's slots within its deadline.
func (s *Source) call(ctx context.Context, k callee, raw json.RawMessage) (json.RawMessage, string, delivery) {
	args, ok := decodeArgs(raw)
	if !ok {
		return nil, codeInvalidArguments, unsent
	}
	d := k.timeout - callMargin
	if dl, ok := ctx.Deadline(); ok {
		d = min(d, time.Until(dl)-callMargin)
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	// a deadline under the margin leaves no time for the call at all
	if ctx.Err() != nil {
		return nil, ctxCode(ctx), late
	}
	srv := s.cat.callee(k)
	if srv == nil {
		return nil, CodeUnavailable, stale
	}
	// a free slot is always taken, so a deadline that ends now reads late
	// below, never busy; only a call that waited for one is busy
	select {
	case srv.slots <- struct{}{}:
	default:
		select {
		case srv.slots <- struct{}{}:
		case <-ctx.Done():
			return nil, ctxCode(ctx), unsent
		}
	}
	defer func() { <-srv.slots }()
	if ctx.Err() != nil {
		return nil, ctxCode(ctx), late
	}
	// it may have changed while the call waited
	if s.cat.callee(k) != srv {
		return nil, CodeUnavailable, stale
	}
	res, err := srv.cfg.Client.CallTool(ctx, k.remote, args)
	if err != nil {
		code, how := Code(err), sent
		if code == "" {
			code = ctxCode(ctx)
		}
		switch {
		case notSent(err) && ctx.Err() != nil:
			return nil, code, late
		case notSent(err):
			how = unsent
		}
		// the server's fault: neither the tool's own error nor the turn's end
		if code != CodeToolError && code != codeCanceled {
			s.cat.CallFailed(k.instance, code)
		}
		return nil, code, how
	}
	out, code := render(res, srv.cfg.Client.MaxResultChars(), k.effects)
	return out, code, sent
}

// callee is the running server of a snapshot's tool, nil unless the catalog
// still offers its local name for the same remote tool and hash.
func (c *Catalog) callee(k callee) *server {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.offered {
		if e.Local == k.local {
			if e.Instance != k.instance || e.Remote != k.remote || e.Override.Hash != k.hash {
				return nil
			}
			return c.servers[k.instance]
		}
	}
	return nil
}

// decodeArgs keeps the arguments as the model wrote them: numbers whole,
// nothing coerced or filled in.
func decodeArgs(raw json.RawMessage) (map[string]any, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var args map[string]any
	if err := dec.Decode(&args); err != nil {
		return nil, false
	}
	return args, true
}

// ctxCode names a failure that came without a code by the call's ctx.
func ctxCode(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return CodeTimeout
	}
	if ctx.Err() != nil {
		return codeCanceled
	}
	return CodeUnavailable
}

// toolErrors are what the model reads of a failed call: fixed words, never
// the server's text, which may carry instructions.
var toolErrors = map[string]agent.ToolError{
	// retryable for Read tools only: toolError
	CodeUnavailable: {Message: "The tool's MCP server is not available now.", Retryable: true},
	CodeTimeout:     {Message: "The tool's MCP server did not answer in time.", Retryable: true},
	// a refused credential is the owner's to fix
	CodeAuth:        {Message: "This tool is unavailable."},
	CodeProtocol:    {Message: "The tool's MCP server sent an answer that could not be read."},
	CodeToolError:   {Message: "The tool reported an error."},
	CodeUnknownTool: {Message: "The MCP server no longer has this tool.", Hint: "Answer without it."},
	CodeTooLarge: {
		Message: "The tool ran, but its result was too large to read.",
		Hint:    "Do not call it again for this: tell the user it ran.",
	},
	codeInvalidArguments: {
		Message: "The arguments were not a JSON object.",
		Hint:    "Fix the arguments as the schema says and call the tool again.", Retryable: true,
	},
	codeCanceled: {Message: "The turn ended before the tool answered."},
}

// unsentMessages say a call that never left was not sent.
var unsentMessages = map[string]string{
	CodeUnavailable: "The tool's MCP server is not available now, so the call was not sent.",
	CodeTimeout:     "The tool's MCP server is busy with other calls, so the call was not sent.",
}

// toolError is code's error for a tool of effects that got as far as how: a
// Write tool that timed out, lost its server or got an answer that could not
// be read may have had its effect, so the model is not told to try it again
// (Ruling M5d-R9); one never sent is told it was not.
func toolError(code string, effects agent.Effect, how delivery) *agent.ToolError {
	if how == stale {
		return &agent.ToolError{
			Code: code, Message: "The tool changed or was withdrawn after the turn began, so the call was not sent.",
			Hint: "Answer without it.",
		}
	}
	if how == late {
		// the turn's own time ran out, whatever else failed meanwhile: a
		// retry in it fails alike
		msg := "No time was left for the tool, so the call was not sent."
		if code == codeCanceled {
			msg = "The turn ended, so the call was not sent."
		}
		return &agent.ToolError{Code: code, Message: msg}
	}
	e, ok := toolErrors[code]
	if !ok {
		e = toolErrors[CodeUnavailable]
	}
	e.Code = code
	transient := code == CodeUnavailable || code == CodeTimeout
	switch {
	case transient && how == unsent:
		e.Message = unsentMessages[code]
		e.Retryable = effects == agent.Read
	case (transient || code == CodeProtocol) && how == sent && effects != agent.Read:
		e.Retryable, e.Hint = false, "It may have run already: do not call it again for this."
	}
	return &e
}
