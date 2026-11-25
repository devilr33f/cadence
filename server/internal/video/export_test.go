package video

import "git.shino.boo/devilreef/cadence/server/internal/screens/calls"

// SwitchMethod is the screen's media.video method, for tests
func (s *Service) SwitchMethod() calls.Method { return s.switchMethod }
