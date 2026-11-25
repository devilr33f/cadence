package tools

import (
	"context"
	"regexp"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

var resultID = regexp.MustCompile(`^r[0-9]{1,9}$`)

// Song is the song ref names for the device: a music_search result id of a song it answered, or the first song a
// search for ref finds in the music setting's instances (an alarm's music, «разбуди под Queen»). false for a list's
// result, an id it does not hold, or a search that found nothing.
func (k *MusicKit) Song(ctx context.Context, deviceID, ref string) (providers.Track, bool) {
	if resultID.MatchString(ref) {
		f, ok := k.r.get(deviceID, ref)
		if !ok || f.coll != nil {
			return providers.Track{}, false
		}
		return f.track, true
	}
	if k.d.Settings == nil {
		return providers.Track{}, false
	}
	v, err := k.d.Settings(ctx, deviceID)
	if err != nil || !musicAvailable(v) {
		return providers.Track{}, false
	}
	got, _ := searchAll(ctx, k.d, v.Music, "song", ref, 1, "")
	for _, f := range got {
		if f.coll == nil {
			return f.track, true
		}
	}
	return providers.Track{}, false
}
