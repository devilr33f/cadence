package history

import (
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/media"
)

// a phone streaming over bluetooth plays tracks, and its cover is the device's upload, never a url
func TestBluetoothIsATrackWithAnUploadedCover(t *testing.T) {
	if got := historyKind("", "bluetooth"); got != "track" {
		t.Fatalf("kind %q", got)
	}
	sess := media.MediaSession{SessionID: "bt", Source: "bluetooth", State: "playing",
		Metadata: media.MediaMetadata{Title: "Song", ArtworkURL: "https://example.com/a.jpg", ArtworkHash: "abc"}}
	e, _ := historyHistoryEntry("d1", sess, Attribution{})
	if e.ArtworkURL != "" || e.ArtworkHash != "abc" || e.Kind != "track" {
		t.Fatalf("%+v", e)
	}
}
