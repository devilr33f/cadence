package cards

import (
	"context"
)

// VideoResults is what the video cards read: the video tools' remembered results, and the
// play a tile's press makes on the screen that pressed it
type VideoResults interface {
	// VideoResult is a video_search result by the id the model saw
	VideoResult(deviceID, id string) (VideoFound, bool)
	// VideoPlay plays a result now on the device that pressed its tile
	VideoPlay(ctx context.Context, deviceID, id string) error
}

// VideoFound is a video result as a tile shows it: third parties' text, memory-only
type VideoFound struct {
	Title, Channel string
	// ArtworkURL is its still, which the image proxy fetches; "" for none
	ArtworkURL string
	DurationS  int
	Live       bool
}
