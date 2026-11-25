package agent

import (
	"context"
	"encoding/json"
)

// ConversationReset is the turn-scoped tool that starts the device's next turn
// with an empty conversation context. Long-term memories are not deleted.
const ConversationReset = "conversation_reset"

const conversationResetInput = `{"type":"object","additionalProperties":false}`

// ConversationResetTool is available in every turn, including a follow-up or a
// tainted/event turn: it changes only this device's context boundary. The
// runner applies a successful call after the turn commits.
func ConversationResetTool() Tool {
	return Tool{
		Name: ConversationReset,
		Description: "Start the current conversation over when the user asks for a clean slate or to forget this conversation. " +
			"This does not delete long-term memories or stored history. Applies from the next turn; after calling it, briefly confirm that the conversation starts over.",
		Input: conversationResetInput, Effects: Write, Output: Trusted,
		EventTurns: true, TaintExempt: true, turnScoped: true,
		Run: func(context.Context, Call) (json.RawMessage, *ToolError) {
			return json.RawMessage(`{"reset":"next_turn"}`), nil
		},
	}
}
