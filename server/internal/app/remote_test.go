package app

import (
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/images"
	"git.shino.boo/devilreef/cadence/server/internal/media"
)

// a remote card's cover reaches the screen as an img: ref alone: the server's own proxied image by its id, a third
// party's through the proxy, an upload only while the server holds it
func TestRemoteArtIsAnImageRef(t *testing.T) {
	p := images.New(images.Config{})
	art := remoteArt("https://cadence.example", p, media.NewArtwork())
	own := strings.Repeat("a", 24)
	if got := art("tv", media.MediaSession{Metadata: media.MediaMetadata{ArtworkURL: "https://cadence.example/v2/images/" + own}}); got != "img:"+own {
		t.Fatalf("own %q", got)
	}
	got := art("tv", media.MediaSession{Metadata: media.MediaMetadata{ArtworkURL: "https://i.ytimg.com/vi/x/hq.jpg"}})
	if id, ok := strings.CutPrefix(got, "img:"); !ok || !images.IDPattern.MatchString(id) {
		t.Fatalf("third party %q", got)
	}
	for _, md := range []media.MediaMetadata{
		{ArtworkURL: "http://plain.example/a.jpg"},
		{ArtworkHash: strings.Repeat("0", 64)},
		{},
	} {
		if got := art("tv", media.MediaSession{Metadata: md}); got != "" {
			t.Fatalf("%+v: %q", md, got)
		}
	}
}
