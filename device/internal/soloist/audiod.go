package soloist

import (
	"context"
	"strconv"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/soloist/updater"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

const (
	// soloist starts at it while the device has no level of its own (reference §5)
	unknownLevel = 50
	// audiod runs a probe within 10 s, reaped within 5 more, maybe behind another
	probeWait = 30 * time.Second
)

// SoloistReady sends a new audiod connection the config, then the level when
// the device has one of its own, and only then uses the connection: audiod's
// sender guard moves nothing until it knows the speaker's level.
func (s *Soloist) SoloistReady(send func(*audiodv1.Envelope) error) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	_ = send(s.configEnvelope())
	if level, known := s.vol.Current(); known {
		for _, e := range levelEnvelopes(level) {
			_ = send(e)
		}
	}
	s.mu.Lock()
	s.send, s.status = send, nil
	s.publishLocked()
	s.mu.Unlock()
}

// SoloistEnvelope takes a SoloistStatus, SoloistProbeResult or SenderVolume
// from audiod; it never blocks.
func (s *Soloist) SoloistEnvelope(env *audiodv1.Envelope) {
	switch b := env.GetBody().(type) {
	case *audiodv1.Envelope_SoloistStatus:
		s.statusChanged(b.SoloistStatus)
	case *audiodv1.Envelope_SoloistProbeResult:
		s.mu.Lock()
		answer, ok := s.probes[b.SoloistProbeResult.GetRequestId()]
		delete(s.probes, b.SoloistProbeResult.GetRequestId())
		s.mu.Unlock()
		if ok {
			answer <- b.SoloistProbeResult
		}
	case *audiodv1.Envelope_SenderVolume:
		s.policy.request(b.SenderVolume.GetSender(), b.SenderVolume.GetLevel())
	}
}

// SoloistGone follows the end of the connection SoloistReady took: a probe
// waiting on it gets no answer.
func (s *Soloist) SoloistGone() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.send, s.status = nil, nil
	for _, answer := range s.probes {
		answer <- nil
	}
	clear(s.probes)
	s.publishLocked()
}

// statusChanged keeps audiod's status for the state, and for the updates
// loop an edge into no_build or expired (a check at once) and the silent starts
func (s *Soloist) statusChanged(st *audiodv1.SoloistStatus) {
	s.mu.Lock()
	prev := s.status
	s.status = st
	if prev == nil || prev.GetState() != st.GetState() {
		switch st.GetState() {
		case audiodv1.SoloistState_SOLOIST_STATE_NO_BUILD:
			s.noBuild = true
		case audiodv1.SoloistState_SOLOIST_STATE_EXPIRED:
			s.expired = true
		}
	}
	if n := st.GetSilentStarts(); !s.silentSeen || n < s.silentMin {
		s.silentMin = n
	}
	s.silentSeen = true
	s.publishLocked()
	s.mu.Unlock()
	s.kickUpdates()
}

// sendLevel tells soloist the level in force, when the device has one; it
// reads the level under sendMu, so a later level never goes out before it
func (s *Soloist) sendLevel() {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.mu.Lock()
	send := s.send
	s.mu.Unlock()
	level, known := s.vol.Current()
	if send != nil && known {
		for _, e := range levelEnvelopes(level) {
			_ = send(e)
		}
	}
}

// switchBuild has audiod start the installed build once nothing plays. With
// audiod down there is none to switch: its next start takes the installed one.
func (s *Soloist) switchBuild(reason string) {
	s.mu.Lock()
	send := s.send
	s.mu.Unlock()
	if send == nil || send(&audiodv1.Envelope{Body: &audiodv1.Envelope_SoloistSwitch{SoloistSwitch: &audiodv1.SoloistSwitch{}}}) != nil {
		s.log.Warn("soloist_switch_dropped", "reason", reason)
		return
	}
	s.log.Info("soloist_switch", "reason", reason)
}

// levelEnvelopes tell both senders a level: audiod keeps one per sender
func levelEnvelopes(level int) []*audiodv1.Envelope {
	out := make([]*audiodv1.Envelope, 0, 2)
	for _, sender := range []audiodv1.Sender{audiodv1.Sender_SENDER_SOLOIST, audiodv1.Sender_SENDER_AIRPLAY} {
		v := &audiodv1.SenderVolumeSet{Sender: sender, Level: uint32(level)} //nolint:gosec // the setter keeps 0-100
		out = append(out, &audiodv1.Envelope{Body: &audiodv1.Envelope_SenderVolumeSet{SenderVolumeSet: v}})
	}
	return out
}

// prober runs a build's --version in soloist's jail through audiod
type prober struct{ s *Soloist }

// Probe is ErrNoAudiod when no audiod ran it (down, gone or silent), io when
// the build did not run or answer in audiod's limit, and version when it does
// not say what it is.
func (p prober) Probe(ctx context.Context, path string) (updater.Build, error) {
	s := p.s
	s.mu.Lock()
	send := s.send
	if send == nil {
		s.mu.Unlock()
		return updater.Build{}, updater.ErrNoAudiod
	}
	s.probeSeq++
	id := "probe-" + strconv.FormatUint(s.probeSeq, 10)
	answer := make(chan *audiodv1.SoloistProbeResult, 1)
	s.probes[id] = answer
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.probes, id)
		s.mu.Unlock()
	}()
	req := &audiodv1.SoloistProbe{RequestId: id, Path: path}
	if send(&audiodv1.Envelope{Body: &audiodv1.Envelope_SoloistProbe{SoloistProbe: req}}) != nil {
		return updater.Build{}, updater.ErrNoAudiod
	}
	t := time.NewTimer(probeWait)
	defer t.Stop()
	select {
	case r := <-answer:
		switch {
		case r == nil:
			return updater.Build{}, updater.ErrNoAudiod
		case r.GetOk() && r.GetBuildEpoch() > 0:
			return updater.Build{Version: r.GetVersion(), Epoch: r.GetBuildEpoch()}, nil
		case r.GetOk() || r.GetCode() == string(updater.FailVersion):
			return updater.Build{}, updater.FailVersion
		default:
			return updater.Build{}, updater.FailIO
		}
	case <-t.C:
		return updater.Build{}, updater.ErrNoAudiod
	case <-ctx.Done():
		return updater.Build{}, updater.ErrNoAudiod
	}
}
