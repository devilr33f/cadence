package music

import (
	"context"
	"strings"
	"unicode"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const matchCandidates = 5

// matchOnYouTube finds a song another catalog (Spotify) found on YouTube
// Music, which the player plays: the same title, within a few seconds of its
// length. ok is false when nothing there fits.
func matchOnYouTube(ctx context.Context, yt providers.Music, t providers.Track) (string, bool, error) {
	query := strings.TrimSpace(t.Artist + " " + t.Title)
	found, err := yt.SearchMusic(ctx, query, matchCandidates)
	if err != nil {
		return "", false, err
	}
	want := normTitle(t.Title)
	var near string
	for _, c := range found {
		if c.VideoID == "" || !closeLength(t.DurationS, c.DurationS) {
			continue
		}
		if want != "" && strings.Contains(normTitle(c.Title), want) {
			return c.VideoID, true, nil
		}
		if near == "" {
			near = c.VideoID
		}
	}
	return near, near != "", nil
}

// closeLength allows 4 s or 4 %, whichever is more; an unknown length fits
func closeLength(want, got int) bool {
	if want <= 0 || got <= 0 {
		return true
	}
	d := want - got
	if d < 0 {
		d = -d
	}
	return d <= max(4, want/25)
}

// normTitle drops what versions add ("Song - Remastered 2004", "Song (Live)")
// and keeps letters and digits, lowercased
func normTitle(s string) string {
	if i := strings.Index(s, " - "); i > 0 {
		s = s[:i]
	}
	if i := strings.IndexAny(s, "(["); i > 0 {
		s = s[:i]
	}
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
