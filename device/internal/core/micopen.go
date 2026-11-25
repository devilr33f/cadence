package core

import (
	"git.shino.boo/devilreef/cadence/device/internal/audio"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// the server's session ids are at most this long, as the device's own
const maxSessionID = 64

// micOpen opens the microphone for a press on a screen in this device's room (rpc assistant.invoke there): a session
// begun by a press (TRIGGER_BUTTON, so a pressed session for policy, rule 7) under the id the server gave, by which it
// knows the screen that asked. A device without a microphone, a ring and the mute switch refuse it as a press would be.
func (s *Sync) micOpen(mo *cadencev2.MicOpen) {
	id := mo.GetSessionId()
	if id == "" || len(id) > maxSessionID || !s.hasAudio {
		s.log.Info("activation_refused", "reason", "mic_open", "trigger", cadencev2.Trigger_TRIGGER_BUTTON.String())
		return
	}
	if s.input != nil && s.input.Muted() {
		s.log.Info("activation_refused", "reason", "muted", "source", "mic_open")
		return
	}
	s.dispatch(audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Mode: audio.Supersede, Session: id})
}
