package tools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"

	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// PlayOutcomeFrame is the fixed start of a late play outcome's event turn.
// It is the server's own framing, never something the owner said.
const PlayOutcomeFrame = "The screen reported the final result of an earlier play call after its reply; nothing was said."

// PlayOutcomeRecorder appends a pending play's final outcome with the original
// tool name, arguments and call id. Nothing is spoken: the model's turn has
// already ended, and a later turn reads the outcome in its history.
func PlayOutcomeRecorder(
	appendTurn func(context.Context, string, string, conversation.TurnMeta, []providers.Message) error,
	log *slog.Logger,
) func(context.Context, PlayRecord) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return func(ctx context.Context, rec PlayRecord) {
		args := rec.Args
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		callID := rec.CallID
		if callID == "" {
			callID = "late-" + rand.Text()[:12]
		}
		sum := sha256.Sum256([]byte(rec.OperationID))
		turnID := "play-late-" + hex.EncodeToString(sum[:12])
		frame := PlayOutcomeFrame
		if rec.TurnID != "" {
			frame += "\nOriginal turn: " + rec.TurnID
		}
		msgs := []providers.Message{
			{Role: "user", Content: frame},
			{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: callID, Name: rec.Tool, Arguments: args}}},
			{Role: "tool", ToolCallID: callID, Content: string(rec.Result)},
		}
		ctx, cancel := context.WithTimeout(ctx, usageTimeout)
		defer cancel()
		if err := appendTurn(ctx, rec.DeviceID, turnID, conversation.TurnMeta{Trigger: conversation.TriggerEvent}, msgs); err != nil {
			log.Error("play_outcome_record_failed", "device_id", rec.DeviceID, "turn_id", turnID, "code", "store")
		}
	}
}
