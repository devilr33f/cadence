package media_test

import (
	"os"
	"strings"
	"testing"
)

// the operator's notes on media sessions and their art stay with the code
func TestReadmeDescribesMedia(t *testing.T) {
	b, err := os.ReadFile("../../../deploy/server/README.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	at := strings.Index(doc, "\n## Media sessions (admin API)\n")
	if at < 0 {
		t.Fatal("no Media sessions section")
	}
	section := doc[at+1:]
	if next := strings.Index(section[3:], "\n## "); next >= 0 {
		section = section[:next+3]
	}
	for _, want := range []string{
		"/media", "/artwork", "artworkHash", "`no_artwork`", "`artwork_unavailable`", "https", "CGNAT", "DNS rebinding",
		"no redirect", "5 s", "5 MiB", "32 images or 32 MiB", "memory only", "reason=url|address|dns",
		"`artwork_changed`", "port 443", "kept 30 s", "At most 4 addresses", "TLS handshake", "`Content-Encoding`",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("the Media sessions section does not mention %q", want)
		}
	}
}
