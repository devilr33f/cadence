package tools_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// a late outcome is a server event linked to the original call, not another
// user's request and not speech injected into an already-finished reply
func TestHAConfirmationRecorderAppendsTheFinalOutcome(t *testing.T) {
	type appended struct {
		device, turn string
		meta         conversation.TurnMeta
		msgs         []providers.Message
	}
	var got []appended
	record := tools.HAConfirmationRecorder(func(_ context.Context, device, turn string, meta conversation.TurnMeta, msgs []providers.Message) error {
		got = append(got, appended{device, turn, meta, msgs})
		return nil
	}, slog.New(slog.DiscardHandler))
	for name, result := range map[string]string{
		"done":   `{"ok":true,"data":{"done":"turn_off","late":true,"state":"off"}}`,
		"failed": `{"ok":false,"error":{"code":"no_effect","message":"The device took the command but still reads \"cool\"."}}`,
	} {
		t.Run(name, func(t *testing.T) {
			before := len(got)
			record(context.Background(), tools.HAConfirmationRecord{
				DeviceID: "d1", TurnID: "turn-original", CallID: "call-original", OperationID: "turn-original:call-original",
				Args: json.RawMessage(`{"action":"turn_off","target":"air conditioner"}`), Result: json.RawMessage(result),
			})
			if len(got) != before+1 {
				t.Fatalf("%d turns", len(got))
			}
			a := got[len(got)-1]
			if a.device != "d1" || !strings.HasPrefix(a.turn, "ha-late-") || a.meta.Trigger != conversation.TriggerEvent || len(a.msgs) != 3 {
				t.Fatalf("appended %+v", a)
			}
			if !strings.HasPrefix(a.msgs[0].Content, tools.HAConfirmationFrame) || !strings.Contains(a.msgs[0].Content, "turn-original") {
				t.Fatalf("frame %q", a.msgs[0].Content)
			}
			calls := a.msgs[1].ToolCalls
			if len(calls) != 1 || calls[0].ID != "call-original" || calls[0].Name != "ha_control" || string(calls[0].Arguments) == "" ||
				a.msgs[2].ToolCallID != "call-original" || a.msgs[2].Content != result {
				t.Fatalf("messages %+v", a.msgs)
			}
		})
	}
}
