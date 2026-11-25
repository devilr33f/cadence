package conversation

import (
	"encoding/base64"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
)

const (
	// maxPage is the most turns one page holds
	maxPage = 100
	// maxCursor bounds a cursor as sent, maxTurnID a turn id in it or a path
	maxCursor = 512
	maxTurnID = 256
	// the last microsecond of 9999, the latest instant a cursor names; a
	// stored turn is never older than 1970
	maxCursorMicros = 253402300799999999
)

// encodeCursor is the opaque cursor of the turns after the one created at at
// with id: its creation in microseconds, as the store keeps it, and its id.
func encodeCursor(at time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(at.UnixMicro(), 10) + ":" + id))
}

// decodeCursor is the (created_at, turn_id) a page starts after; "" is the
// first page, after infinity. Anything the store did not make is ErrCursor.
func decodeCursor(c string) (pgtype.Timestamptz, string, error) {
	if c == "" {
		return pgtype.Timestamptz{InfinityModifier: pgtype.Infinity, Valid: true}, "", nil
	}
	if len(c) > maxCursor {
		return pgtype.Timestamptz{}, "", ErrCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return pgtype.Timestamptz{}, "", ErrCursor
	}
	us, id, ok := strings.Cut(string(raw), ":")
	n, err := strconv.ParseInt(us, 10, 64)
	if !ok || err != nil || n < 0 || n > maxCursorMicros || !validID(id) {
		return pgtype.Timestamptz{}, "", ErrCursor
	}
	return pgtype.Timestamptz{Time: time.UnixMicro(n), Valid: true}, id, nil
}

// validID is a turn id the store can hold: 1–256 bytes of UTF-8, no NUL.
func validID(id string) bool {
	return id != "" && len(id) <= maxTurnID && utf8.ValidString(id) && !strings.ContainsRune(id, 0)
}
