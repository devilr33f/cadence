package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/link"
	"git.shino.boo/devilreef/cadence/device/internal/session"
)

// startSession runs a session controller on this link when the board has audio; it takes the triggers routed to the
// link from now until it ends.
func (s *Sync) startSession(ctx context.Context, sess link.Session) {
	if !s.hasAudio {
		return
	}
	opts := []session.Option{session.WithActivity(s.activity), session.WithErrorSounds(s.errors)}
	if t, ok := s.board.(board.SessionTracker); ok {
		opts = append(opts, session.WithRecent(t.Sessions()))
	}
	if s.input != nil {
		opts = append(opts, session.WithMuted(s.input.Muted))
	}
	if s.vol != nil {
		opts = append(opts, session.WithBeforeReply(s.recheck))
	}
	if s.ind != nil {
		opts = append(opts, session.WithPhases(s.ind.Phase))
	}
	if s.ringer != nil {
		opts = append(opts, session.WithRinging(s.ringer.Ringing))
	}
	if s.counts != nil {
		opts = append(opts, session.WithCounts(s.counts))
	}
	triggers := make(chan audio.Trigger, linkTriggers)
	ctl := session.New(audio.IO{Triggers: triggers, Mic: s.aio.Mic, Speaker: s.aio.Speaker}, sess.Send, randomID, s.log, opts...)
	s.ctl = ctl
	s.mu.Lock()
	s.triggers = triggers
	s.mu.Unlock()
	s.wg.Go(func() { ctl.Run(ctx) })
}

func randomID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
