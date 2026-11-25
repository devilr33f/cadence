package media

import (
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// SetOutputDelay sets the board's audio delay trim (audio.output's delayMs, clamped to its bounds): sent to audiod now
// and again after every Ready, so it overrides the trim of the board's Configure without reopening the output.
func (m *Media) SetOutputDelay(ms int) {
	ms = min(max(ms, capschema.AudioDelayMinMs), capschema.AudioDelayMaxMs)
	m.setMu.Lock()
	defer m.setMu.Unlock()
	m.delay, m.delaySet = int32(ms), true //nolint:gosec // bounded above
	m.mu.Lock()
	send := m.send
	m.mu.Unlock()
	if send != nil {
		m.sendDelayTo(send)
	}
}

// callers hold setMu
func (m *Media) sendDelayTo(send func(*audiodv1.Envelope) error) {
	if !m.delaySet {
		return
	}
	_ = send(&audiodv1.Envelope{Body: &audiodv1.Envelope_OutputDelay{OutputDelay: &audiodv1.OutputDelay{DelayMs: m.delay}}})
}
