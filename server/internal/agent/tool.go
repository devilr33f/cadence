// Package agent holds the agent runtime: tools and their policy, and the agent
// modes that set a turn's model and budgets.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/jsonschemax"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// Effect is what a tool does to the world. The zero value counts as Write.
type Effect int

const (
	Read Effect = iota + 1
	Write
)

// Trust says whether a tool's output may carry someone else's instructions.
// The zero value counts as Untrusted.
type Trust int

const (
	Trusted Trust = iota + 1
	Untrusted
)

// DefaultTimeout bounds a tool that sets no Timeout of its own.
const DefaultTimeout = 10 * time.Second

// MaxHold bounds a Holds tool's Timeout: the voice turn's deadline keeps this
// much room beyond the mode's time, so a holding call is never cut by it.
const MaxHold = 100 * time.Second

// Tool is one function the model may call. Input is its arguments' JSON Schema.
type Tool struct {
	Name, Description, Input              string
	Effects                               Effect
	Output                                Trust
	Timeout                               time.Duration
	SilentOnSuccess, RequiresWokenSession bool
	// Holds lets a call run its whole Timeout (at most MaxHold) past the
	// step's and the mode's time, which the call's time does not count
	// against: a play waits for its real outcome instead of the turn ending
	// before the play starts.
	Holds bool
	// EventTurns offers a Write tool in event turns too, which otherwise get
	// only Read tools; its Run keeps to what such a turn may do.
	EventTurns bool
	// TaintExempt keeps a Write tool offered once untrusted output tainted a
	// microphone turn: its own policy, not the taint, bounds what it does.
	TaintExempt bool
	// DefaultOff offers the tool only where the device's tools setting names
	// it: an unset one (every tool) leaves it out, and a mode's list neither
	// adds nor removes it (Ruling M5d-R5).
	DefaultOff bool
	Run        func(ctx context.Context, c Call) (json.RawMessage, *ToolError)
	// Describe, on a registered tool, is its description as it is now, read
	// at each turn's snapshot in place of Description; it must be quick and
	// safe for concurrent use. nil: Description.
	Describe func() string
	// Available reports whether the device's settings let the tool work at
	// all; a tool that is not is not offered. nil: always.
	Available func(v settings.Values) bool
	// Reach reports whether the turn's device can reach what the tool acts
	// on (a screen-only tool: a screen it may drive); a tool that cannot is
	// not offered. nil: always.
	Reach func(d Devices) bool
	// NoEventTurns keeps the tool out of event turns even when it is Read
	// (the screen and catalog tools: an event turn never acts on a screen).
	NoEventTurns bool

	schema  *jsonschema.Schema // compiled Input, set by NewRegistry or Compile
	dynamic bool               // from a Source, set by the snapshot
	// turnScoped is a turn's own tool (load_skill): offered whatever the
	// tool lists say, by its policy alone
	turnScoped bool
}

// ErrInvalidSchema is a tool whose Input is not a valid JSON Schema.
var ErrInvalidSchema = errors.New("agent: invalid tool input schema")

// Compile returns t with its Input compiled, so that a Source compiles a
// tool once rather than at every turn's snapshot.
func Compile(t Tool) (Tool, error) {
	sch, err := jsonschemax.Compile(t.Input)
	if err != nil {
		return t, ErrInvalidSchema
	}
	t.schema = sch
	return t, nil
}

// Taints reports whether the tool's output taints the turn.
func (t Tool) Taints() bool { return t.Output != Trusted }

// TurnScoped reports whether the tool is a turn's own (load_skill,
// conversation_reset), whose effect the turn's own machinery applies: no
// caller outside a turn may run it.
func (t Tool) TurnScoped() bool { return t.turnScoped }

// Call is one tool call. Text is the turn's input: the user's words in a
// microphone turn, which a tool can check an argument claiming them against.
// TaskID is the short id of the task whose event started an event turn.
// TurnID names the turn and CallID this call in it, so a tool can tell what
// this turn did from what earlier ones did and finish a result after its reply.
type Call struct {
	DeviceID, SessionID, TurnID string
	// CallID is the model's id of the call, as the bus's tool_call names it
	CallID       string
	Trigger      cadencev2.Trigger
	Text, TaskID string
	Args         json.RawMessage
}

// ToolError goes back to the model as the call's result. Its text never
// carries upstream text; its Message may echo the model's own argument values
// (M3-R4), so it reaches only the model, the conversation store and the live
// feed, never a log.
type ToolError struct {
	Code       string   `json:"code"`
	Message    string   `json:"message,omitempty"`
	Hint       string   `json:"hint,omitempty"`
	Retryable  bool     `json:"retryable"`
	Candidates []string `json:"candidates,omitempty"`
}

// Validate checks the arguments against the tool's schema. The error says
// where and why they failed so the model can fix them; it may quote the
// model's own values, so it goes only to the model, the conversation store and
// the live feed, never to a log.
func Validate(t Tool, args json.RawMessage) *ToolError {
	sch := t.schema
	if sch == nil && t.Input != "" {
		var err error
		if sch, err = jsonschemax.Compile(t.Input); err != nil {
			return &ToolError{Code: "internal", Message: "The tool's input schema is invalid."}
		}
	}
	f, ok := jsonschemax.Check(sch, args)
	if ok {
		return nil
	}
	msg := "invalid arguments: " + f.Reason
	switch {
	case malformed(args):
		msg = "The arguments were not a valid JSON object (maybe cut off); send one JSON object."
	case f.Path != "":
		msg = fmt.Sprintf("invalid arguments at %s: %s", f.Path, f.Reason)
	}
	return &ToolError{
		Code:      "invalid_arguments",
		Message:   msg,
		Hint:      "Fix the arguments as the schema says and call the tool again.",
		Retryable: true,
	}
}

// malformed reports arguments that are not JSON, or that a provider passed on
// as a JSON string because they did not parse: "got string, want object"
// would not tell the model what went wrong.
func malformed(args json.RawMessage) bool {
	trimmed := bytes.TrimSpace(args)
	return !json.Valid(trimmed) || trimmed[0] == '"'
}

type outcome struct {
	out json.RawMessage
	err *ToolError
}

// Run calls the tool under its timeout and a recover guard. A tool that
// ignores its context is left behind once the deadline passes.
func Run(ctx context.Context, t Tool, c Call) (out json.RawMessage, terr *ToolError) {
	ctx, span := startToolSpan(ctx, t)
	defer func() { endToolSpan(span, terr) }()
	if t.RequiresWokenSession && !Woken(c.Trigger) {
		return nil, WakeRequired()
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan outcome, 1)
	go func() {
		defer func() {
			// the panic value may hold arguments, so it never reaches the error
			if recover() != nil {
				done <- outcome{err: &ToolError{Code: "internal", Message: "The tool failed."}}
			}
		}()
		out, err := t.Run(ctx, c)
		done <- outcome{out, err}
	}()
	select {
	case o := <-done:
		if o.err != nil && ctx.Err() != nil {
			return nil, ended(ctx)
		}
		return o.out, o.err
	case <-ctx.Done():
		return nil, ended(ctx)
	}
}

func ended(ctx context.Context) *ToolError {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &ToolError{Code: "timeout", Message: "The tool did not answer in time.", Retryable: true}
	}
	return &ToolError{Code: "canceled", Message: "The turn ended before the tool answered."}
}
