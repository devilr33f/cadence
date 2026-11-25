// Package music plays songs on a device and keeps its queue: the songs a play
// started, next and previous, shuffle and repeat, radio at the queue's end,
// and the next song once one ends by itself. It turns a song into a stream
// with the device's player (yt-dlp) and sends it as a media.url session.
// Queues live in memory only (D11); lines carry ids, codes and timings, never
// a title, a query or a URL.
package music

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/playprogress"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

const (
	// the device's media.url bounds (device/internal/media/url.go)
	maxDeviceURL = 2047
	// songs the radio adds at a time
	radioBatch = 25
	// a song that will not play is skipped, at most this many in a row
	maxSkips = 3
	// changes of the device's session this soon after a play command are
	// that play's own
	settleWindow   = 5 * time.Second
	advanceTimeout = 90 * time.Second
)

var (
	ErrNotSetUp     = errors.New("music: no player set up")
	ErrNotOnYouTube = errors.New("music: not on youtube music")
	ErrNoQueue      = errors.New("music: no queue")
	ErrQueueEnd     = errors.New("music: end of the queue")
)

// YouTube is YouTube Music's own search and radio (ytmusic.Client)
type YouTube interface {
	providers.Music
	Radio(ctx context.Context, videoID string, limit int) ([]providers.Track, error)
}

type Deps struct {
	Settings func(ctx context.Context, deviceID string) (settings.Values, error)
	// Player builds the instance the musicPlayer setting names
	Player  func(ctx context.Context, name string) (providers.Player, error)
	YouTube YouTube
	// Music builds a music provider instance: a song's own (Track.Instance),
	// which streams it and its radio when it can; nil plays every song from
	// YouTube Music
	Music func(ctx context.Context, name string) (providers.Music, error)
	// Stream answers the url the device fetches a stream by; nil sends the
	// upstream url itself
	Stream func(deviceID string, upstream providers.Stream) (string, error)
	// Image answers an artwork url through the server's image proxy; nil leaves it as it is
	Image func(rawURL string) string
	// Command sends a capability command to the device, past Intercept
	Command func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error)
	// Session is the device's media session as the server last saw it
	Session func(deviceID string) (media.MediaSession, bool)
	Log     *slog.Logger
	Now     func() time.Time
}

type Service struct {
	d Deps

	mu     sync.Mutex
	queues map[string]*queue
	modes  map[string]modes
	// fbMu guards the listening feedback queue and its worker
	fbMu sync.Mutex
	fb   chan providers.ListeningEvent
	// plays serializes a device's plays: a resolve takes seconds
	plays map[string]*sync.Mutex
	// changed hears that a device's queue changed without its session changing (SessionFields to send again)
	changed func(deviceID string)
	// screen plays a song on a screen's own player, for a device viaScreen says is a screen (UseScreens)
	screen    ScreenPlay
	viaScreen func(ctx context.Context, deviceID string) bool
}

// ScreenPlay plays a song on a screen's own player (mpv, through the video service's screen path) and answers the
// title the screen's session shows for it.
type ScreenPlay func(ctx context.Context, deviceID string, t providers.Track) (string, error)

// UseScreens plays songs on a screen through play when viaScreen says the device is one (a screen's audiod has no
// url source); set before the links open.
func (s *Service) UseScreens(play ScreenPlay, viaScreen func(ctx context.Context, deviceID string) bool) {
	s.screen, s.viaScreen = play, viaScreen
}

type modes struct {
	shuffle bool
	repeat  Repeat
}

func New(d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d, queues: map[string]*queue{}, modes: map[string]modes{}, plays: map[string]*sync.Mutex{}}
}

// OnFieldsChange has fn hear each change of a queue that its session's state does not show (songs added, moved or
// removed, a shuffle, a forgotten queue); set it before the links open. fn must not block.
func (s *Service) OnFieldsChange(fn func(deviceID string)) { s.changed = fn }

func (s *Service) fieldsChanged(deviceID string) {
	if s.changed != nil {
		s.changed(deviceID)
	}
}

