// Package ringsounds keeps the sounds a device's rings stream at ring time (owner, 2026-11-16): an alarm's music,
// transcoded once when it is set, and a reminder's speech, synthesized when it is set. Both stay on the server and
// the device fetches them with its token (GET /v2/ring-sound/{id}); nothing is cached on the device, which rings its
// own tone or bundled prompt when the server cannot be reached. A sound's title is the user's setting (the music's
// title, the reminder's text), kept as long as an alarm or reminder names it; it is never logged, and no stream or
// signed url is ever stored (D11).
package ringsounds

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/prompts"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// Kind is what a sound is made of.
type Kind string

const (
	KindMusic  Kind = "music"
	KindSpeech Kind = "speech"
)

const (
	// MaxTextRunes bounds a reminder's text
	MaxTextRunes = 300
	// a reminder's speech, once its silence is trimmed
	maxSpeechSeconds = 20
	// an alarm's music is cut to this
	MaxMusicSeconds = 600
	// a sound no alarm or reminder names goes this long after it last did
	unusedFor = 7 * 24 * time.Hour
	idLength  = 16
	// bounds a store call
	storeTimeout = 5 * time.Second
)

var (
	// ErrNoTTS is a device without a text-to-speech provider set
	ErrNoTTS = errors.New("ringsounds: no tts set")
	// ErrTTS is a provider that failed or spoke nothing
	ErrTTS = errors.New("ringsounds: tts failed")
	// ErrTooLong is speech past 20 s or a text past MaxTextRunes
	ErrTooLong = errors.New("ringsounds: too long")
	// ErrNoTranscoder is a server without ffmpeg: no music can be made
	ErrNoTranscoder = errors.New("ringsounds: no transcoder")
	// ErrTranscode is music that could not be fetched or transcoded
	ErrTranscode = errors.New("ringsounds: transcode failed")
	// ErrStore is a database that failed
	ErrStore = errors.New("ringsounds: store failed")
	// ErrNotFound is no sound by that id for that device
	ErrNotFound = errors.New("ringsounds: not found")
)

// Queries are the store's ring_sounds queries (db.Queries).
type Queries interface {
	PutRingSound(ctx context.Context, arg db.PutRingSoundParams) (string, error)
	RingSoundBySource(ctx context.Context, arg db.RingSoundBySourceParams) (db.RingSoundBySourceRow, error)
	GetRingSound(ctx context.Context, arg db.GetRingSoundParams) (db.GetRingSoundRow, error)
	RingSoundInfo(ctx context.Context, arg db.RingSoundInfoParams) ([]db.RingSoundInfoRow, error)
	ListRingSounds(ctx context.Context) ([]db.ListRingSoundsRow, error)
	TouchRingSounds(ctx context.Context, arg db.TouchRingSoundsParams) error
	DeleteStaleRingSounds(ctx context.Context, before pgtype.Timestamptz) (int64, error)
}

// Voice is the device's text-to-speech as its turns speak: the provider, its voice ("" for the provider's default)
// and the language. ErrNoTTS when the device has none.
type Voice func(ctx context.Context, deviceID string) (tts providers.TTS, voice, lang string, err error)

// Transcoder fetches what open returns (a song's upstream, read on the server's own address) and makes it 48 kHz
// mono Ogg Opus of at most maxSeconds, its loudness normalized and its first seconds faded in.
type Transcoder func(ctx context.Context, open func(ctx context.Context) (Body, error), maxSeconds int) ([]byte, error)

type Deps struct {
	Q     Queries
	Voice Voice
	// Encoder makes the speech's Opus; nil is libopus (prompts.DefaultEncoder)
	Encoder prompts.NewEncoder
	// Transcode makes the music; nil: no music (ErrNoTranscoder)
	Transcode Transcoder
	Now       func() time.Time
	Log       *slog.Logger
}

// Sound is a ring sound as the tools see it.
type Sound struct {
	ID    string
	Kind  Kind
	Title string
	// it was made before, from the same source, and kept
	Reused bool
}

type Service struct{ d Deps }

