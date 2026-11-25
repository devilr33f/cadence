package core

import (
	"context"

	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/link"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// forwardMedia sends the board's latest media session state at once, as the
// server forgets it on a disconnect, then every new one, until the link ends.
// The state carries metadata: it is never logged.
func (s *Sync) forwardMedia(ctx context.Context, sess link.Session) {
	mr, ok := s.board.(board.MediaReporter)
	if !ok {
		return
	}
	ch := mr.MediaSessions()
	if ch == nil {
		return
	}
	s.wg.Go(func() {
		select {
		case v := <-ch:
			s.rememberMedia(v)
		default:
		}
		if v := s.lastMedia(); v != nil {
			s.sendMedia(sess, v)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case v := <-ch:
				s.rememberMedia(v)
				s.sendMedia(sess, v)
			}
		}
	})
}

func (s *Sync) rememberMedia(v *cadencev2.MediaSessionState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.media = v
}

func (s *Sync) lastMedia() *cadencev2.MediaSessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.media
}

// sendMedia drops a failed send: the state is kept and goes out on the next link.
func (s *Sync) sendMedia(sess link.Session, v *cadencev2.MediaSessionState) {
	_ = sess.Send(&cadencev2.Envelope{Body: &cadencev2.Envelope_MediaSessionState{MediaSessionState: v}})
}
