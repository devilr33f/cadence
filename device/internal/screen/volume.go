package screen

import screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"

// ShowVolume tells the screen the level in force after cadenced applied it, whoever set it, and the system stream's
// mute where the level is the system's: the screen shows its volume overlay. A screen that connects later gets the
// latest as its state, with show false; show false here only records it (the state at start). It never blocks, so
// the setter's listeners may call it.
func (s *Server) ShowVolume(level int, muted, show bool) {
	v := &screenv1.VolumeState{Level: uint32(min(max(level, 0), 100)), Muted: muted} //nolint:gosec // clamped
	s.mu.Lock()
	s.volume = v
	s.mu.Unlock()
	s.send(volumeEnvelope(v, show))
}

func volumeEnvelope(v *screenv1.VolumeState, show bool) *screenv1.Envelope {
	return &screenv1.Envelope{Body: &screenv1.Envelope_VolumeState{VolumeState: &screenv1.VolumeState{
		Level: v.GetLevel(), Muted: v.GetMuted(), Show: show,
	}}}
}
