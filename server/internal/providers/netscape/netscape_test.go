package netscape_test

import (
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers/netscape"
)

func TestParseTakesTextOrBase64AndRefusesOthers(t *testing.T) {
	txt := ".x.com\tTRUE\t/\tTRUE\t1893456000\tauth_token\tA\n#HttpOnly_.x.com\tTRUE\t/\tTRUE\t1893456000\tct0\tB\n"
	file, err := netscape.Parse(txt)
	if err != nil || !strings.HasPrefix(string(file), "# Netscape HTTP Cookie File\n") {
		t.Fatalf("%q %v", file, err)
	}
	if got := netscape.For(file, "x.com"); got["auth_token"] != "A" || got["ct0"] != "B" {
		t.Fatalf("%v", got)
	}
	if got := netscape.For(file, "api.x.com"); got["ct0"] != "B" {
		t.Fatalf("subdomain: %v", got)
	}
	if got := netscape.For(file, "notx.com"); len(got) != 0 {
		t.Fatalf("another site: %v", got)
	}
	for _, bad := range []string{"just text", ".x.com\tTRUE\t/\n", "# only a comment\n"} {
		if _, err := netscape.Parse(bad); err == nil {
			t.Errorf("took %q", bad)
		}
	}
}

// set renews a cookie in place, whatever its domain's leading dot or HttpOnly mark, and adds one it lacks
func TestSetRenewsOrAdds(t *testing.T) {
	file, _ := netscape.Parse("# Netscape HTTP Cookie File\n# kept comment\nx.com\tFALSE\t/\tTRUE\t1\tct0\tOLD\n" +
		"#HttpOnly_.x.com\tTRUE\t/\tTRUE\t1\tauth_token\tA\n.other.com\tTRUE\t/\tTRUE\t1\tct0\tOTHER\n")
	file = netscape.Set(file, ".x.com", "ct0", "NEW", 1900000000)
	file = netscape.Set(file, "x.com", "guest_id", "G", 0)
	got := netscape.For(file, "x.com")
	if got["ct0"] != "NEW" || got["auth_token"] != "A" || got["guest_id"] != "G" {
		t.Fatalf("%v", got)
	}
	if netscape.For(file, "other.com")["ct0"] != "OTHER" || !strings.Contains(string(file), "# kept comment\n") ||
		!strings.Contains(string(file), "x.com\tFALSE\t/\tTRUE\t1900000000\tct0\tNEW\n") {
		t.Fatalf("%s", file)
	}
	if _, err := netscape.Parse(string(file)); err != nil {
		t.Fatalf("set left a file parse refuses: %v", err)
	}
}
