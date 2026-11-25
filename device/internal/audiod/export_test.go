package audiod

import "git.shino.boo/devilreef/cadence/device/internal/audio"

var DefaultTimings = defaultTimings

const VendorProbeLimit = vendorProbeLimit

// NewTestIO serves c's envelopes as the audio.IO the supervisor builds for
// each connection; it takes over c.Recv().
func NewTestIO(c *Client) audio.IO {
	a := newIO(c, c.Recv(), defaultTimings.FirstPacket, defaultTimings.FirstPacket)
	return audio.IO{Mic: a, Speaker: a}
}

// StreamPending reports whether the current connection still waits for the
// StreamDone of id.
func StreamPending(s *Supervisor, id string) bool {
	a := s.current()
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.streams[id]
	return ok
}
