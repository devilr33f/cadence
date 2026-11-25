package music

import (
	"context"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// feedbackQueueLen bounds the listening events waiting for their provider
const feedbackQueueLen = 100

// reportQueue queues one station event; it never blocks a play or a skip
func (s *Service) reportQueue(deviceID string, ev providers.ListeningEvent) {
	track := ev.Track
	if track.Instance == "" || track.Station == "" || s.d.Music == nil {
		return
	}
	s.fbMu.Lock()
	defer s.fbMu.Unlock()
	if s.fb == nil {
		s.fb = make(chan providers.ListeningEvent, feedbackQueueLen)
		go s.runFeedback()
	}
	select {
	case s.fb <- ev:
	default:
		s.d.Log.Warn("music_feedback_dropped", "device_id", deviceID)
	}
}

// runFeedback sends the queued events in order, one at a time, so a station
// hears a song's end before the next one's start
func (s *Service) runFeedback() {
	for ev := range s.fb {
		ctx, cancel := context.WithTimeout(context.Background(), advanceTimeout)
		m, err := s.d.Music(ctx, ev.Track.Instance)
		if err == nil {
			if r, ok := m.(providers.ListeningReporter); ok {
				if err = r.ReportListening(ctx, ev); err != nil {
					s.d.Log.Warn("music_feedback_failed", "code", errCode(err))
				}
			}
		}
		cancel()
	}
}

// StopFeedback ends the reporter's worker; no event queued, none was started
func (s *Service) StopFeedback() {
	s.fbMu.Lock()
	defer s.fbMu.Unlock()
	if s.fb != nil {
		close(s.fb)
		s.fb = nil
	}
}

// displaced is the old queue song's skip. It stays pending until a replacement
// actually plays, so a failed replacement reports nothing.
func (s *Service) displaced(deviceID string) *providers.ListeningEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.queues[deviceID]
	cur, ok := q.current()
	if !ok || !q.radio || !q.last.ours {
		return nil
	}
	cur.Station = providers.RadioStation(cur)
	if cur.Station == "" {
		return nil
	}
	at := s.d.Now()
	return &providers.ListeningEvent{Track: cur, Kind: providers.ListeningTrackSkipped,
		At: at, PlayedS: playedSeconds(q, at)}
}

// playedSeconds is where the queue's song had got to at now, from the device's
// last report of it
func playedSeconds(q *queue, now time.Time) float64 {
	l := q.last
	if !l.ours {
		return 0
	}
	pos := l.positionMs
	if l.playing {
		pos += uint64(max(now.Sub(l.at).Milliseconds(), 0))
	}
	if l.durationMs > 0 && pos > l.durationMs {
		pos = l.durationMs
	}
	return float64(pos) / 1000
}
