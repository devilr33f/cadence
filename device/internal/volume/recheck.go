package volume

import (
	"context"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/policy"
)

// Recheck puts the level in force back on the hardware, as Reassert does, unless it is not the device's own or
// ringing says an alarm or a timer rings, whose stop restores it (M6a-D8): another writer may have moved the hardware
// since the level held. ringing runs under the setter's lock, so no ramp step slips in between; it must not block or
// call the setter, and nil never rings. It reports whether it put the level back.
func (s *Setter) Recheck(ringing func() bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !policy.VolumeReassert(policy.VolumeFacts{Known: s.known, Ringing: ringing != nil && ringing()}) {
		return false
	}
	s.reassertLocked()
	return true
}

// RecheckWhilePlaying runs Recheck every interval while playing says media plays, until ctx ends.
func (s *Setter) RecheckWhilePlaying(ctx context.Context, every time.Duration, playing, ringing func() bool) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if playing() {
				s.Recheck(ringing)
			}
		}
	}
}
