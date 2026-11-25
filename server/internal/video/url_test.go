package video_test

import (
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/video"
)

func TestParseURLReadsYouTubeLinks(t *testing.T) {
	cases := []struct {
		in    string
		id    string
		start int64
	}{
		{"https://www.youtube.com/watch?v=5qZQEq_C3vc", "5qZQEq_C3vc", 0},
		{"https://youtube.com/watch?v=5qZQEq_C3vc&t=90", "5qZQEq_C3vc", 90_000},
		{"https://m.youtube.com/watch?feature=share&v=5qZQEq_C3vc&t=1m30s", "5qZQEq_C3vc", 90_000},
		{"https://music.youtube.com/watch?v=5qZQEq_C3vc", "5qZQEq_C3vc", 0},
		{"https://youtu.be/5qZQEq_C3vc?t=1h2m3s", "5qZQEq_C3vc", 3_723_000},
		{"https://www.youtube.com/shorts/5qZQEq_C3vc", "5qZQEq_C3vc", 0},
		{"https://www.youtube.com/live/5qZQEq_C3vc?si=x", "5qZQEq_C3vc", 0},
		{"https://www.youtube-nocookie.com/embed/5qZQEq_C3vc?start=12", "5qZQEq_C3vc", 12_000},
		{"https://www.youtube.com/watch?v=5qZQEq_C3vc#t=30s", "5qZQEq_C3vc", 30_000},
	}
	for _, c := range cases {
		id, start, ok := video.ParseURL(c.in)
		if !ok || id != c.id || start != c.start {
			t.Errorf("%s: %q %d %v", c.in, id, start, ok)
		}
	}
	for _, bad := range []string{
		"https://evil.example/watch?v=5qZQEq_C3vc", "https://youtube.com.evil.example/watch?v=5qZQEq_C3vc",
		"ftp://youtube.com/watch?v=5qZQEq_C3vc", "https://www.youtube.com/watch?v=short", "https://youtu.be/",
		"https://www.youtube.com/playlist?list=PL1", "https://user@youtube.com/watch?v=5qZQEq_C3vc", "not a url",
		"https://youtu.be/5qZQEq_C3vc/extra",
	} {
		if _, _, ok := video.ParseURL(bad); ok {
			t.Errorf("%s accepted", bad)
		}
	}
	if _, start, _ := video.ParseURL("https://youtu.be/5qZQEq_C3vc?t=1x"); start != 0 {
		t.Errorf("bad offset %d", start)
	}
}
