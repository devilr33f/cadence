package agent

import cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"

// Woken reports whether the user began the session with the wake word or a
// press. Rule 7: only such a session may raise the volume or set an alarm,
// since a follow-up hears whoever talks after a reply.
func Woken(t cadencev2.Trigger) bool {
	return t == cadencev2.Trigger_TRIGGER_WAKE || t == cadencev2.Trigger_TRIGGER_BUTTON
}

// WakeRequired is the refusal of a rule-7 action outside a woken session.
func WakeRequired() *ToolError {
	return &ToolError{
		Code:    "wake_required",
		Message: "This needs a session the user began with the wake word or a button press.",
		Hint:    "Tell the user to wake you first.",
	}
}

// EventTurn reports a turn the server started for a task event: nobody spoke,
// and its input is task output nobody vouched for.
func EventTurn(t cadencev2.Trigger) bool { return t == cadencev2.Trigger_TRIGGER_EVENT }

// NotInEventTurn is the refusal of an action an event turn may not take.
func NotInEventTurn() *ToolError {
	return &ToolError{
		Code:    "not_in_event_turn",
		Message: "This cannot be done in a turn a task event started.",
		Hint:    "Say what happened; the user can ask for it in their next session.",
	}
}

// offered keeps a tainted turn to tools that change nothing, and to the ones
// whose own policy bounds them (TaintExempt, Ruling M5b-R4): untrusted output
// may carry instructions to call the others. An event turn reads such output
// from its start, so it gets Read tools and the ones marked for it, tainted
// or not (M5b-D4).
func offered(t Tool, tainted bool, trigger cadencev2.Trigger) bool {
	if EventTurn(trigger) {
		return !t.NoEventTurns && (t.Effects == Read || t.EventTurns)
	}
	return !tainted || t.Effects == Read || t.TaintExempt
}
