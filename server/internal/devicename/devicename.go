// Package devicename holds the rules a device's name follows, at its create
// and at each rename.
package devicename

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
)

// MaxLength is the longest name, in characters.
const MaxLength = 64

// index is the unique index on lower(name) (migration 00027)
const index = "devices_name_lower"

// Clean trims name of white space and reports whether what remains is 1–64
// characters with no control or format character and no line or paragraph
// separator: none of them shows, and some reverse or break what follows.
func Clean(name string) (string, bool) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	bad := strings.ContainsFunc(name, func(r rune) bool { return unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) })
	return name, n > 0 && n <= MaxLength && !bad
}

// Taken reports a store error that is another device holding the name, in
// any case.
func Taken(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == index
}

// CleanRoom is Clean for a room's name, which may be empty: no room.
func CleanRoom(room string) (string, bool) {
	room = strings.TrimSpace(room)
	if room == "" {
		return "", true
	}
	return Clean(room)
}
