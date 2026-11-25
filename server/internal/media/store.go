// Package media keeps each device's latest media session in memory (D11): its
// metadata goes to admin callers and the live feed, and the cover art it names
// is fetched into memory too; none reaches a log. The separate play-history
// recorder stores its bounded 14-day copy without stream or signed urls.
package media

import (
	"log/slog"
	"sync"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/events"
)

type Store struct {
	bus *events.Bus
	log *slog.Logger

	mu       sync.Mutex
	sessions map[string]MediaSession // by device; absent: none

	// decorate adds what the server's music queue knows to a session it
	// plays; changed hears of every change. observe hears the lifecycle with
	// the decorated previous and current sessions. All are set before links open.
	decorate func(deviceID string, s *MediaSession)
	changed  func(deviceID string)
	observe  func(deviceID string, previous, current *MediaSession)
	// lead answers the stream source of the group deviceID follows, followers the members that follow deviceID
	// (multiroom): a member's group session shows its source's cover
	lead      func(deviceID string) (string, bool)
	followers func(deviceID string) []string
}

// ShareGroups has a group member's session (source group) take its stream source's cover and the metadata it lacks,
// and a source's change reach its members' feed (multiroom).
func (s *Store) ShareGroups(lead func(deviceID string) (string, bool), followers func(deviceID string) []string) {
	s.lead, s.followers = lead, followers
}

// Decorate sets what adds the music queue's view to a session it plays.
func (s *Store) Decorate(f func(deviceID string, s *MediaSession)) { s.decorate = f }

// OnChange sets what hears of every change of a device's session, after it.
func (s *Store) OnChange(f func(deviceID string)) { s.changed = f }

// OnSession sets the lifecycle observer. Nil current means the session ended.
func (s *Store) OnSession(f func(deviceID string, previous, current *MediaSession)) { s.observe = f }

func NewStore(bus *events.Bus, log *slog.Logger) *Store {
	return &Store{bus: bus, log: log, sessions: map[string]MediaSession{}}
}

// Handle stores a MediaSessionState and reports whether env was one. An idle
// state without a session id means the device has none.
func (s *Store) Handle(deviceID string, env *cadencev2.Envelope) bool {
	m := env.GetMediaSessionState()
	if m == nil {
		return false
	}
	v := view(m)
	if v.State == stateIdle && v.SessionID == "" {
		s.clear(deviceID)
		return true
	}
	s.mu.Lock()
	previous, had := s.sessions[deviceID]
	s.sessions[deviceID] = v
	s.mu.Unlock()
	decorated := s.decorated(deviceID, v)
	if s.observe != nil {
		var before *MediaSession
		if had {
			v := s.decorated(deviceID, previous)
			before = &v
		}
		s.observe(deviceID, before, &decorated)
	}
	s.log.Info("media_state", "device_id", deviceID, "state", v.State, "source", v.Source)
	if s.changed != nil {
		s.changed(deviceID)
	}
	s.publish(deviceID, decorated)
	s.publishFollowers(deviceID)
	return true
}

func (s *Store) decorated(deviceID string, v MediaSession) MediaSession {
	if s.decorate != nil {
		v.Actions = append([]string(nil), v.Actions...)
		s.decorate(deviceID, &v)
	}
	s.share(deviceID, &v)
	return v
}

// share fills a group member's session with what its stream source's plays and the member's lacks: the cover (its
// url and hash, and the device that uploaded it) and the item's details, while both name the same title and artist
// (the member's state may run ahead of or behind the source's)
func (s *Store) share(deviceID string, v *MediaSession) {
	if v.Source != "group" || s.lead == nil {
		return
	}
	leader, ok := s.lead(deviceID)
	if !ok {
		return
	}
	s.mu.Lock()
	src, ok := s.sessions[leader]
	s.mu.Unlock()
	if !ok || src.Metadata.Title != v.Metadata.Title || src.Metadata.Artist != v.Metadata.Artist {
		return
	}
	md, from := &v.Metadata, src.Metadata
	if md.ArtworkHash == "" && from.ArtworkHash != "" {
		md.ArtworkURL, md.ArtworkHash = from.ArtworkURL, from.ArtworkHash
		v.ArtworkDevice = leader
	}
	fill := func(dst *string, src string) {
		if *dst == "" {
			*dst = src
		}
	}
	fill(&md.Album, from.Album)
	fill(&md.Kind, from.Kind)
	fill(&md.Series, from.Series)
	fill(&md.Episode, from.Episode)
	fill(&md.EpisodeName, from.EpisodeName)
	fill(&md.Provider, from.Provider)
	fill(&md.ProviderSource, from.ProviderSource)
	if md.Season == 0 {
		md.Season = from.Season
	}
	if v.DurationMs == 0 {
		v.DurationMs = src.DurationMs
	}
}

// publishFollowers sends the feed the sessions of the members that follow deviceID again: what they share of its
// session changed with it
func (s *Store) publishFollowers(deviceID string) {
	if s.followers == nil {
		return
	}
	for _, f := range s.followers(deviceID) {
		s.mu.Lock()
		v, ok := s.sessions[f]
		s.mu.Unlock()
		if ok && v.Source == "group" {
			s.publish(f, s.decorated(f, v))
		}
	}
}

// Closed forgets the device's session: its link ended, or a new one began.
func (s *Store) Closed(deviceID string) { s.clear(deviceID) }

// Get returns the device's session, false for none.
func (s *Store) Get(deviceID string) (MediaSession, bool) {
	s.mu.Lock()
	v, ok := s.sessions[deviceID]
	s.mu.Unlock()
	if !ok {
		return v, false
	}
	return s.decorated(deviceID, v), true
}

// Raw returns the device's session as the device sent it, false for none.
func (s *Store) Raw(deviceID string) (MediaSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.sessions[deviceID]
	return v, ok
}

func (s *Store) clear(deviceID string) {
	s.mu.Lock()
	previous, had := s.sessions[deviceID]
	delete(s.sessions, deviceID)
	s.mu.Unlock()
	if had {
		if s.observe != nil {
			before := s.decorated(deviceID, previous)
			s.observe(deviceID, &before, nil)
		}
		s.log.Info("media_state", "device_id", deviceID, "state", stateIdle)
		if s.changed != nil {
			s.changed(deviceID)
		}
		s.publish(deviceID, none)
		s.publishFollowers(deviceID)
	}
}

func (s *Store) publish(deviceID string, data any) {
	if s.bus != nil {
		s.bus.Publish(events.Event{Type: events.MediaState, DeviceID: deviceID, At: time.Now(), Data: data})
	}
}
