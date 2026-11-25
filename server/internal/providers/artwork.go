package providers

import (
	"net/url"
	"regexp"
)

// a youtube video's own thumbnails: hqdefault, sddefault and the numbered ones are 4:3 with black bars above and
// below the 16:9 frame; mqdefault is the frame alone
var ytThumbRe = regexp.MustCompile(`^/(vi|vi_webp)/([A-Za-z0-9_-]{6,20})/(?:default|hqdefault|sddefault|[0-3]|hq[0-3]|sd[0-3])\.(jpg|webp)$`)

// MusicArtwork is a music item's artwork as a square box shows it, cover-fit to the centre: a youtube video's
// letterboxed thumbnail (a track that has no album cover, only its video's) becomes the same frame without its bars,
// so the centre crop is picture, not black; any other url (YouTube Music's square covers) is left as it is.
func MusicArtwork(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "i.ytimg.com" || u.RawQuery != "" {
		return raw
	}
	m := ytThumbRe.FindStringSubmatch(u.Path)
	if m == nil {
		return raw
	}
	return "https://i.ytimg.com/" + m[1] + "/" + m[2] + "/mqdefault." + m[3]
}
