package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	"git.shino.boo/devilreef/cadence/server/internal/video"
)

// a tv's audiod has no url source (CADENCE_SCREEN), yet the android app up to aa3b4aed reports media.url and answers
// every url play unsupported (the bedroom tv's music_play): every screen plays songs on its own player, a speaker
// through media.url
func TestSongsPlayOnEveryScreensOwnPlayer(t *testing.T) {
	reports := map[string]map[string]bool{
		// the deployed tv app: audiod's media.url beside the screen's player
		"tv-old": {video.CapURL: true, video.CapVideo: true},
		// sim-tv and the fixed tv app: no media.url
		"tv":      {video.CapVideo: true},
		"speaker": {video.CapURL: true},
	}
	videos := video.New(video.Deps{
		State: func(_ context.Context, dev, c string) (json.RawMessage, bool) {
			if c == capschema.DisplayScreenID && dev != "speaker" {
				return json.RawMessage(`{"screen":true,"maxHeight":1080}`), true
			}
			return nil, false
		},
		Reports: func(_ context.Context, dev, c, _ string) error {
			if reports[dev][c] {
				return nil
			}
			return errors.New("unsupported")
		},
	})
	on := songsOnScreen(videos)
	for dev, want := range map[string]bool{"tv-old": true, "tv": true, "speaker": false} {
		if got := on(context.Background(), dev); got != want {
			t.Errorf("%s: on screen %v, want %v", dev, got, want)
		}
	}
}
