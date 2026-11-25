package screens

import (
	"context"
	"crypto/rand"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

// ResetMethod serves assistant.reset {} for the conversation the drawer's room
// uses: its shown turn's origin first, else its configured microphone. The
// callback also fans the reset state out to the room's screens.
func (s *Service) ResetMethod() calls.Method {
	return func(ctx context.Context, c calls.Call) calls.Reply {
		st, ok := s.screen(ctx, c.DeviceID)
		if !ok {
			return calls.Fail(rpc.StatusInvalid, "not_a_screen")
		}
		if s.d.ResetConversation == nil {
			return calls.Fail(rpc.StatusUnavailable, "not_supported")
		}
		answer := make(chan string, 1)
		s.push(input{kind: inResetTarget, dev: c.DeviceID, target: answer})
		var target string
		select {
		case target = <-answer:
		case <-ctx.Done():
			return calls.Fail(rpc.StatusUnavailable, "timeout")
		}
		if target == "" {
			switch {
			case st.MicKind == "local":
				target = c.DeviceID
			case st.MicKind == "speaker" && st.MicDevice != "":
				// the conversation can be reset while its speaker is offline
				target = st.MicDevice
			default:
				var code string
				target, code = s.speaker(ctx, c.DeviceID, st)
				if code != "" {
					return calls.Reply{Status: rpc.StatusUnavailable, JSON: InvokeAnswer{
						Code: code, Toast: s.d.Messages.Format(s.lang(ctx, c.DeviceID), "assistant."+code, nil),
					}}
				}
			}
		}
		if err := s.d.ResetConversation(ctx, target); err != nil {
			s.d.Log.Warn("assistant_reset", "device_id", c.DeviceID, "mic_device_id", target, "code", "failed")
			return calls.Fail(rpc.StatusUnavailable, "unavailable")
		}
		s.d.Log.Info("assistant_reset", "device_id", c.DeviceID, "mic_device_id", target, "code", "ok")
		return calls.OK(struct{}{})
	}
}

// ConversationReset clears the shown chat of a device's room after its context
// boundary moved. sessionID names the voice turn to replace, empty the newest.
func (s *Service) ConversationReset(deviceID, sessionID string) {
	s.push(input{kind: inReset, dev: deviceID, session: sessionID})
}

// resetTarget is the origin of the newest turn the screen shows. Run's
// goroutine alone calls it.
func (s *Service) resetTarget(screen string) string {
	var newest *turn
	for _, t := range s.turns {
		if contains(t.screens, screen) && (newest == nil || t.started.After(newest.started)) {
			newest = t
		}
	}
	if newest != nil {
		return newest.origin
	}
	return ""
}

func (s *Service) resetConversation(ctx context.Context, deviceID, sessionID string) {
	t := s.turns[sessionID]
	if t != nil && t.origin != deviceID {
		t = nil
	}
	if t == nil {
		for _, candidate := range s.turns {
			if candidate.origin == deviceID && (t == nil || candidate.started.After(t.started)) {
				t = candidate
			}
		}
	}
	if t != nil {
		t.transcript, t.stable, t.unstable = "", "", ""
		t.steps, t.reply, t.attached = nil, nil, nil
		t.cards = map[string][]*cardsv1.Card{}
		t.errorCode, t.window = "", 0
		t.state, t.reset = cadencev2.AssistantState_ASSISTANT_STATE_RESTING, true
		s.send(ctx, t)
		t.reset = false
		s.duck(t, false)
		delete(s.turns, t.id)
		s.setPressed(t.id, "")
		return
	}
	// no live turn: a standalone resting state still clears an open drawer
	screens, _ := s.room(ctx, deviceID)
	id := "reset_" + rand.Text()
	for _, scr := range screens {
		lang := s.lang(ctx, scr)
		_ = s.d.Send(scr, &cadencev2.Envelope{Body: &cadencev2.Envelope_AssistantTurn{AssistantTurn: &cadencev2.AssistantTurn{
			TurnId: id, Revision: 1, OriginDeviceId: deviceID, Trigger: cadencev2.Trigger_TRIGGER_WAKE, State: cadencev2.AssistantState_ASSISTANT_STATE_RESTING,
			ConversationReset: true,
			Reply:             &cadencev2.AssistantReply{Text: s.d.Messages.Format(lang, "assistant.conversation_reset", nil)},
		}}})
	}
}
