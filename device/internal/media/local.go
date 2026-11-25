package media

import (
	"context"
	"encoding/json"
	"strconv"

	"google.golang.org/protobuf/proto"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// Local is the device's own view of its media session, for a screen on the device: the state the server gets (an idle
// one when there is none), the sending device's name and the cover once read. The sender and the cover stay on the
// device; neither is ever logged (D11).
type Local struct {
	State  *cadencev2.MediaSessionState
	Sender string
	// nil while the session names no cover, or it is not read or fetched yet
	Cover *Cover
}

// Cover is a cover a source handed audiod as bytes (airplay), as read from audiod's scratch, or one the server served
// for the session (a multiroom group's stream source's, an artwork url's).
type Cover struct {
	SHA256 string
	// image/jpeg or image/png
	Type string
	Data []byte
}

// UseLocal has f hear the session after every change, and again once its cover is read; set it before audiod's first
// Ready. f must never block: it runs with the session's state held.
func (m *Media) UseLocal(f func(Local)) {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	m.local = f
}

// notifyLocal hands the latest session to the local view; callers hold stateMu
func (m *Media) notifyLocal() {
	if m.local == nil {
		return
	}
	l := Local{State: &cadencev2.MediaSessionState{State: cadencev2.PlaybackState_PLAYBACK_STATE_IDLE}}
	if m.latest != nil {
		l.State = proto.CloneOf(m.latest)
		l.Sender = m.latestSender
		l.Cover = m.cover(m.latestArt)
		if l.Cover == nil {
			l.Cover = m.serverCover()
		}
	}
	m.local(l)
}

// Transport carries out a transport action (pause, resume, stop, seek, next, previous) on the session as media.player's
// command does: the device's own screen's controls of a session audiod renders. A code comes back as the command's.
func (m *Media) Transport(ctx context.Context, action string, positionMs uint64) error {
	args := json.RawMessage(`{}`)
	if action == "seek" {
		args = json.RawMessage(`{"positionMs":` + strconv.FormatUint(positionMs, 10) + `}`)
	}
	_, err := m.player.Command(ctx, action, args)
	return err
}
