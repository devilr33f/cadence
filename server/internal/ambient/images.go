package ambient

import (
	"context"
	"errors"
)

// Image is one still background: an https url the screen fetches (the owner's) or the image proxy's (artwork), with
// a caption in the screen's language or none.
type Image struct {
	URL     string `json:"url"`
	Caption string `json:"caption,omitempty"`
}

// OwnerImage is an entry of the owner's list.
type OwnerImage struct {
	URL     string `json:"url"`
	Caption string `json:"caption,omitempty"`
}

var errImageURL = errors.New("ambient: an owner image needs an https url")

// CheckOwnerImages refuses an entry the screen could not fetch safely.
func CheckOwnerImages(list []OwnerImage) error {
	for _, im := range list {
		if !httpsURL(im.URL) {
			return errImageURL
		}
	}
	return nil
}

// Artwork gives backdrops from the catalog's libraries for a device, already as the image proxy's urls. Stream D's
// catalog implements it; until then NoArtwork has none.
type Artwork interface {
	Backdrops(ctx context.Context, deviceID string, limit int) ([]Image, error)
}

// NoArtwork is an Artwork with no backdrops.
type NoArtwork struct{}

// Backdrops returns none.
func (NoArtwork) Backdrops(context.Context, string, int) ([]Image, error) { return nil, nil }
