package devicename_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"git.shino.boo/devilreef/cadence/server/internal/devicename"
)

func TestClean(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"  Living room \t", "Living room"},
		{"Кухня", "Кухня"},
		{strings.Repeat("ё", 64), strings.Repeat("ё", 64)},
	} {
		if got, ok := devicename.Clean(c.in); !ok || got != c.want {
			t.Errorf("%q: %q %v", c.in, got, ok)
		}
	}
	for _, in := range []string{
		"", "   ", strings.Repeat("ё", 65),
		"a\x00b", "a\nb", "a\x7fb", // Cc
		"\u200b", "kitchen\u200b", "\ufeffkitchen", "a\u202eb", "a\u00adb", // Cf
		"a\u2028b", // Zl
		"a\u2029b", // Zp
	} {
		if got, ok := devicename.Clean(in); ok {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
}

func TestTaken(t *testing.T) {
	if !devicename.Taken(&pgconn.PgError{Code: "23505", ConstraintName: "devices_name_lower"}) {
		t.Fatal("the name index's violation is not taken")
	}
	for _, err := range []error{
		nil, errors.New("x"), &pgconn.PgError{Code: "23505", ConstraintName: "devices_token_hash_key"},
		&pgconn.PgError{Code: "23503", ConstraintName: "devices_name_lower"},
	} {
		if devicename.Taken(err) {
			t.Errorf("%v taken", err)
		}
	}
}
