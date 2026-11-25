package ambient

import (
	"slices"
	"strings"
)

// Profile is what a screen plays, from display.screen's state (maxHeight, codecs) and config (ambient.aerialQuality).
type Profile struct {
	MaxHeight int
	Codecs    []string
	// "4k" asks for 4K SDR where the screen takes it; anything else is 1080
	Quality string
}

func (p Profile) hevc() bool {
	return slices.ContainsFunc(p.Codecs, func(c string) bool {
		c = strings.ToLower(c)
		return c == "hevc" || c == "h265"
	})
}

// choose picks a clip's rendition: 4K SDR HEVC only when asked for and the screen takes 2160p HEVC, 1080p SDR HEVC
// where it takes HEVC (the stick), else 1080p H.264. HDR is never chosen in v1. A missing file falls down the list.
func (p Profile) choose(c Clip) (Rendition, string, bool) {
	var order []Rendition
	if p.hevc() {
		if strings.EqualFold(p.Quality, "4k") && p.MaxHeight >= 2160 {
			order = append(order, SDR4K)
		}
		order = append(order, SDR1080)
	}
	order = append(order, H264_1080)
	for _, r := range order {
		if u, ok := c.URLs[r]; ok {
			return r, u, true
		}
	}
	return "", "", false
}
