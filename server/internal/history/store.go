package history

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

const (
	maxPage     = 100
	maxCursor   = 512
	maxCursorUs = 253402300799999999
)

var (
	ErrCursor   = errors.New("history: invalid cursor")
	ErrDevice   = errors.New("history: invalid device")
	ErrNotFound = errors.New("history: not found")
)

// HistoryEntry is one play as the admin API returns it. DedupeKey is internal identity.
type HistoryEntry struct {
	ID               int64      `json:"id"`
	DeviceID         string     `json:"deviceId"`
	DeviceName       string     `json:"deviceName"`
	Source           string     `json:"source"`
	Provider         string     `json:"provider,omitempty"`
	Kind             string     `json:"kind" enum:"track,video,episode,stream"`
	Title            string     `json:"title"`
	Artist           string     `json:"artist,omitempty"`
	Album            string     `json:"album,omitempty"`
	ArtworkURL       string     `json:"artworkUrl,omitempty"`
	ArtworkHash      string     `json:"artworkHash,omitempty"`
	CatalogID        string     `json:"catalogId,omitempty"`
	ProviderSongID   string     `json:"providerSongId,omitempty"`
	WebURL           string     `json:"webUrl,omitempty"`
	Feedback         string     `json:"feedback" enum:"none,like,dislike"`
	Favorite         bool       `json:"favorite"`
	TurnID           string     `json:"turnId,omitempty"`
	StartedAt        time.Time  `json:"startedAt"`
	EndedAt          *time.Time `json:"endedAt,omitempty"`
	PlayedMS         int64      `json:"playedMs" minimum:"0"`
	DedupeKey        string     `json:"-"`
	ProviderInstance string     `json:"-"`
}

type HistoryPage struct {
	Items []HistoryEntry `json:"items" nullable:"false"`
	Next  string         `json:"next,omitempty"`
}

func (s *Service) List(ctx context.Context, deviceID, before string, limit int) (HistoryPage, error) {
	limit = min(max(limit, 1), maxPage)
	dev, err := parseUUID(deviceID)
	if err != nil {
		return HistoryPage{}, ErrDevice
	}
	at, id, err := decodeCursor(before)
	if err != nil {
		return HistoryPage{}, err
	}
	rows, err := s.q.PlayHistoryPage(ctx, db.PlayHistoryPageParams{
		DeviceID: dev, BeforeAt: at, BeforeID: id, MaxRows: int32(limit + 1),
	})
	if err != nil {
		return HistoryPage{}, err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = encodeCursor(last.StartedAt.Time, last.ID)
	}
	out := HistoryPage{Items: make([]HistoryEntry, len(rows)), Next: next}
	for i, r := range rows {
		out.Items[i] = HistoryEntry{
			ID: r.ID, DeviceID: uuid.UUID(r.DeviceID.Bytes).String(), DeviceName: r.DeviceName,
			Source: r.Source, Provider: r.Provider, Kind: r.Kind, Title: r.Title, Artist: r.Artist, Album: r.Album,
			ArtworkURL: r.ArtworkUrl, ArtworkHash: r.ArtworkHash, CatalogID: r.CatalogID,
			ProviderSongID: r.ProviderSongID, WebURL: r.WebUrl, Feedback: r.Feedback, Favorite: r.Favorite,
			TurnID: r.TurnID, StartedAt: r.StartedAt.Time.UTC(), EndedAt: timePtr(r.EndedAt), PlayedMS: r.PlayedMs,
		}
	}
	return out, nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	n, err := s.q.DeletePlayHistory(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func parseUUID(raw string) (pgtype.UUID, error) {
	if raw == "" {
		return pgtype.UUID{}, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return pgtype.UUID{}, ErrDevice
	}
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

func encodeCursor(at time.Time, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(at.UnixMicro(), 10) + ":" + strconv.FormatInt(id, 10)))
}

func decodeCursor(raw string) (pgtype.Timestamptz, int64, error) {
	if raw == "" {
		return pgtype.Timestamptz{InfinityModifier: pgtype.Infinity, Valid: true}, int64(^uint64(0) >> 1), nil
	}
	if len(raw) > maxCursor {
		return pgtype.Timestamptz{}, 0, ErrCursor
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return pgtype.Timestamptz{}, 0, ErrCursor
	}
	us, rawID, ok := strings.Cut(string(decoded), ":")
	micros, err := strconv.ParseInt(us, 10, 64)
	if !ok || err != nil || micros < 0 || micros > maxCursorUs {
		return pgtype.Timestamptz{}, 0, ErrCursor
	}
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		return pgtype.Timestamptz{}, 0, ErrCursor
	}
	return stamp(time.UnixMicro(micros)), id, nil
}
