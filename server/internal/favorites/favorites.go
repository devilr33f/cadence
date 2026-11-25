// Package favorites keeps the household's durable music likes, dislikes and
// favorites, and synchronizes them to providers that support track ratings.
package favorites

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

const (
	ProviderUpdated      = "updated"
	ProviderNotSupported = "not_supported"
	maxFavorites         = 200
)

var ErrNotFound = errors.New("favorites: song not found")

// FavoriteEntry is one song's household state. Track is its playback form,
// never API data: instance stays internal and no stream URL is present.
type FavoriteEntry struct {
	Provider       string                `json:"provider"`
	ProviderSongID string                `json:"providerSongId"`
	Title          string                `json:"title"`
	Artist         string                `json:"artist,omitempty"`
	Album          string                `json:"album,omitempty"`
	DurationS      int                   `json:"durationS,omitempty"`
	ArtworkURL     string                `json:"artworkUrl,omitempty"`
	WebURL         string                `json:"webUrl,omitempty"`
	Feedback       providers.TrackRating `json:"feedback"`
	Favorite       bool                  `json:"favorite"`
	UpdatedAt      time.Time             `json:"updatedAt"`
	Track          providers.Track       `json:"-"`
}

// Entry is kept as the service's short name; OpenAPI sees FavoriteEntry.
type Entry = FavoriteEntry

type Result struct {
	Entry        Entry
	ProviderSync string
}

type Music func(ctx context.Context, name string) (providers.Music, error)

type Service struct {
	q     *db.Queries
	music Music
	now   func() time.Time
}

func New(q *db.Queries, music Music, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{q: q, music: music, now: now}
}

// Set updates the provider first when its instance supports ratings, then the
// household state. A provider failure leaves the local state unchanged.
func (s *Service) Set(ctx context.Context, track providers.Track, rating providers.TrackRating) (Result, error) {
	identity, ok := providers.PublicSongIdentity(track)
	if !ok || !providers.ValidTrackRating(rating) {
		return Result{}, errors.New(providers.CodeMusicFailed)
	}
	sync := ProviderNotSupported
	if track.Instance != "" && s.music != nil {
		m, err := s.music(ctx, track.Instance)
		if err == nil {
			if rater, ok := m.(providers.TrackRater); ok {
				if err := rater.RateTrack(ctx, track, rating); err != nil {
					return Result{}, err
				}
				sync = ProviderUpdated
			}
		}
	}
	track.Source = providers.MusicSource(identity.Provider)
	track.ID = identity.SongID
	track.WebURL = identity.WebURL
	if track.Source == providers.SourceYouTube && track.VideoID == "" {
		track.VideoID = track.ID
	}
	entry := Entry{
		Provider: identity.Provider, ProviderSongID: identity.SongID, Title: track.Title, Artist: track.Artist,
		Album: track.Album, DurationS: track.DurationS, ArtworkURL: track.ArtworkURL, WebURL: track.WebURL,
		Feedback: rating, Favorite: rating == providers.RatingLike, UpdatedAt: s.now().UTC(), Track: track,
	}
	params := db.DeleteMusicSongStateParams{Provider: identity.Provider, ProviderSongID: identity.SongID}
	if rating == providers.RatingNone {
		if _, err := s.q.DeleteMusicSongState(ctx, params); err != nil {
			return Result{}, err
		}
		return Result{Entry: entry, ProviderSync: sync}, nil
	}
	row, err := s.q.UpsertMusicSongState(ctx, db.UpsertMusicSongStateParams{
		Provider: identity.Provider, ProviderSongID: identity.SongID, Instance: track.Instance, VideoID: track.VideoID,
		Title: track.Title, Artist: track.Artist, Album: track.Album, DurationS: int32(min(max(track.DurationS, 0), 2_147_483_647)),
		ArtworkUrl: track.ArtworkURL, WebUrl: track.WebURL, Feedback: string(rating), Now: stamp(entry.UpdatedAt),
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Entry: entryFromState(row), ProviderSync: sync}, nil
}

// SetByID rates a song from its stored state or latest history metadata.
func (s *Service) SetByID(ctx context.Context, provider, songID string, rating providers.TrackRating) (Result, error) {
	track, err := s.track(ctx, provider, songID)
	if err != nil {
		if rating != providers.RatingNone || !errors.Is(err, ErrNotFound) {
			return Result{}, err
		}
		track = providers.Track{Source: providers.MusicSource(provider), ID: songID}
	}
	return s.Set(ctx, track, rating)
}

func (s *Service) track(ctx context.Context, provider, songID string) (providers.Track, error) {
	state, err := s.q.GetMusicSongState(ctx, db.GetMusicSongStateParams{Provider: provider, ProviderSongID: songID})
	if err == nil {
		return trackFromState(state), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return providers.Track{}, err
	}
	h, err := s.q.LatestMusicSongHistory(ctx, db.LatestMusicSongHistoryParams{Provider: provider, ProviderSongID: songID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return providers.Track{}, ErrNotFound
		}
		return providers.Track{}, err
	}
	source := providers.MusicSource(provider)
	if source == "" {
		return providers.Track{}, ErrNotFound
	}
	track := providers.Track{Source: source, Instance: h.ProviderInstance, ID: songID, Title: h.Title,
		Artist: h.Artist, Album: h.Album, ArtworkURL: h.ArtworkUrl, WebURL: h.WebUrl}
	if source == providers.SourceYouTube {
		track.VideoID = songID
	}
	return track, nil
}

func (s *Service) List(ctx context.Context, query string, limit int) ([]Entry, error) {
	query = strings.TrimSpace(query)
	if len(query) > 200 {
		query = query[:200]
	}
	rows, err := s.q.ListMusicFavorites(ctx, db.ListMusicFavoritesParams{Query: query, MaxRows: int32(min(max(limit, 1), maxFavorites))})
	if err != nil {
		return nil, err
	}
	out := make([]Entry, len(rows))
	for i, row := range rows {
		out[i] = entryFromState(row)
	}
	return out, nil
}

func trackFromState(row db.MusicSongState) providers.Track {
	track := providers.Track{
		Source: providers.MusicSource(row.Provider), Instance: row.Instance, ID: row.ProviderSongID,
		VideoID: row.VideoID, Title: row.Title, Artist: row.Artist, Album: row.Album, DurationS: int(row.DurationS),
		ArtworkURL: row.ArtworkUrl, WebURL: row.WebUrl,
	}
	if track.Source == providers.SourceYouTube && track.VideoID == "" {
		track.VideoID = track.ID
	}
	return track
}

func entryFromState(row db.MusicSongState) Entry {
	return Entry{
		Provider: row.Provider, ProviderSongID: row.ProviderSongID, Title: row.Title, Artist: row.Artist,
		Album: row.Album, DurationS: int(row.DurationS), ArtworkURL: row.ArtworkUrl, WebURL: row.WebUrl,
		Feedback: providers.TrackRating(row.Feedback), Favorite: row.FavoritedAt.Valid,
		UpdatedAt: row.UpdatedAt.Time.UTC(), Track: trackFromState(row),
	}
}

func stamp(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t.UTC(), Valid: true} }
