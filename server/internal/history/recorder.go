// Package history records what played on each device for the owner's 14-day
// history. It writes no titles or urls to logs, and never stores playback urls.
package history

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

const (
	repeatWindow = 30 * time.Second
	writeTimeout = 5 * time.Second
)

// Attribution is what a play call knows beyond the device's report. Empty
// fields leave the report's own metadata in place.
type Attribution struct {
	TurnID, ItemID          string
	Source, Provider, Kind  string
	Title, Artist, Album    string
	ArtworkURL, ArtworkHash string
}

// Service follows the one media-session lifecycle and owns the history store.
type Service struct {
	q   *db.Queries
	log *slog.Logger
	now func() time.Time

	mu         sync.Mutex
	active     map[string]*activePlay
	attributes map[string]map[string]Attribution
}

type activePlay struct {
	id        int64
	sessionID string
	identity  itemIdentity
	playedMS  int64
	resumedAt time.Time
}

type itemIdentity struct {
	source, title, artist, catalogID string
	songID                           string
	series, episode, episodeName     string
	season                           uint32
}

func New(q *db.Queries, log *slog.Logger, now func() time.Time) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if now == nil {
		now = time.Now
	}
	return &Service{q: q, log: log, now: now, active: map[string]*activePlay{}, attributes: map[string]map[string]Attribution{}}
}

// Attribute adds an assistant turn or catalog item to a session before its
// first report. AttributeObserved also covers a late pending-play follower.
func (s *Service) Attribute(deviceID, sessionID string, a Attribution) {
	if deviceID == "" || sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	bySession := s.attributes[deviceID]
	if bySession == nil {
		bySession = map[string]Attribution{}
		s.attributes[deviceID] = bySession
	}
	bySession[sessionID] = a
}

// AttributeObserved is Attribute for a play follower that discovered its
// session from this report; it also fills a row already started.
func (s *Service) AttributeObserved(deviceID, sessionID string, sess media.MediaSession, a Attribution) {
	s.Attribute(deviceID, sessionID, a)
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.active[deviceID]
	if current == nil || current.sessionID != sessionID {
		return
	}
	entry, _ := historyHistoryEntry(deviceID, sess, a)
	identity := identityOf(entry, sess)
	if identityChanged(current.identity, identity) {
		now := s.now().UTC()
		s.finish(deviceID, current, now)
		delete(s.active, deviceID)
		if sess.State == "playing" {
			s.startLocked(deviceID, sessionID, entry, identity, now)
		}
		return
	}
	current.identity = mergeIdentity(current.identity, identity)
	if err := s.annotate(context.Background(), current.id, entry); err != nil {
		s.log.Error("history_write_failed", "op", "annotate", "device_id", deviceID)
	}
}

// Observe records playing time from every session replacement, pause, idle and
// disconnect. A title or url is only ever a query parameter, never a log field.
// memberSource is a multiroom member's session source
const memberSource = "group"

func (s *Service) Observe(deviceID string, previous, current *media.MediaSession) {
	if current != nil && current.Source == memberSource {
		// a multiroom member plays its leader's stream: the leader's row already says what played
		current = nil
	}
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.active[deviceID]
	var entry HistoryEntry
	var identity itemIdentity
	if current != nil {
		entry = s.entry(deviceID, current)
		identity = identityOf(entry, *current)
	}
	if a != nil && (current == nil || current.SessionID != a.sessionID || identityChanged(a.identity, identity)) {
		s.finish(deviceID, a, now)
		delete(s.active, deviceID)
		a = nil
	}
	if current == nil {
		return
	}
	if a == nil {
		if current.State != "playing" {
			return
		}
		s.startLocked(deviceID, current.SessionID, entry, identity, now)
		return
	}
	a.identity = mergeIdentity(a.identity, identity)
	switch current.State {
	case "playing":
		if a.resumedAt.IsZero() {
			a.resumedAt = now
		}
	case "paused", "buffering":
		if !a.resumedAt.IsZero() {
			a.playedMS = playedThrough(a, now)
			a.resumedAt = time.Time{}
			ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			defer cancel()
			if err := s.q.CheckpointPlayHistory(ctx, db.CheckpointPlayHistoryParams{ID: a.id, PlayedMs: a.playedMS}); err != nil {
				s.log.Error("history_write_failed", "op", "checkpoint", "device_id", deviceID)
			}
		}
	default:
		s.finish(deviceID, a, now)
		delete(s.active, deviceID)
	}
	_ = previous // active state already carries the intervals that were playing
}

func (s *Service) startLocked(deviceID, sessionID string, entry HistoryEntry, identity itemIdentity, now time.Time) {
	row, err := s.start(context.Background(), deviceID, entry, now)
	if err != nil {
		s.log.Error("history_write_failed", "op", "start", "device_id", deviceID)
		return
	}
	s.active[deviceID] = &activePlay{id: row.ID, sessionID: sessionID, identity: identity,
		playedMS: row.PlayedMs, resumedAt: now}
	s.forgetOtherAttributes(deviceID, sessionID)
}

