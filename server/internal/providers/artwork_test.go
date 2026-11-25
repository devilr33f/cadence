package providers_test

import (
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// a track's letterboxed video thumbnail becomes its bare frame, which a square box centre-crops; square covers stay
func TestMusicArtwork(t *testing.T) {
	for in, want := range map[string]string{
		"https://i.ytimg.com/vi/CSvFpBOe8eY/hqdefault.jpg":       "https://i.ytimg.com/vi/CSvFpBOe8eY/mqdefault.jpg",
		"https://i.ytimg.com/vi/CSvFpBOe8eY/sddefault.jpg":       "https://i.ytimg.com/vi/CSvFpBOe8eY/mqdefault.jpg",
		"https://i.ytimg.com/vi_webp/CSvFpBOe8eY/0.webp":         "https://i.ytimg.com/vi_webp/CSvFpBOe8eY/mqdefault.webp",
		"https://i.ytimg.com/vi/CSvFpBOe8eY/maxresdefault.jpg":   "https://i.ytimg.com/vi/CSvFpBOe8eY/maxresdefault.jpg",
		"https://i.ytimg.com/vi/CSvFpBOe8eY/hqdefault.jpg?sqp=x": "https://i.ytimg.com/vi/CSvFpBOe8eY/hqdefault.jpg?sqp=x",
		"https://lh3.googleusercontent.com/abc=w544-h544-l90-rj": "https://lh3.googleusercontent.com/abc=w544-h544-l90-rj",
		"http://i.ytimg.com/vi/CSvFpBOe8eY/hqdefault.jpg":        "http://i.ytimg.com/vi/CSvFpBOe8eY/hqdefault.jpg",
		"": "",
	} {
		if got := providers.MusicArtwork(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
