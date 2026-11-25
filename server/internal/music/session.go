package music

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// ours says whether the device's session plays the queue's song: a URL
// session whose title is the song's (the device may have cut it to its
// bound), or a screen's player showing the title its play answered
func ours(sess media.MediaSession, q *queue) bool {
	t, ok := q.current()
	want, source := t.Title, "url"
	if q.shown != "" {
		want, source = q.shown, "video"
	}
	return ok && sess.Source == source && sess.Metadata.Title != "" && strings.HasPrefix(want, sess.Metadata.Title)
}

// Owns says whether the device now plays a song of its queue.
func (s *Service) Owns(deviceID string) bool {
	sess, ok := s.d.Session(deviceID)
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.queues[deviceID]
	return ok && q != nil && ours(sess, q)
}

// CurrentTrack is the queue's song, with its provider identity and instance.
// It remains available just after playback ended so feedback can name it.
func (s *Service) CurrentTrack(deviceID string) (providers.Track, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.queues[deviceID]
	return q.current()
}

// Intercept takes a device's next and previous while it plays a song of its
// queue, which the device's URL session cannot skip itself: every caller
// (the agent, the dashboard, Home Assistant) skips through the queue. A stop
// ends the queue's auto-advance. handled false sends the command on.
func (s *Service) Intercept(ctx context.Context, deviceID, capability, name string, _ []byte) (json.RawMessage, bool, error) {
	if capability != "media.player" {
		return nil, false, nil
	}
	switch name {
	case "next", "previous":
		if !s.Owns(deviceID) {
			return nil, false, nil
		}
		dir := 1
		if name == "previous" {
			dir = -1
		}
		err := s.Skip(ctx, deviceID, dir)
		if errors.Is(err, ErrQueueEnd) && dir < 0 {
			// before the first song, previous starts it again
			err = s.Restart(ctx, deviceID)
		}
		return nil, true, commandError(err)
	case "stop":
		s.mu.Lock()
		if q := s.queues[deviceID]; q != nil {
			q.last = lastSeen{}
		}
		s.mu.Unlock()
	}
	return nil, false, nil
}

// Restart plays the queue's song again from its start.
func (s *Service) Restart(ctx context.Context, deviceID string) error {
	defer s.playLock(deviceID)()
	s.mu.Lock()
	q := s.queues[deviceID]
	s.mu.Unlock()
	if q == nil {
		return ErrNoQueue
	}
	return s.playCurrent(ctx, deviceID, q)
}

// Decorate adds what the queue knows to the device's session while it plays
// one of its songs: the skips the queue gives it, the song's provider and
// its page.
func (s *Service) Decorate(deviceID string, sess *media.MediaSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.queues[deviceID]
	if q == nil || !ours(*sess, q) {
		return
	}
	t, _ := q.current()
	for _, a := range []string{"next", "previous"} {
		if !slices.Contains(sess.Actions, a) {
			sess.Actions = append(sess.Actions, a)
		}
	}
	sess.Provider = ProviderName(t.Source)
	if identity, ok := providers.PublicSongIdentity(t); ok {
		sess.ProviderSongID = identity.SongID
		sess.WebURL = identity.WebURL
		sess.ProviderInstance = t.Instance
	}
}

// SessionChanged follows the device's session: while it plays the queue's
// song the queue notes where it is, and once that song ends by itself the
// next one plays (the same one with repeat one).
func (s *Service) SessionChanged(deviceID string) {
	sess, ok := s.d.Session(deviceID)
	now := s.d.Now()
	s.mu.Lock()
	q := s.queues[deviceID]
	if q == nil {
		s.mu.Unlock()
		return
	}
	if ok && ours(sess, q) {
		q.note(sess, now)
		s.mu.Unlock()
		return
	}
	if now.Sub(q.started) < settleWindow {
		s.mu.Unlock()
		return
	}
	ended := q.endedByItself(now)
	// a song that played to its end tells its station, before the next starts
	if ended {
		if cur, ok := q.current(); ok && q.radio {
			cur.Station = providers.RadioStation(cur)
			if cur.Station != "" {
				s.reportQueue(deviceID, providers.ListeningEvent{Track: cur, Kind: providers.ListeningTrackFinished,
					At: now, PlayedS: playedSeconds(q, now)})
			}
		}
	}
	q.last = lastSeen{}
	s.mu.Unlock()
	if !ended {
		return
	}
	go s.advance(deviceID)
}

func (s *Service) advance(deviceID string) {
	ctx, cancel := context.WithTimeout(context.Background(), advanceTimeout)
	defer cancel()
	start := s.d.Now()
	err := s.skip(ctx, deviceID, 1, true)
	code := "ok"
	switch {
	case errors.Is(err, ErrQueueEnd):
		code = "queue_end"
	case err != nil:
		code = errCode(err)
	}
	s.d.Log.Info("music_advance", "device_id", deviceID, "code", code, "ms", s.d.Now().Sub(start).Milliseconds())
}

