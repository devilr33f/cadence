package voice

import "context"

const (
	codeStopRequested = "stop_requested"
	// a session the server stopped (a screen's assistant.stop)
	codeStopped = "stopped"
)

// the device leaves these to the server, which tells the user's speech from a reply's own echo (M6a-D6); a language
// without a set of its own uses en's
var stopWords = map[string]map[string]bool{
	"ru": {"стоп": true, "хватит": true, "пауза": true, "остановись": true, "останови": true, "приостанови": true},
	"en": {"stop": true, "pause": true},
}

// isStopRequest: stop words alone ask for silence, not an answer
func isStopRequest(text, lang string) bool {
	set, ok := stopWords[lang]
	if !ok {
		set = stopWords["en"]
	}
	said := words(text)
	for _, w := range said {
		if !set[w] {
			return false
		}
	}
	return len(said) > 0
}

// stopRequested ends the turn without an answer (M6a-D6), pausing the
// device's media first when it plays, best effort within the pause bound:
// pausing never raises the volume, so a follow-up may do it too. The log never
// carries the words. An announcement that plays stops too.
func (s *session) stopRequested() string {
	s.stopped = true
	d := s.c.d
	paused := false
	if d.MediaPlaying != nil && d.PauseMedia != nil && d.MediaPlaying(s.dev) {
		ctx, cancel := context.WithTimeout(s.ctx, s.c.lim.pause)
		paused = d.PauseMedia(ctx, s.dev) == nil
		cancel()
	}
	announced := s.c.stopAnnouncement(s.dev)
	d.Log.Info("stop_requested", "device_id", s.dev, "session_id", s.id, "paused", paused, "announcement_stopped", announced)
	return codeStopRequested
}
