package tools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// HAConfirmationFrame is the fixed start of a late outcome's event turn. It
// is the server's own framing, never something the owner said.
const HAConfirmationFrame = "Home Assistant reported the final result of an earlier ha_control call after its reply; nothing was said."

// HAConfirmationRecorder appends a late outcome as an event turn, with the
// original arguments and call id. A pending result therefore never remains
// unresolved in the history a later model turn reads. Nothing is spoken: the
// model's turn has already ended, and late speech would interrupt a follow-up
// or a newer request.
func HAConfirmationRecorder(
	appendTurn func(context.Context, string, string, conversation.TurnMeta, []providers.Message) error,
	log *slog.Logger,
) func(context.Context, HAConfirmationRecord) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return func(ctx context.Context, rec HAConfirmationRecord) {
		args := rec.Args
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		callID := rec.CallID
		if callID == "" {
			callID = "late-" + rand.Text()[:12]
		}
		sum := sha256.Sum256([]byte(rec.OperationID))
		turnID := "ha-late-" + hex.EncodeToString(sum[:12])
		frame := HAConfirmationFrame
		if rec.TurnID != "" {
			frame += "\nOriginal turn: " + rec.TurnID
		}
		msgs := []providers.Message{
			{Role: "user", Content: frame},
			{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: callID, Name: agent.HAControl, Arguments: args}}},
			{Role: "tool", ToolCallID: callID, Content: string(rec.Result)},
		}
		ctx, cancel := context.WithTimeout(ctx, usageTimeout)
		defer cancel()
		if err := appendTurn(ctx, rec.DeviceID, turnID, conversation.TurnMeta{Trigger: conversation.TriggerEvent}, msgs); err != nil {
			log.Error("ha_confirmation_record_failed", "device_id", rec.DeviceID, "turn_id", turnID, "code", "store")
		}
	}
}
