package screens_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/screens"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

func TestAssistantResetUsesTheShownTurnsConversation(t *testing.T) {
	var targets []string
	var r *rig
	r = newRig(t, func(d *screens.Deps) {
		d.ResetConversation = func(_ context.Context, deviceID string) error {
			targets = append(targets, deviceID)
			// the production resetter's OnReset callback
			r.svc.ConversationReset(deviceID, "")
			return nil
		}
	})
	r.svc.Inbound("spk", &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionStart{SessionStart: &cadencev2.SessionStart{
		SessionId: "s1", Trigger: cadencev2.Trigger_TRIGGER_WAKE,
	}}})
	r.await("tv", "listening", func(at *cadencev2.AssistantTurn) bool { return at.GetTurnId() == "s1" })
	r.svc.Transcript("spk", "s1", "Сброс", true)
	r.await("tv", "the transcript", func(at *cadencev2.AssistantTurn) bool { return at.GetTranscript().GetStable() == "Сброс" })

	reply := r.svc.ResetMethod()(context.Background(), calls.Call{DeviceID: "tv", ID: "r1", Args: []byte(`{}`)})
	if reply.Status != rpc.StatusOK || !slices.Equal(targets, []string{"spk"}) {
		t.Fatalf("reply %+v, targets %v", reply, targets)
	}
	at := r.await("tv", "the reset", func(at *cadencev2.AssistantTurn) bool { return at.GetConversationReset() })
	if at.GetTurnId() != "s1" || at.GetState() != cadencev2.AssistantState_ASSISTANT_STATE_RESTING ||
		at.GetReply().GetText() != "Начинаем заново." || at.GetTranscript().GetStable() != "" || len(at.GetSteps()) != 0 || len(at.GetCards()) != 0 {
		t.Fatalf("reset state %v", at)
	}
}

func TestAssistantResetWithoutALiveTurn(t *testing.T) {
	for _, tc := range []struct {
		name, screen, state, target, status string
	}{
		{"configured speaker, even offline", "tv", `{"screen":true,"mic":{"kind":"speaker","deviceId":"offline"}}`, "offline", rpc.StatusOK},
		{"local microphone", "tv", `{"screen":true,"mic":{"kind":"local"}}`, "tv", rpc.StatusOK},
		{"no microphone", "kitchen-tv", `{"screen":true,"mic":{"kind":"none"}}`, "", rpc.StatusUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var targets []string
			var r *rig
			r = newRig(t, func(d *screens.Deps) {
				d.State = func(context.Context, string, string) (json.RawMessage, bool) { return json.RawMessage(tc.state), true }
				d.ResetConversation = func(_ context.Context, deviceID string) error {
					targets = append(targets, deviceID)
					r.svc.ConversationReset(deviceID, "")
					return nil
				}
			})
			reply := r.svc.ResetMethod()(context.Background(), calls.Call{DeviceID: tc.screen, ID: "r1", Args: []byte(`{}`)})
			if reply.Status != tc.status {
				t.Fatalf("reply %+v", reply)
			}
			if tc.target == "" {
				if len(targets) != 0 {
					t.Fatalf("targets %v", targets)
				}
				return
			}
			if !slices.Equal(targets, []string{tc.target}) {
				t.Fatalf("targets %v", targets)
			}
			if tc.target == "tv" {
				at := r.await("tv", "the standalone reset", func(at *cadencev2.AssistantTurn) bool { return at.GetConversationReset() })
				if at.GetReply().GetText() != "Начинаем заново." || at.GetTurnId() == "" {
					t.Fatalf("reset %v", at)
				}
			}
		})
	}
}
