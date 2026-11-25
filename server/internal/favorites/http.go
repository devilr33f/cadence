package favorites

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

type favoritesIn struct {
	Query string `query:"query" maxLength:"200" doc:"Optional case-insensitive title, artist or album filter."`
	Limit int    `query:"limit" minimum:"1" maximum:"200" doc:"At most 200, newest favorites first; default 100."`
}

type favoritesOut struct {
	Body struct {
		Items []Entry `json:"items" nullable:"false"`
	}
}

type ratingIn struct {
	Provider string `path:"provider"`
	SongID   string `path:"songId" maxLength:"128"`
	Body     struct {
		Feedback providers.TrackRating `json:"feedback" enum:"none,like,dislike" doc:"like favorites the song, dislike removes it from favorites, none clears both marks."`
	}
}

type ratingOut struct {
	Body struct {
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
		UpdatedAt      string                `json:"updatedAt" format:"date-time"`
		ProviderSync   string                `json:"providerSync" enum:"updated,not_supported" doc:"updated means the provider accepted the rating; not_supported means it is Cadence-local only."`
	}
}

// RegisterAdmin serves the household's durable favorites and song ratings.
// These routes are admin-only; play history keeps its own 14-day lifecycle.
func RegisterAdmin(a huma.API, s *Service) {
	huma.Register(a, huma.Operation{
		OperationID: "list-music-favorites", Method: http.MethodGet, Path: "/music/favorites", Tags: []string{"history"},
		Summary: "List the household's favorite songs",
		Description: "Durable Cadence favorites, independent of the 14-day play history. Each item has its provider's " +
			"public song id and page, never a signed or stream URL.",
	}, func(ctx context.Context, in *favoritesIn) (*favoritesOut, error) {
		limit := in.Limit
		if limit == 0 {
			limit = 100
		}
		items, err := s.List(ctx, in.Query, limit)
		if err != nil {
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		out := &favoritesOut{}
		out.Body.Items = items
		return out, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "rate-music-song", Method: http.MethodPut, Path: "/music/songs/{provider}/{songId}/feedback", Tags: []string{"history"},
		Summary: "Like, dislike or clear a song's rating",
		Description: "Updates the provider when its stored instance supports ratings, then Cadence's durable song state. " +
			"A provider failure changes nothing locally. Like adds a favorite; dislike and none remove it.",
	}, func(ctx context.Context, in *ratingIn) (*ratingOut, error) {
		if providers.MusicSource(in.Provider) == "" || in.SongID == "" || !providers.ValidTrackRating(in.Body.Feedback) {
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid", "/body/feedback")
		}
		got, err := s.SetByID(ctx, in.Provider, in.SongID, in.Body.Feedback)
		if err != nil {
			return nil, ratingError(err)
		}
		out := &ratingOut{}
		out.Body.Provider, out.Body.ProviderSongID = got.Entry.Provider, got.Entry.ProviderSongID
		out.Body.Title, out.Body.Artist, out.Body.Album = got.Entry.Title, got.Entry.Artist, got.Entry.Album
		out.Body.DurationS, out.Body.ArtworkURL, out.Body.WebURL = got.Entry.DurationS, got.Entry.ArtworkURL, got.Entry.WebURL
		out.Body.Feedback, out.Body.Favorite = got.Entry.Feedback, got.Entry.Favorite
		out.Body.UpdatedAt = got.Entry.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00")
		out.Body.ProviderSync = got.ProviderSync
		return out, nil
	})
}

func ratingError(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return api.Error(http.StatusNotFound, "not_found")
	case errors.Is(err, providers.ErrSignInRequired):
		return api.Error(http.StatusConflict, "sign_in_required")
	case err != nil && err.Error() == providers.CodeMusicFailed:
		return api.Error(http.StatusServiceUnavailable, "music_failed")
	case errors.Is(err, providers.ErrUnplayable):
		return api.Error(http.StatusUnprocessableEntity, "invalid")
	default:
		return api.Error(http.StatusInternalServerError, "internal")
	}
}
