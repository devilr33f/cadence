package media

import (
	"context"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

const codeNoSession = "no_session"

// StopScreencast stops audiod's media session while airplay mirroring holds it (the screen's stop key): audiod lets
// the sender go and reports the mirror ended with reason stopped. no_session when another session holds the speaker,
// or none does, so a stop meant for the mirror never ends what took its place
func (m *Media) StopScreencast(ctx context.Context) error {
	if _, ext := m.currentExternal(); ext != nil {
		return &capability.CommandError{Code: codeNoSession}
	}
	m.stateMu.Lock()
	latest := m.latest
	m.stateMu.Unlock()
	if latest.GetSource() != cadencev2.MediaSource_MEDIA_SOURCE_SCREENCAST ||
		latest.GetState() == cadencev2.PlaybackState_PLAYBACK_STATE_IDLE {
		return &capability.CommandError{Code: codeNoSession}
	}
	return m.request(ctx, func(id string) *audiodv1.Envelope {
		mc := &audiodv1.MediaCommand{RequestId: id, Action: audiodv1.MediaAction_MEDIA_ACTION_STOP}
		return &audiodv1.Envelope{Body: &audiodv1.Envelope_MediaCommand{MediaCommand: mc}}
	})
}
