package screens

import (
	"context"
	"crypto/rand"
	"encoding/json"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

// InvokeAnswer is assistant.invoke's result: the turn the press opened and
// the speaker that listens, or, refused, the code and its toast.
type InvokeAnswer struct {
	TurnID      string `json:"turnId,omitempty"`
	MicDeviceID string `json:"micDeviceId,omitempty"`
	// Mic is "local" when the screen's own microphone serves it: cadenced
	// starts that session itself
	Mic   string `json:"mic,omitempty"`
	Code  string `json:"code,omitempty"`
	Toast string `json:"toast,omitempty"`
}

// InvokeMethod serves assistant.invoke {}: a press of a screen's assistant
// button asks the room's speaker to open its microphone (MicOpen), a session
// it starts with trigger BUTTON, so a pressed one for policy. The screen
// shows the turn as invoked at once; its states follow as the speaker's.
func (s *Service) InvokeMethod() calls.Method {
	return func(ctx context.Context, c calls.Call) calls.Reply {
		st, ok := s.screen(ctx, c.DeviceID)
		if !ok {
			return calls.Fail(rpc.StatusInvalid, "not_a_screen")
		}
		if st.MicKind == "local" {
			return calls.OK(InvokeAnswer{Mic: "local"})
		}
		speaker, code := s.speaker(ctx, c.DeviceID, st)
		if code != "" {
			s.d.Log.Info("assistant_invoke", "device_id", c.DeviceID, "code", code)
			return calls.Reply{Status: rpc.StatusUnavailable, JSON: InvokeAnswer{
				Code: code, Toast: s.d.Messages.Format(s.lang(ctx, c.DeviceID), "assistant."+code, nil),
			}}
		}
		session := rand.Text()
		s.pmu.Lock()
		s.pending[session] = pendingOpen{screen: c.DeviceID, speaker: speaker, at: s.d.Now()}
		s.pmu.Unlock()
		err := s.d.Send(speaker, &cadencev2.Envelope{Body: &cadencev2.Envelope_MicOpen{MicOpen: &cadencev2.MicOpen{
			SessionId: session, OriginDeviceId: c.DeviceID,
		}}})
		if err != nil {
			s.takePending(session)
			s.d.Log.Info("assistant_invoke", "device_id", c.DeviceID, "mic_device_id", speaker, "code", "mic_offline")
			return calls.Reply{Status: rpc.StatusUnavailable, JSON: InvokeAnswer{
				Code: "mic_offline", Toast: s.d.Messages.Format(s.lang(ctx, c.DeviceID), "assistant.mic_offline", nil),
			}}
		}
		_ = s.d.Send(c.DeviceID, &cadencev2.Envelope{Body: &cadencev2.Envelope_AssistantTurn{AssistantTurn: &cadencev2.AssistantTurn{
			TurnId: session, OriginDeviceId: c.DeviceID, Trigger: cadencev2.Trigger_TRIGGER_BUTTON,
			State:     cadencev2.AssistantState_ASSISTANT_STATE_INVOKED,
			MicSource: s.speakerMic(ctx, speaker),
		}}})
		s.d.Log.Info("assistant_invoke", "device_id", c.DeviceID, "mic_device_id", speaker, "session_id", session, "code", "ok")
		return calls.OK(InvokeAnswer{TurnID: session, MicDeviceID: speaker})
	}
}

// StopAnswer is assistant.stop's result: the session it stopped.
type StopAnswer struct {
	TurnID string `json:"turnId,omitempty"`
}

type stopTarget struct {
	device, session string
	// pending: the press's session has not started yet; it is stopped as it starts
	pending bool
}

// StopMethod serves assistant.stop {turnId}: the screen's stop (the drawer's
// stop, design §16) ends the voice session of the turn it shows, on whichever
// device of its room listens or speaks, as the device's own stop would. An
// empty or stale turnId stops the newest turn the screen shows (a follow-up
// is a session of its own). not_found: no turn of this screen runs.
func (s *Service) StopMethod() calls.Method {
	return func(ctx context.Context, c calls.Call) calls.Reply {
		var req struct {
			TurnID string `json:"turnId"`
		}
		if len(c.Args) > 0 && json.Unmarshal(c.Args, &req) != nil {
			return calls.Fail(rpc.StatusInvalid, "invalid_args")
		}
		if _, ok := s.screen(ctx, c.DeviceID); !ok {
			return calls.Fail(rpc.StatusInvalid, "not_a_screen")
		}
		if s.d.StopSession == nil {
			return calls.Fail(rpc.StatusUnavailable, "not_supported")
		}
		answer := make(chan stopTarget, 1)
		s.push(input{kind: inStop, dev: c.DeviceID, session: req.TurnID, stopped: answer})
		var tg stopTarget
		select {
		case tg = <-answer:
		case <-ctx.Done():
			return calls.Fail(rpc.StatusUnavailable, "timeout")
		}
		switch {
		case tg.pending:
			s.d.Log.Info("assistant_stop", "device_id", c.DeviceID, "session_id", tg.session, "code", "pending")
			return calls.OK(StopAnswer{TurnID: tg.session})
		case tg.device == "":
			s.d.Log.Info("assistant_stop", "device_id", c.DeviceID, "code", "not_found")
			return calls.Fail(rpc.StatusNotFound, "not_found")
		case !s.d.StopSession(tg.device, tg.session):
			// the turn's session ended as the stop came, and its close is on
			// its way here: a newer session of the same device (its follow-up) is
			// what the screen shows next
			if !s.d.StopSession(tg.device, "") {
				s.d.Log.Info("assistant_stop", "device_id", c.DeviceID, "code", "not_found")
				return calls.Fail(rpc.StatusNotFound, "not_found")
			}
		}
		s.d.Log.Info("assistant_stop", "device_id", c.DeviceID, "mic_device_id", tg.device, "session_id", tg.session, "code", "ok")
		return calls.OK(StopAnswer{TurnID: tg.session})
	}
}

// stopTarget finds the session a screen's stop ends: the turn it names when
// the screen shows it, else the newest it shows, else a press of its own not
// started yet. Run's goroutine alone calls it.
func (s *Service) stopTarget(screen, id string) stopTarget {
	if t := s.turns[id]; t != nil && contains(t.screens, screen) {
		return stopTarget{device: t.origin, session: t.id}
	}
	var newest *turn
	for _, t := range s.turns {
		if contains(t.screens, screen) && (newest == nil || t.started.After(newest.started)) {
			newest = t
		}
	}
	if newest != nil {
		return stopTarget{device: newest.origin, session: newest.id}
	}
	s.pmu.Lock()
	defer s.pmu.Unlock()
	if p, ok := s.pending[id]; ok && p.screen == screen {
		p.stopped = true
		s.pending[id] = p
		return stopTarget{session: id, pending: true}
	}
	return stopTarget{}
}