func (s *Service) playLock(deviceID string) func() {
	s.mu.Lock()
	m := s.plays[deviceID]
	if m == nil {
		m = &sync.Mutex{}
		s.plays[deviceID] = m
	}
	s.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// Options shape a new queue.
type Options struct {
	// Radio tops the queue up from the last song's radio at its end
	Radio bool
	// Name and ArtworkURL are the playlist's, album's or artist's, when the queue started from one
	Name       string
	ArtworkURL string
}

// Play replaces the device's queue with tracks and plays tracks[start]. A
// song that will not play is skipped for the next, up to 3; the error is the
// last one's.
func (s *Service) Play(ctx context.Context, deviceID string, tracks []providers.Track, start int, o Options) error {
	if len(tracks) == 0 {
		return ErrNoQueue
	}
	defer s.playLock(deviceID)()
	return s.playNew(ctx, deviceID, tracks, start, o)
}

// playNew plays a new queue and keeps it once a song of it plays; the
// caller holds the device's play lock
func (s *Service) playNew(ctx context.Context, deviceID string, tracks []providers.Track, start int, o Options) error {
	s.mu.Lock()
	m := s.modesOf(deviceID)
	s.mu.Unlock()
	q := newQueue(tracks, start, m.shuffle, m.repeat, o)
	q.previous = s.displaced(deviceID)
	if err := s.playSkipping(ctx, deviceID, q, 1); err != nil {
		return err
	}
	s.mu.Lock()
	s.queues[deviceID] = q
	s.mu.Unlock()
	return nil
}

// Add puts tracks after the playing song (next) or at the queue's end, and
// answers how many fit; without a queue it plays them as a new one.
func (s *Service) Add(ctx context.Context, deviceID string, tracks []providers.Track, next bool) (int, error) {
	s.mu.Lock()
	q := s.queues[deviceID]
	if q != nil {
		n := q.add(tracks, next)
		s.mu.Unlock()
		s.fieldsChanged(deviceID)
		return n, nil
	}
	s.mu.Unlock()
	if err := s.Play(ctx, deviceID, tracks, 0, Options{}); err != nil {
		return 0, err
	}
	return min(len(tracks), MaxQueue), nil
}

// Skip plays the next (+1) or previous (-1) song of the device's queue.
// ErrQueueEnd at an end with nothing to wrap to or top up from.
func (s *Service) Skip(ctx context.Context, deviceID string, dir int) error {
	return s.skip(ctx, deviceID, dir, false)
}

func (s *Service) skip(ctx context.Context, deviceID string, dir int, auto bool) error {
	defer s.playLock(deviceID)()
	s.mu.Lock()
	q := s.queues[deviceID]
	s.mu.Unlock()
	if q == nil {
		return ErrNoQueue
	}
	if auto && q.repeat == RepeatOne {
		return s.playSkipping(ctx, deviceID, q, 1)
	}
	// a skip the owner asked for tells the song's station it was left
	var skipped providers.Track
	var played float64
	if !auto {
		s.mu.Lock()
		if cur, ok := q.current(); ok && q.radio {
			cur.Station = providers.RadioStation(cur)
			if cur.Station != "" {
				skipped, played = cur, playedSeconds(q, s.d.Now())
			}
		}
		s.mu.Unlock()
	}
	if !s.step(ctx, q, dir) {
		return ErrQueueEnd
	}
	if skipped.ID != "" {
		s.reportQueue(deviceID, providers.ListeningEvent{Track: skipped, Kind: providers.ListeningTrackSkipped,
			At: s.d.Now(), PlayedS: played})
	}
	return s.playSkipping(ctx, deviceID, q, dir)
}

// step moves the queue, topping it up from radio at its end
func (s *Service) step(ctx context.Context, q *queue, dir int) bool {
	s.mu.Lock()
	moved := q.step(dir)
	cur, _ := q.current()
	topUp := !moved && dir > 0 && q.radio && (cur.VideoID != "" || cur.Instance != "")
	s.mu.Unlock()
	if moved || !topUp {
		return moved
	}
	more := s.radioOf(ctx, cur)
	if len(more) == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if q.add(unseen(q.items, more), false) == 0 {
		return false
	}
	return q.step(dir)
}

// unseen drops tracks the queue already holds: the same YouTube video, or
// the same song of the same provider
func unseen(have, more []providers.Track) []providers.Track {
	seen := map[string]bool{}
	for _, t := range have {
		seen[songKey(t)] = true
	}
	var out []providers.Track
	for _, t := range more {
		if k := songKey(t); !seen[k] {
			seen[k] = true
			out = append(out, t)
		}
	}
	return out
}

func songKey(t providers.Track) string {
	if t.VideoID != "" {
		return "yt:" + t.VideoID
	}
	return t.Source + ":" + t.ID
}

// playSkipping plays the queue's song, stepping in dir past ones that will
// not play
func (s *Service) playSkipping(ctx context.Context, deviceID string, q *queue, dir int) error {
	var err error
	for range maxSkips {
		if err = s.playCurrent(ctx, deviceID, q); err == nil || !skippable(err) {
			return err
		}
		if !s.step(ctx, q, dir) {
			return err
		}
	}
	return err
}

func skippable(err error) bool {
	return errors.Is(err, providers.ErrUnplayable) || errors.Is(err, providers.ErrSignInRequired) || errors.Is(err, ErrNotOnYouTube)
}

// onYouTube gives a song another catalog found its YouTube Music video
func (s *Service) onYouTube(ctx context.Context, t providers.Track) (providers.Track, error) {
	if t.VideoID != "" {
		return t, nil
	}
	if s.d.YouTube == nil {
		return t, ErrNotOnYouTube
	}
	id, found, err := matchOnYouTube(ctx, s.d.YouTube, t)
	if err != nil {
		return t, err
	}
	if !found {
		return t, ErrNotOnYouTube
	}
	t.VideoID = id
	return t, nil
}

func (s *Service) playCurrent(ctx context.Context, deviceID string, q *queue) error {
	s.mu.Lock()
	t, ok := q.current()
	s.mu.Unlock()
	if !ok {
		return ErrNoQueue
	}
	// a play's card names the song it fetches now: a list's first, or the one after a skip
	playprogress.ReportItem(ctx, playprogress.Item{Title: t.Title, Artist: t.Artist, ArtworkURL: t.ArtworkURL})
	v, err := s.d.Settings(ctx, deviceID)
	if err != nil {
		return err
	}
	onScreen := s.screen != nil && s.viaScreen != nil && s.viaScreen(ctx, deviceID)
	// a screen plays songs on its own player, which takes a YouTube video alone
	stream, native, nativeErr := providers.Stream{}, false, error(nil)
	if t.VideoID == "" && !onScreen {
		stream, native, nativeErr = s.streamNative(ctx, v, t)
	}
	if !native {
		if err := s.playYouTube(ctx, deviceID, v, q, &t, onScreen, &stream); err != nil {
			// the song's own provider said more than "not on YouTube" or "no player"
			if nativeErr != nil && (errors.Is(err, ErrNotSetUp) || errors.Is(err, ErrNotOnYouTube)) {
				return nativeErr
			}
			return err
		}
		if onScreen {
			return nil
		}
	}
	if s.d.Stream != nil {
		if stream.URL, err = s.d.Stream(deviceID, stream); err != nil {
			return errors.New(providers.CodePlayerFailed)
		}
	}
	if len(stream.URL) > maxDeviceURL {
		return providers.ErrUnplayable
	}
	raw, err := json.Marshal(playCommand(t, stream))
	if err != nil {
		return err
	}
	if _, err := s.d.Command(ctx, deviceID, "media.url", "play", raw); err != nil {
		return err
	}
	s.mu.Lock()
	q.started, q.last, q.shown = s.d.Now(), lastSeen{}, ""
	s.mu.Unlock()
	s.songStarted(deviceID, q, t)
	s.seed(deviceID, q)
	return nil
}

// songStarted tells the song's station it began, the station itself once
func (s *Service) songStarted(deviceID string, q *queue, t providers.Track) {
	s.mu.Lock()
	previous := q.previous
	q.previous = nil
	at := s.d.Now()
	first := q.radio && !q.radioStarted
	q.radioStarted = true
	s.mu.Unlock()
	if previous != nil {
		s.reportQueue(deviceID, *previous)
	}
	t.Station = providers.RadioStation(t)
	if t.Station == "" || !q.radio {
		return
	}
	if first {
		s.reportQueue(deviceID, providers.ListeningEvent{Track: t, Kind: providers.ListeningRadioStarted, At: at})
	}
	s.reportQueue(deviceID, providers.ListeningEvent{Track: t, Kind: providers.ListeningTrackStarted, At: at})
}

// streamNative streams a song from the provider instance that found it, when
// that instance streams its own songs and is still set up. native is false
// when the song should play from YouTube Music instead; err is then why its
// own provider would not play it, if it said.
func (s *Service) streamNative(ctx context.Context, v settings.Values, t providers.Track) (providers.Stream, bool, error) {
	if t.Instance == "" || s.d.Music == nil || !slices.Contains(v.Music, t.Instance) {
		return providers.Stream{}, false, nil
	}
	m, err := s.d.Music(ctx, t.Instance)
	if err != nil {
		return providers.Stream{}, false, nil
	}
	st, ok := m.(providers.Streamer)
	if !ok {
		return providers.Stream{}, false, nil
	}
	stream, err := st.StreamTrack(ctx, t)
	if err == nil && stream.URL != "" {
		return stream, true, nil
	}
	if err == nil {
		err = providers.ErrUnplayable
	}
	s.d.Log.Info("music_native_fallback", "source", t.Source, "code", errCode(err))
	return providers.Stream{}, false, err
}

// playYouTube plays the song from YouTube Music: matched there first when
// another catalog found it, then played on the screen's own player, or
// resolved by the musicPlayer instance into stream
func (s *Service) playYouTube(ctx context.Context, deviceID string, v settings.Values, q *queue, t *providers.Track, onScreen bool, stream *providers.Stream) error {
	if v.MusicPlayer == "" {
		return ErrNotSetUp
	}
	p, err := s.d.Player(ctx, v.MusicPlayer)
	if err != nil {
		return ErrNotSetUp
	}
	if t.VideoID == "" {
		matched, err := s.onYouTube(ctx, *t)
		if err != nil {
			return err
		}
		*t = matched
		s.mu.Lock()
		q.setCurrent(matched)
		s.mu.Unlock()
	}
	if onScreen {
		title, err := s.screen(ctx, deviceID, *t)
		if err != nil {
			return err
		}
		s.mu.Lock()
		q.started, q.last, q.shown = s.d.Now(), lastSeen{}, title
		if q.shown == "" {
			q.shown = t.Title
		}
		s.mu.Unlock()
		s.songStarted(deviceID, q, *t)
		s.seed(deviceID, q)
		return nil
	}
	*stream, err = p.Resolve(ctx, t.VideoID)
	return err
}

type playMetadata struct {
	Title      string `json:"title,omitempty"`
	Artist     string `json:"artist,omitempty"`
	Album      string `json:"album,omitempty"`
	ArtworkURL string `json:"artworkUrl,omitempty"`
}

type playArgs struct {
	URL      string       `json:"url"`
	Mode     string       `json:"mode"`
	Metadata playMetadata `json:"metadata"`
}

func playCommand(t providers.Track, s providers.Stream) playArgs {
	md := playMetadata{Title: t.Title, Artist: t.Artist, Album: t.Album}
	if strings.HasPrefix(t.ArtworkURL, "https://") && len(t.ArtworkURL) <= maxDeviceURL {
		md.ArtworkURL = t.ArtworkURL
	}
	return playArgs{URL: s.URL, Mode: "session", Metadata: md}
}

func (s *Service) modesOf(deviceID string) modes {
	m, ok := s.modes[deviceID]
	if !ok {
		m = modes{repeat: RepeatOff}
	}
	return m
}

// SetShuffle and SetRepeat set the device's modes, which its next queues keep
func (s *Service) SetShuffle(deviceID string, on bool) {
	s.mu.Lock()
	m := s.modesOf(deviceID)
	m.shuffle = on
	s.modes[deviceID] = m
	q := s.queues[deviceID]
	if q != nil {
		q.setShuffle(on)
	}
	s.mu.Unlock()
	if q != nil {
		s.fieldsChanged(deviceID)
	}
}

func (s *Service) SetRepeat(deviceID string, r Repeat) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.modesOf(deviceID)
	m.repeat = r
	s.modes[deviceID] = m
	if q := s.queues[deviceID]; q != nil {
		q.repeat = r
	}
}

// Forget drops the device's queue (its deletion, or a stop that should not
// resume)
func (s *Service) Forget(deviceID string) {
	s.mu.Lock()
	delete(s.queues, deviceID)
	s.mu.Unlock()
	s.fieldsChanged(deviceID)
}

// Jump plays the song n places ahead in play order (1 is the next one), as
// the queue's view lists them.
func (s *Service) Jump(ctx context.Context, deviceID string, n int) error {
	defer s.playLock(deviceID)()
	s.mu.Lock()
	q := s.queues[deviceID]
	if q == nil {
		s.mu.Unlock()
		return ErrNoQueue
	}
	if n < 1 || q.pos+n >= len(q.order) {
		s.mu.Unlock()
		return ErrQueueEnd
	}
	q.pos += n
	s.mu.Unlock()
	return s.playSkipping(ctx, deviceID, q, 1)
}
