package memory

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// vectorText renders pgvector's text form, so queries need no type registration.
func vectorText(v []float32) (string, error) {
	if len(v) == 0 {
		return "", fmt.Errorf("%w: empty vector", ErrInvalid)
	}
	var b strings.Builder
	zero := true
	b.WriteByte('[')
	for i, f := range v {
		if f != 0 {
			zero = false
		}
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return "", fmt.Errorf("%w: non-finite vector value", ErrInvalid)
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	if zero {
		return "", fmt.Errorf("%w: zero vector", ErrInvalid)
	}
	b.WriteByte(']')
	return b.String(), nil
}

func parseUUID(id string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(id); err != nil {
		return u, fmt.Errorf("%w: bad id", ErrInvalid)
	}
	return u, nil
}

// optUUID maps empty to NULL.
func optUUID(id string) (pgtype.UUID, error) {
	if id == "" {
		return pgtype.UUID{}, nil
	}
	return parseUUID(id)
}

func optText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func timestamp(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func ptrText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

// i32 clamps a caller's count into the int32 sqlc wants.
func i32(n int) int32 { return int32(max(0, min(n, math.MaxInt32))) } //nolint:gosec // clamped

// uuids never returns nil: a NULL array would match nothing in `= ANY`.
func uuids(ids []string) ([]pgtype.UUID, error) {
	out := make([]pgtype.UUID, len(ids))
	for i, id := range ids {
		u, err := parseUUID(id)
		if err != nil {
			return nil, err
		}
		out[i] = u
	}
	return out, nil
}
