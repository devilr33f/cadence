//go:build live

package xweb

import (
	"context"
	"os"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
	"git.shino.boo/devilreef/cadence/server/internal/providers/netscape"
)

// go test -tags live -run TestLive ./server/internal/providers/xweb/ with XWEB_COOKIES_FILE: prints counts and handles
func TestLive(t *testing.T) {
	raw, err := os.ReadFile(os.Getenv("XWEB_COOKIES_FILE"))
	if err != nil {
		t.Skip("no cookies file")
	}
	file, err := netscape.Parse(string(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c, err := New(Config{Cookies: file}, httpx.Client(nil))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := probe(context.Background(), c); err != nil {
		t.Fatalf("probe: %v", err)
	}
	s := c.session
	if s == nil {
		s, _ = c.current(context.Background())
	}
	t.Logf("session: query id %d chars, %d features bytes", len(s.queryID), len(s.features))
	for _, latest := range []bool{false, true} {
		posts, err := c.SearchPosts(context.Background(), "spacex", latest, 5)
		if err != nil {
			t.Fatalf("search latest=%v: %v", latest, err)
		}
		t.Logf("latest=%v: %d posts", latest, len(posts))
		for _, p := range posts {
			t.Logf("  %s likes=%d views=%d text=%d chars quote=%v note=%v time=%s", p.Handle, p.Likes, p.Views, len(p.Text), p.Quote != nil, p.Note != "", p.Time)
		}
	}
}
