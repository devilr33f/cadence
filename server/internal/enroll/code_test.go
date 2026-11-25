package enroll_test

import (
	"regexp"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/enroll"
)

func TestNormalizeCode(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"abcd-efgh", "ABCDEFGH", true},
		{"i0l0-0000", "10100000", true},
		{"oooo-0000", "00000000", true},
		{"OOOO-0000", "00000000", true},
		{"ABCD EFGH", "ABCDEFGH", true},
		{"ABCD-EFG", "", false},
		{"ABCD-EFGU", "", false},
	}
	for _, c := range cases {
		got, ok := enroll.NormalizeCode(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("%q: got %q %v", c.in, got, ok)
		}
	}
}

func TestNewCodeShape(t *testing.T) {
	re := regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`)
	for range 50 {
		c, err := enroll.NewCode()
		if err != nil || !re.MatchString(c) {
			t.Fatalf("%q %v", c, err)
		}
		if _, ok := enroll.NormalizeCode(c); !ok {
			t.Fatalf("%q does not normalize", c)
		}
	}
}