// commandError is a skip's failure as a command answers it: the device's own
// errors as they are, the queue's as a device code
func commandError(err error) error {
	var device *commands.DeviceError
	if err == nil || errors.As(err, &device) || errors.Is(err, commands.ErrOffline) || errors.Is(err, commands.ErrTimeout) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &commands.DeviceError{Code: errCode(err)}
}

// errCode names an error for a log line: a code, never text from outside
func errCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNotSetUp):
		return "not_set_up"
	case errors.Is(err, ErrNotOnYouTube):
		return "not_on_youtube"
	case errors.Is(err, ErrNoQueue):
		return "no_queue"
	case errors.Is(err, ErrQueueEnd):
		return "queue_end"
	case errors.Is(err, providers.ErrUnplayable):
		return "unplayable"
	case errors.Is(err, providers.ErrSignInRequired):
		return "sign_in_required"
	}
	return "failed"
}

// ProviderName is a track source as people call it
func ProviderName(source string) string {
	switch source {
	case providers.SourceYouTube:
		return "YouTube Music"
	case providers.SourceSpotify:
		return "Spotify"
	case providers.SourceYandex:
		return "Yandex Music"
	case providers.SourceZvuk:
		return "Zvuk"
	case providers.SourceSoundCloud:
		return "SoundCloud"
	case providers.SourceVK:
		return "VK Music"
	case providers.SourceCadence:
		return "Cadence"
	}
	return ""
}

// Song is a queued song as the admin API and the agent see it.
type Song struct {
	Title          string `json:"title"`
	Artist         string `json:"artist,omitempty"`
	Album          string `json:"album,omitempty"`
	DurationS      int    `json:"durationS,omitempty"`
	ArtworkURL     string `json:"artworkUrl,omitempty"`
	Provider       string `json:"provider,omitempty" doc:"Where the song was found: YouTube Music, Spotify, Yandex Music, Zvuk, SoundCloud or VK Music."`
	ProviderSongID string `json:"providerSongId,omitempty" doc:"The song's public id at its provider, without an access key."`
	WebURL         string `json:"webUrl,omitempty" doc:"The song's public page, without query secrets or signed stream URLs."`
}

func song(t providers.Track) Song {
	out := Song{
		Title: t.Title, Artist: t.Artist, Album: t.Album, DurationS: t.DurationS, ArtworkURL: t.ArtworkURL,
		Provider: ProviderName(t.Source),
	}
	if identity, ok := providers.PublicSongIdentity(t); ok {
		out.ProviderSongID, out.WebURL = identity.SongID, identity.WebURL
	}
	return out
}

func songs(ts []providers.Track) []Song {
	out := make([]Song, len(ts))
	for i, t := range ts {
		out[i] = song(t)
	}
	return out
}

// View is a device's queue.
type View struct {
	Active   bool   `json:"active" doc:"Whether the device plays a song of the queue now."`
	Current  *Song  `json:"current,omitempty" doc:"The queue's song: playing when active, else the one a skip moves from."`
	Upcoming []Song `json:"upcoming" nullable:"false" doc:"The songs after it in play order, at most 50."`
	History  []Song `json:"history" nullable:"false" doc:"The songs before it, newest first, at most 20."`
	Shuffle  bool   `json:"shuffle"`
	Repeat   Repeat `json:"repeat" enum:"off,all,one"`
	Radio    bool   `json:"radio" doc:"Whether the queue tops itself up from radio at its end."`
}

// State is the device's queue; no queue answers its modes alone.
func (s *Service) State(deviceID string) View {
	owns := s.Owns(deviceID)
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.modesOf(deviceID)
	v := View{Upcoming: []Song{}, History: []Song{}, Shuffle: m.shuffle, Repeat: m.repeat}
	q := s.queues[deviceID]
	if q == nil {
		return v
	}
	if t, ok := q.current(); ok {
		cur := song(t)
		v.Current = &cur
	}
	v.Active, v.Radio = owns, q.radio
	v.Upcoming, v.History = songs(q.upcoming(50)), songs(q.history(20))
	return v
}

// note keeps where the queue's song is in the device's session; the caller holds s.mu
func (q *queue) note(sess media.MediaSession, now time.Time) {
	t, _ := q.current()
	at := now
	if ts, err := time.Parse(time.RFC3339Nano, sess.PositionAt); err == nil {
		at = ts
	}
	dur := sess.DurationMs
	if dur == 0 {
		dur = uint64(max(t.DurationS, 0)) * 1000 //nolint:gosec // max keeps it non-negative
	}
	q.last = lastSeen{ours: true, playing: sess.State == "playing", positionMs: sess.PositionMs, at: at, durationMs: dur}
}

// seed reads the device's session again once a play command answered: its first report can come before the answer
// (or before a new queue is kept), and without it a song that then plays to its end unreported never advances
func (s *Service) seed(deviceID string, q *queue) {
	sess, ok := s.d.Session(deviceID)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ours(sess, q) {
		q.note(sess, s.d.Now())
	}
}
