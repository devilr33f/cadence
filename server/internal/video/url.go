package video

import (
	"net/url"
	"strconv"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/providers/ytmusic"
)

// MaxURLBytes bounds a pasted link
const MaxURLBytes = 2048

var youtubeHosts = map[string]bool{
	"youtube.com": true, "www.youtube.com": true, "m.youtube.com": true, "music.youtube.com": true,
	"youtube-nocookie.com": true, "www.youtube-nocookie.com": true,
}

// ParseURL reads a youtube.com or youtu.be link (watch, shorts, live, embed) as its video id and the start its t=
// or start= names ("90", "1m30s", "1h2m3s"); false for anything else. The link is never logged.
func ParseURL(raw string) (videoID string, startMS int64, ok bool) {
	if len(raw) > MaxURLBytes {
		return "", 0, false
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return "", 0, false
	}
	host := strings.ToLower(u.Hostname())
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	switch {
	case host == "youtu.be" || host == "www.youtu.be":
		if len(parts) == 1 {
			videoID = parts[0]
		}
	case youtubeHosts[host]:
		switch {
		case len(parts) == 1 && parts[0] == "watch":
			videoID = u.Query().Get("v")
		case len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "live" || parts[0] == "embed" || parts[0] == "v"):
			videoID = parts[1]
		}
	}
	if !ytmusic.ValidVideoID(videoID) {
		return "", 0, false
	}
	q := u.Query()
	t := q.Get("t")
	if t == "" {
		t = q.Get("start")
	}
	if t == "" && strings.HasPrefix(u.Fragment, "t=") {
		t = u.Fragment[2:]
	}
	return videoID, parseOffset(t) * 1000, true
}

// parseOffset reads "90", "90s", "1m30s" or "1h2m3s" as seconds, 0 for anything else
func parseOffset(s string) int64 {
	if s == "" {
		return 0
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n >= 0 && n < 1e6 {
		return n
	}
	var total, n int64
	digits := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			n = n*10 + int64(r-'0')
			digits = true
			if n > 1e6 {
				return 0
			}
		case digits && (r == 'h' || r == 'm' || r == 's'):
			total += n * map[rune]int64{'h': 3600, 'm': 60, 's': 1}[r]
			n, digits = 0, false
		default:
			return 0
		}
	}
	if digits {
		return 0
	}
	return total
}

// IsMusicURL is a music.youtube.com link: a song, which a screen plays as audio by default
func IsMusicURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && strings.EqualFold(u.Hostname(), "music.youtube.com")
}