func New(d Deps) *Service {
	if d.Encoder == nil {
		d.Encoder = prompts.DefaultEncoder
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Service{d: d}
}

// CanMakeMusic is whether this server transcodes music (it has ffmpeg).
func (s *Service) CanMakeMusic() bool { return s.d.Transcode != nil }

// Speech speaks text in the device's voice into a ring sound, or finds the one the same text, voice and language
// made before. Its title is the text.
func (s *Service) Speech(ctx context.Context, deviceID, text string) (Sound, error) {
	text = strings.TrimSpace(text)
	if text == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > MaxTextRunes {
		return Sound{}, ErrTooLong
	}
	dev, ok := parseDevice(deviceID)
	if !ok {
		return Sound{}, ErrNotFound
	}
	tts, voice, lang, err := s.d.Voice(ctx, deviceID)
	if err != nil {
		if errors.Is(err, ErrNoTTS) {
			return Sound{}, ErrNoTTS
		}
		return Sound{}, ErrTTS
	}
	key := sourceKey(string(KindSpeech), lang, voice, text)
	if found, ok := s.bySource(ctx, dev, KindSpeech, key); ok {
		return found, nil
	}
	file, err := prompts.RenderSpeech(ctx, tts, voice, lang, text, maxSpeechSeconds, s.d.Encoder)
	switch {
	case errors.Is(err, prompts.ErrSpeechTooLong):
		s.d.Log.Warn("ring_sound_failed", "device_id", deviceID, "kind", string(KindSpeech), "code", "too_long")
		return Sound{}, ErrTooLong
	case err != nil:
		s.d.Log.Warn("ring_sound_failed", "device_id", deviceID, "kind", string(KindSpeech), "code", "tts")
		return Sound{}, ErrTTS
	}
	return s.put(ctx, dev, deviceID, KindSpeech, key, text, file)
}

// Music transcodes what open fetches (key names it: the provider's track, the same each time) into a ring sound
// titled title, or finds the one made from key before.
func (s *Service) Music(ctx context.Context, deviceID, key, title string, open func(ctx context.Context) (Body, error)) (Sound, error) {
	if s.d.Transcode == nil {
		return Sound{}, ErrNoTranscoder
	}
	dev, ok := parseDevice(deviceID)
	if !ok {
		return Sound{}, ErrNotFound
	}
	source := sourceKey(string(KindMusic), key)
	if found, ok := s.bySource(ctx, dev, KindMusic, source); ok {
		return found, nil
	}
	file, err := s.d.Transcode(ctx, open, MaxMusicSeconds)
	if err != nil {
		code := "transcode"
		if ctx.Err() != nil {
			code = "canceled"
		}
		s.d.Log.Warn("ring_sound_failed", "device_id", deviceID, "kind", string(KindMusic), "code", code)
		return Sound{}, ErrTranscode
	}
	return s.put(ctx, dev, deviceID, KindMusic, source, clip(title), file)
}

// Info is what the device's sounds among ids are, by id: an id it does not hold is absent.
type Info struct {
	Kind       Kind
	Title      string
	Size       int
	DurationMs int
}

func (s *Service) Info(ctx context.Context, deviceID string, ids []string) map[string]Info {
	out := map[string]Info{}
	dev, ok := parseDevice(deviceID)
	if !ok || len(ids) == 0 {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	rows, err := s.d.Q.RingSoundInfo(ctx, db.RingSoundInfoParams{DeviceID: dev, Ids: ids})
	if err != nil {
		return out
	}
	for _, r := range rows {
		out[r.ID] = Info{Kind: Kind(r.Kind), Title: r.Title, Size: int(r.Size), DurationMs: int(r.DurationMs)}
	}
	return out
}

func (s *Service) bySource(ctx context.Context, dev pgtype.UUID, kind Kind, key string) (Sound, bool) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	row, err := s.d.Q.RingSoundBySource(ctx, db.RingSoundBySourceParams{DeviceID: dev, Kind: string(kind), SourceKey: key})
	if err != nil {
		return Sound{}, false
	}
	_ = s.d.Q.TouchRingSounds(ctx, db.TouchRingSoundsParams{Now: stamp(s.d.Now()), DeviceID: dev, Ids: []string{row.ID}})
	return Sound{ID: row.ID, Kind: kind, Title: row.Title, Reused: true}, true
}

func (s *Service) put(ctx context.Context, dev pgtype.UUID, deviceID string, kind Kind, key, title string, file []byte) (Sound, error) {
	sum := sha256.Sum256(file)
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	id, err := s.d.Q.PutRingSound(ctx, db.PutRingSoundParams{
		ID: newID(), DeviceID: dev, Kind: string(kind), SourceKey: key, Sha256: hex.EncodeToString(sum[:]), Audio: file,
		Size: int32(len(file)), DurationMs: int32(oggDurationMs(file)), Title: title, Now: stamp(s.d.Now()), //nolint:gosec // at most a few MiB
	})
	if err != nil {
		s.d.Log.Error("ring_sound_failed", "device_id", deviceID, "kind", string(kind), "code", "store")
		return Sound{}, ErrStore
	}
	s.d.Log.Info("ring_sound_made", "device_id", deviceID, "kind", string(kind), "bytes", len(file))
	return Sound{ID: id, Kind: kind, Title: title}, nil
}

// get is the device's sound by id
func (s *Service) get(ctx context.Context, deviceID, id string) (db.GetRingSoundRow, error) {
	dev, ok := parseDevice(deviceID)
	if !ok || !validID(id) {
		return db.GetRingSoundRow{}, ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	row, err := s.d.Q.GetRingSound(ctx, db.GetRingSoundParams{ID: id, DeviceID: dev})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return row, ErrNotFound
	case err != nil:
		return row, ErrStore
	}
	_ = s.d.Q.TouchRingSounds(ctx, db.TouchRingSoundsParams{Now: stamp(s.d.Now()), DeviceID: dev, Ids: []string{id}})
	return row, nil
}

// clip keeps a title to a card's length
func clip(title string) string {
	title = strings.TrimSpace(title)
	if utf8.RuneCountInString(title) <= MaxTextRunes {
		return title
	}
	return string([]rune(title)[:MaxTextRunes])
}

func sourceKey(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

const idAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// newID is [a-z0-9]{16}, the device's id pattern
func newID() string {
	b := make([]byte, 0, idLength)
	var r [32]byte
	for len(b) < idLength {
		_, _ = rand.Read(r[:])
		for _, c := range r {
			// 252 is the largest multiple of 36 a byte holds: higher bytes would favour the first letters
			if c < 252 && len(b) < idLength {
				b = append(b, idAlphabet[int(c)%len(idAlphabet)])
			}
		}
	}
	return string(b)
}

func validID(id string) bool {
	if len(id) != idLength {
		return false
	}
	for _, c := range []byte(id) {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func parseDevice(id string) (pgtype.UUID, bool) {
	u, err := uuid.Parse(id)
	if err != nil {
		return pgtype.UUID{}, false
	}
	return pgtype.UUID{Bytes: u, Valid: true}, true
}

func stamp(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
