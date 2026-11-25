package volume

import "errors"

// ErrNoTransient refuses a ramp on a board whose output has no transient path.
var ErrNoTransient = errors.New("volume: no transient path")

// FollowTransient applies each level Transient raises the hardware to through apply: an alarm's ramp on the board's
// output, which never becomes the level in force there. Set it once, before the first Transient.
func (s *Setter) FollowTransient(apply func(level int) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transient = apply
}

// Transient raises the hardware to level, clamped to 0-100, for an alarm's ramp (rule 7 as amended), and reports
// whether it did. It never lowers: a level up to the one in force does nothing. Nothing is stored or heard, the level
// in force stays as it was, and the next plain change or Reassert puts it back on the hardware. ErrUnknown while the
// level is not the device's own, ErrNoTransient without a path, or the path's own error.
func (s *Setter) Transient(level int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.known {
		return false, ErrUnknown
	}
	level = clamp(level)
	if level <= s.level {
		return false, nil
	}
	if s.transient == nil {
		return false, ErrNoTransient
	}
	if err := s.transient(level); err != nil {
		return false, err
	}
	s.raised = level
	return true, nil
}

// Quiet sets the hardware to level, clamped to 0-100, at or below the level in force, for an alarm's quiet ramp, and
// reports whether it did: lowering is always allowed, and a level above the one in force does nothing (Transient
// raises). Nothing is stored or heard; the next plain change or Reassert puts the level in force back. ErrUnknown while
// the level is not the device's own, ErrNoTransient without a path, or the path's own error.
func (s *Setter) Quiet(level int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.known {
		return false, ErrUnknown
	}
	level = clamp(level)
	if level > s.level {
		return false, nil
	}
	if s.transient == nil {
		return false, ErrNoTransient
	}
	if err := s.transient(level); err != nil {
		return false, err
	}
	s.raised = level
	return true, nil
}

// Reassert puts the level in force on the hardware again with a plain set, as when an alarm's ramp ends, logging
// volume_reapplied when a transient level stood above it.
func (s *Setter) Reassert() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reassertLocked()
}

// callers hold mu
func (s *Setter) reassertLocked() {
	if s.raised > s.level {
		s.log.Info("volume_reapplied", "found", s.raised, "level", s.level)
	}
	s.raised = 0
	if s.apply != nil {
		s.apply(s.level)
	}
}

// DropTransient forgets the level Transient raised the hardware to, once the hardware lost it (the output's audiod
// restarted with the level in force, or its gate shut): Reassert then finds nothing above the level in force.
func (s *Setter) DropTransient() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raised = 0
}
