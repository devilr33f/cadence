package receivername_test

import (
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/receivername"
)

func TestPick(t *testing.T) {
	for _, c := range []struct{ configured, device, want string }{
		{"Kitchen", "Bedroom Station 2", "Kitchen"},
		{"", "Bedroom Station 2", "Bedroom Station 2"},
		{"", "", receivername.Default},
		{"", " \t\u200b ", receivername.Default},
	} {
		if got := receivername.Pick(c.configured, c.device, 32); got != c.want {
			t.Errorf("Pick(%q, %q) = %q, want %q", c.configured, c.device, got, c.want)
		}
	}
}

func TestFrom(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"  Bedroom TV \t", "Bedroom TV"},
		{"Кухня", "Кухня"},
		{"Bed\x00room\n", "Bedroom"},
		{"a\u202eb\u200bc ", "abc"},
		{"bad\xffutf8", "badutf8"},
		{strings.Repeat("a", 40), strings.Repeat("a", 32)},
		// 2-byte letters: 16 whole ones fit 32 bytes, never half of the 17th
		{strings.Repeat("ё", 20), strings.Repeat("ё", 16)},
		{"a" + strings.Repeat("ё", 20), "a" + strings.Repeat("ё", 15)},
		// a cut that ends in a space leaves none
		{strings.Repeat("a", 31) + " b", strings.Repeat("a", 31)},
		{"", ""},
	} {
		if got := receivername.From(c.in, 32); got != c.want {
			t.Errorf("From(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
