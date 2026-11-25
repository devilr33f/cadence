package cards

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// ScreenFrame is a recorded card action's user message: fixed English of our
// own, never anything a person said (the memory learner reads it as the
// server's, conversation.TriggerScreen).
const ScreenFrame = "A press on a screen's remote ran a card's action; nothing was said."

const recordTimeout = 2 * time.Second

// Appender is the conversation store's commit.
type Appender func(ctx context.Context, deviceID, turnID string, meta conversation.TurnMeta, msgs []providers.Message) error

// Recorder keeps a card action in the device's history like a tool call: a
// turn of trigger screen with the framing, the call and its result. A failed
// write is logged by its ids alone.
func Recorder(appendTurn Appender, log *slog.Logger) func(ctx context.Context, deviceID string, r Record) {
	return func(ctx context.Context, deviceID string, r Record) {
		args, err := json.Marshal(r.Args)
		if err != nil {
			return
		}
		result, err := json.Marshal(r.Result)
		if err != nil {
			return
		}
		turn := "screen-" + rand.Text()[:16]
		call := "call-" + rand.Text()[:12]
		msgs := []providers.Message{
			{Role: "user", Content: ScreenFrame},
			{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: call, Name: r.Tool, Arguments: args}}},
			{Role: "tool", ToolCallID: call, Content: string(result)},
		}
		ctx, cancel := context.WithTimeout(ctx, recordTimeout)
		defer cancel()
		if err := appendTurn(ctx, deviceID, turn, conversation.TurnMeta{Trigger: conversation.TriggerScreen}, msgs); err != nil {
			log.Error("card_record_failed", "device_id", deviceID, "turn_id", turn)
		}
	}
}