func (s *Service) start(ctx context.Context, deviceID string, e HistoryEntry, now time.Time) (db.StartPlayHistoryRow, error) {
	dev, err := parseUUID(deviceID)
	if err != nil {
		return db.StartPlayHistoryRow{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return s.q.StartPlayHistory(ctx, db.StartPlayHistoryParams{
		DeviceID: dev, DedupeKey: e.DedupeKey, RecentAfter: stamp(now.Add(-repeatWindow)), TurnID: e.TurnID,
		Source: e.Source, Provider: e.Provider, Kind: e.Kind, Title: e.Title, Artist: e.Artist, Album: e.Album,
		ArtworkUrl: e.ArtworkURL, ArtworkHash: e.ArtworkHash, CatalogID: e.CatalogID, StartedAt: stamp(now),
		ProviderSongID: e.ProviderSongID, WebUrl: e.WebURL, ProviderInstance: e.ProviderInstance,
	})
}

func (s *Service) annotate(ctx context.Context, id int64, e HistoryEntry) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return s.q.AnnotatePlayHistory(ctx, db.AnnotatePlayHistoryParams{
		ID: id, Source: e.Source, Provider: e.Provider, Kind: e.Kind, Title: e.Title, Artist: e.Artist, Album: e.Album,
		ArtworkUrl: e.ArtworkURL, ArtworkHash: e.ArtworkHash, CatalogID: e.CatalogID, TurnID: e.TurnID,
		DedupeKey: e.DedupeKey, ProviderSongID: e.ProviderSongID, WebUrl: e.WebURL,
		ProviderInstance: e.ProviderInstance,
	})
}

func (s *Service) finish(deviceID string, a *activePlay, now time.Time) {
	a.playedMS = playedThrough(a, now)
	a.resumedAt = time.Time{}
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	if err := s.q.FinishPlayHistory(ctx, db.FinishPlayHistoryParams{ID: a.id, EndedAt: stamp(now), PlayedMs: a.playedMS}); err != nil {
		s.log.Error("history_write_failed", "op", "finish", "device_id", deviceID)
	}
}

func playedThrough(a *activePlay, now time.Time) int64 {
	if a.resumedAt.IsZero() {
		return a.playedMS
	}
	return a.playedMS + max(now.Sub(a.resumedAt).Milliseconds(), 0)
}

func (s *Service) entry(deviceID string, sess *media.MediaSession) HistoryEntry {
	if sess == nil {
		return HistoryEntry{}
	}
	entry, _ := historyHistoryEntry(deviceID, *sess, s.attributes[deviceID][sess.SessionID])
	return entry
}

func identityOf(e HistoryEntry, sess media.MediaSession) itemIdentity {
	return itemIdentity{source: e.Source, title: e.Title, artist: e.Artist, catalogID: e.CatalogID,
		songID: e.ProviderSongID, series: sess.Metadata.Series, episode: sess.Metadata.Episode,
		episodeName: sess.Metadata.EpisodeName, season: sess.Metadata.Season}
}

// identityChanged ignores fields that arrive late or disappear from a pause
// report, but catches a new track or episode within one long-lived cast session.
func identityChanged(before, current itemIdentity) bool {
	return before.catalogID != "" && current.catalogID != "" && before.catalogID != current.catalogID ||
		before.songID != "" && current.songID != "" && before.songID != current.songID ||
		before.title != "" && current.title != "" && before.title != current.title ||
		before.artist != "" && current.artist != "" && before.artist != current.artist ||
		before.episode != "" && current.episode != "" && before.episode != current.episode ||
		before.episodeName != "" && current.episodeName != "" && before.episodeName != current.episodeName
}

func mergeIdentity(before, current itemIdentity) itemIdentity {
	if current.source == "" {
		current.source = before.source
	}
	if current.title == "" {
		current.title = before.title
	}
	if current.artist == "" {
		current.artist = before.artist
	}
	if current.catalogID == "" {
		current.catalogID = before.catalogID
	}
	if current.songID == "" {
		current.songID = before.songID
	}
	if current.series == "" {
		current.series = before.series
	}
	if current.episode == "" {
		current.episode = before.episode
	}
	if current.episodeName == "" {
		current.episodeName = before.episodeName
	}
	if current.season == 0 {
		current.season = before.season
	}
	return current
}

func (s *Service) forgetOtherAttributes(deviceID, keep string) {
	for id := range s.attributes[deviceID] {
		if id != keep {
			delete(s.attributes[deviceID], id)
		}
	}
}

func historyHistoryEntry(deviceID string, sess media.MediaSession, a Attribution) (HistoryEntry, string) {
	e := HistoryEntry{
		DeviceID: deviceID, Source: sess.Source, Provider: sess.Provider, Title: sess.Metadata.Title,
		Artist: sess.Metadata.Artist, Album: sess.Metadata.Album, ArtworkURL: sess.Metadata.ArtworkURL,
	}
	if e.Source == "url" && e.Provider != "" {
		e.Source = "music"
	}
	switch strings.ToLower(e.Provider) {
	case "youtube music", "youtube", "ytmusic":
		if e.Source == "music" {
			e.Provider = "ytmusic"
		}
	case "spotify":
		e.Provider = "spotify"
	}
	if e.Source == "soloist" {
		e.Provider = "spotify"
	}
	if e.Source == "video" {
		e.Provider = sess.Metadata.Provider
		if p := strings.ToLower(sess.Metadata.Provider); p == "torrplayer" || p == "torrserver" {
			e.Source = p
		}
	}
	if a.Source != "" {
		e.Source = a.Source
	}
	if a.Provider != "" {
		e.Provider = a.Provider
	}
	if e.Source == "music" {
		switch strings.ToLower(e.Provider) {
		case "youtube", "youtube music", "ytmusic":
			e.Provider = "ytmusic"
		case "spotify":
			e.Provider = "spotify"
		case "yandex", "yandex music":
			e.Provider = "yandex"
		case "zvuk":
			e.Provider = "zvuk"
		case "soundcloud":
			e.Provider = "soundcloud"
		case "vk", "vk music":
			e.Provider = "vk"
		}
	}
	if a.Title != "" && e.Title == "" {
		e.Title = a.Title
	}
	// the queue's decoration carries the song's canonical id and instance; the
	// page is rebuilt from them, never stored as the session sent it
	if e.Source == "music" && sess.ProviderSongID != "" {
		e.ProviderSongID = bounded(sess.ProviderSongID, 128)
		e.ProviderInstance = bounded(sess.ProviderInstance, 64)
		e.WebURL = providers.PublicSongWebURL(e.Provider, e.ProviderSongID, sess.WebURL)
	}
	if a.Artist != "" && e.Artist == "" {
		e.Artist = a.Artist
	}
	if a.Album != "" {
		e.Album = a.Album
	}
	if a.ArtworkURL != "" && e.ArtworkURL == "" {
		e.ArtworkURL = a.ArtworkURL
	}
	e.Kind = historyKind(sess.Metadata.Kind, e.Source)
	if a.Kind != "" {
		e.Kind = a.Kind
	}
	e.CatalogID, e.TurnID = a.ItemID, a.TurnID
	// a sender's cover is the device's upload, never a url
	if e.Source == "airplay" || e.Source == "bluetooth" {
		e.ArtworkURL = ""
		e.ArtworkHash = sess.Metadata.ArtworkHash
		if a.ArtworkHash != "" {
			e.ArtworkHash = a.ArtworkHash
		}
	} else {
		e.ArtworkURL = publicArtworkURL(e.ArtworkURL)
		e.ArtworkHash = ""
	}
	e.Source, e.Provider, e.Kind = bounded(e.Source, 64), bounded(e.Provider, 255), bounded(e.Kind, 16)
	e.Title, e.Artist, e.Album = bounded(e.Title, 255), bounded(e.Artist, 255), bounded(e.Album, 255)
	e.CatalogID, e.TurnID = bounded(e.CatalogID, 256), bounded(e.TurnID, 256)
	// a provider song id can arrive late, so it is not part of the repeat key
	identity := []string{e.Source, e.Provider, e.Kind, e.Title, e.Artist, e.Album, e.CatalogID,
		sess.Metadata.Series, strconv.FormatUint(uint64(sess.Metadata.Season), 10), sess.Metadata.Episode, sess.Metadata.EpisodeName}
	raw, _ := json.Marshal(identity)
	hash := sha256.Sum256(raw)
	e.DedupeKey = hex.EncodeToString(hash[:])
	return e, e.DedupeKey
}

func historyKind(kind, source string) string {
	switch kind {
	case "track":
		return "track"
	case "episode", "podcast":
		return "episode"
	case "movie", "video":
		return "video"
	case "live", "channel":
		return "stream"
	}
	switch source {
	case "music", "soloist", "airplay", "bluetooth":
		return "track"
	case "video", "torrplayer", "torrserver":
		return "video"
	}
	return "stream"
}

// publicArtworkURL is deliberately conservative: a query can carry a signed
// thumbnail just as it can carry a signed stream, so neither is retained.
func publicArtworkURL(raw string) string {
	if len(raw) == 0 || len(raw) > 2047 || !utf8.ValidString(raw) {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	return u.String()
}

func bounded(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func stamp(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t.UTC(), Valid: true} }
