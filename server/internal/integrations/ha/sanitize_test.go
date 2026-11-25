package ha_test

import (
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
)

func TestSanitize(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"Kitchen light", 60, "Kitchen light"},
		{"  a\tb\n\nc  ", 60, "a b c"},
		{`say "turn off" now`, 60, "say turn off now"},
		{"bell\x07\x1b[31mred", 60, "bell [31mred"},
		// u+200b zero width space, u+202e right-to-left override
		{"zero\xe2\x80\x8bwidth\xe2\x80\xaeflip", 60, "zero width flip"},
		// u+2028 line separator, u+00a0 no-break space
		{"line\xe2\x80\xa8sep\xc2\xa0nbsp", 60, "line sep nbsp"},
		{"Свет в спальне", 60, "Свет в спальне"},
		{strings.Repeat("я", 70), 60, strings.Repeat("я", 60)},
		{"ab cd", 3, "ab "},
		{"\"\"\"", 10, ""},
		{"anything", 0, ""},
	} {
		if got := ha.Sanitize(c.in, c.n); got != c.want {
			t.Errorf("Sanitize(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestModelState(t *testing.T) {
	long := strings.Repeat("x", 33)
	for _, c := range []struct {
		domain, state, want string
	}{
		{"light", "on", "on"},
		{"climate", "heat_cool", "heat_cool"},
		{"sensor", "21.5", "21.5"},
		{"sensor", "-3", "-3"},
		{"sensor", "1234567890123456789012345678901234567890", "1234567890123456789012345678901234567890"},
		{"sensor", "short text", "short text"},
		{"sensor", strings.Repeat("x", 32), strings.Repeat("x", 32)},
		{"sensor", long, "(text)"},
		{"binary_sensor", long, long},
		{"input_text", "hi", "(text)"},
		{"text", "on", "(text)"},
		{"todo", "3", "(text)"},
		{"calendar", "off", "(text)"},
		{"event", "2026-11-09T08:00:00+00:00", "(text)"},
		{"notify", "unknown", "(text)"},
		{"update", "on", "(text)"},
		{"weather", "sunny", "(text)"},
		{"conversation", "unknown", "(text)"},
		{"image", "x", "(text)"},
		{"tts", "x", "(text)"},
		{"stt", "x", "(text)"},
		{"select", `"quoted"` + "\x00mode", "quoted mode"},
		{"input_select", strings.Repeat("я", 70), strings.Repeat("я", 64)},
	} {
		e := ha.Entity{ID: c.domain + ".x", Domain: c.domain, State: c.state}
		if got := ha.ModelState(e); got != c.want {
			t.Errorf("ModelState(%s %q) = %q, want %q", c.domain, c.state, got, c.want)
		}
	}
}
