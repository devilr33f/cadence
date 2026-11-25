package favorites_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"git.shino.boo/devilreef/cadence/server/internal/favorites"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

type musicOnly struct{}

func (musicOnly) SearchMusic(context.Context, string, int) ([]providers.Track, error) {
	return nil, nil
}

type rater struct {
	musicOnly
	ratings []providers.TrackRating
	err     error
}

func (r *rater) RateTrack(_ context.Context, _ providers.Track, rating providers.TrackRating) error {
	r.ratings = append(r.ratings, rating)
	return r.err
}

func TestFavoritesSyncAndSurviveHistoryPruning(t *testing.T) {
	pool := storetest.Pool(t)
	q := db.New(pool)
	r := &rater{}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	s := favorites.New(q, func(context.Context, string) (providers.Music, error) { return r, nil }, func() time.Time { return now })
	track := providers.Track{
		Source: providers.SourceYandex, Instance: "ym", ID: "42", Title: "Song", Artist: "Artist",
		DurationS: 180, WebURL: "https://music.yandex.ru/album/7/track/42",
	}
	got, err := s.Set(context.Background(), track, providers.RatingLike)
	if err != nil || got.ProviderSync != favorites.ProviderUpdated || !got.Entry.Favorite || got.Entry.Feedback != providers.RatingLike {
		t.Fatalf("like %+v, err %v", got, err)
	}
	if got.Entry.WebURL != track.WebURL || got.Entry.Track.Instance != "ym" {
		t.Fatalf("favorite identity %+v", got.Entry)
	}
	listed, err := s.List(context.Background(), "song", 10)
	if err != nil || len(listed) != 1 || listed[0].ProviderSongID != "42" {
		t.Fatalf("favorites %+v, err %v", listed, err)
	}
	if _, err := pool.Exec(context.Background(), "DELETE FROM play_history"); err != nil {
		t.Fatal(err)
	}
	listed, _ = s.List(context.Background(), "", 10)
	if len(listed) != 1 {
		t.Fatal("history pruning removed a favorite")
	}
	now = now.Add(time.Minute)
	got, err = s.Set(context.Background(), track, providers.RatingDislike)
	if err != nil || got.Entry.Favorite || got.Entry.Feedback != providers.RatingDislike {
		t.Fatalf("dislike %+v, err %v", got, err)
	}
	listed, _ = s.List(context.Background(), "", 10)
	if len(listed) != 0 {
		t.Fatalf("disliked song stayed a favorite: %+v", listed)
	}
	if _, err = s.Set(context.Background(), track, providers.RatingNone); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetMusicSongState(context.Background(), db.GetMusicSongStateParams{Provider: "yandex", ProviderSongID: "42"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cleared state remains: %v", err)
	}
	want := []providers.TrackRating{providers.RatingLike, providers.RatingDislike, providers.RatingNone}
	if len(r.ratings) != len(want) {
		t.Fatalf("provider ratings %v", r.ratings)
	}
	for i := range want {
		if r.ratings[i] != want[i] {
			t.Fatalf("provider ratings %v", r.ratings)
		}
	}
}

func TestFavoritesAreLocalWhenTheProviderHasNoRatings(t *testing.T) {
	pool := storetest.Pool(t)
	s := favorites.New(db.New(pool), func(context.Context, string) (providers.Music, error) { return musicOnly{}, nil }, nil)
	track := providers.Track{Source: providers.SourceYouTube, Instance: "ytm", ID: "5qZQEq_C3vc", Title: "Song"}
	got, err := s.Set(context.Background(), track, providers.RatingLike)
	if err != nil || got.ProviderSync != favorites.ProviderNotSupported || !got.Entry.Favorite ||
		got.Entry.Track.VideoID != track.ID || got.Entry.WebURL != "https://music.youtube.com/watch?v="+track.ID {
		t.Fatalf("local favorite %+v, err %v", got, err)
	}
}

func TestFavoriteFailureDoesNotClaimAProviderUpdate(t *testing.T) {
	pool := storetest.Pool(t)
	q := db.New(pool)
	r := &rater{err: providers.ErrSignInRequired}
	s := favorites.New(q, func(context.Context, string) (providers.Music, error) { return r, nil }, nil)
	track := providers.Track{Source: providers.SourceYandex, Instance: "ym", ID: "42", Title: "Song"}
	if _, err := s.Set(context.Background(), track, providers.RatingLike); !errors.Is(err, providers.ErrSignInRequired) {
		t.Fatalf("provider failure %v", err)
	}
	if _, err := q.GetMusicSongState(context.Background(), db.GetMusicSongStateParams{Provider: "yandex", ProviderSongID: "42"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("failed provider update was stored: %v", err)
	}
}

func TestSetByIDUsesHistoryMetadataAndPublicVKIdentity(t *testing.T) {
	pool := storetest.Pool(t)
	q := db.New(pool)
	dev, err := q.CreateDevice(context.Background(), db.CreateDeviceParams{Name: "Kitchen", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(context.Background(), `INSERT INTO play_history (
		device_id, source, provider, kind, title, artist, dedupe_key, provider_song_id, web_url, provider_instance
	) VALUES ($1, 'music', 'yandex', 'track', 'Song', 'Artist', 'key', '42', 'https://music.yandex.ru/track/42', 'ym')`, dev.ID); err != nil {
		t.Fatal(err)
	}
	r := &rater{}
	s := favorites.New(q, func(context.Context, string) (providers.Music, error) { return r, nil }, nil)
	got, err := s.SetByID(context.Background(), "yandex", "42", providers.RatingLike)
	if err != nil || got.Entry.Title != "Song" || got.Entry.Track.Instance != "ym" || got.ProviderSync != favorites.ProviderUpdated {
		t.Fatalf("history rating %+v, err %v", got, err)
	}
	vk := providers.Track{Source: providers.SourceVK, ID: "-2001_456239017_secretKey", Title: "VK"}
	got, err = s.Set(context.Background(), vk, providers.RatingLike)
	if err != nil || got.Entry.ProviderSongID != "-2001_456239017" || got.Entry.WebURL != "https://vk.com/audio-2001_456239017" {
		t.Fatalf("vk identity %+v, err %v", got, err)
	}
	if _, err := s.SetByID(context.Background(), "yandex", uuid.NewString(), providers.RatingLike); !errors.Is(err, favorites.ErrNotFound) {
		t.Fatalf("unknown history song: %v", err)
	}
}
