package screen

import (
	"context"
	"errors"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
)

// a board without audiod has nobody to confirm the level (VolumeApplied), so cadenced holds it itself: the screen's
// player is the only thing that plays, levelled through its own volume (the DIRECT route). The gate opens only once
// the player answered ok to the level in force on this connection, and shuts when it refuses one or goes (rule 7):
// the level is never raised here, only the persisted one is handed on.

// DirectLevel takes the level in force (the volume setter's, loaded from the store or the default) for a board that
// runs no audiod (Options.DirectGate): the attached screen's player gets it, and every screen that attaches later.
// It never blocks.
func (s *Server) DirectLevel(level int) {
	if !s.o.DirectGate {
		return
	}
	level = min(max(level, 0), 100)
	s.mu.Lock()
	s.level, s.hasLevel = level, true
	p := s.conn
	s.mu.Unlock()
	if p != nil {
		go s.pushLevel(p, level)
	}
}

// pushLevel hands level to p's player and opens the gate once it holds it; a refusal, or a newer level, decides
// nothing here but its own
func (s *Server) pushLevel(p *peer, level int) {
	err := s.request(context.Background(), func(id string) *screenv1.Envelope {
		return &screenv1.Envelope{Body: &screenv1.Envelope_SetLevel{SetLevel: &screenv1.SetLevel{
			RequestId: id, Level: uint32(level), //nolint:gosec // 0-100: DirectLevel clamps every level it keeps
		}}}
	})
	s.mu.Lock()
	if s.conn != p || s.level != level {
		s.mu.Unlock()
		return
	}
	open := err == nil
	changed := s.levelHeld != open
	s.levelHeld = open
	s.mu.Unlock()
	if err != nil {
		s.log.Warn("screen_level_refused", "code", errCode(err))
	}
	if changed {
		s.log.Info("screen_gate", "open", open, "level", level)
		s.send(gateEnvelope(open))
	}
}

// attachLevel starts the level's push to a screen that just attached; its gate starts shut
func (s *Server) attachLevel(p *peer) {
	if !s.o.DirectGate {
		return
	}
	s.mu.Lock()
	level, ok := s.level, s.hasLevel
	s.mu.Unlock()
	if ok {
		go s.pushLevel(p, level)
	}
}

func errCode(err error) string {
	if ce, ok := errors.AsType[*capability.CommandError](err); ok {
		return ce.Code
	}
	return "failed"
}
