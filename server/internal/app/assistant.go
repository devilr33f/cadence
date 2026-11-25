package app

import (
	"context"
	"errors"

	"git.shino.boo/devilreef/cadence/server/internal/voice"
)

// announceCode is the dashboard assistant's view of announcements: a typed
// turn's reply played on its device, answered by the code the announce route
// would give ("" once it plays).
func announceCode(c *voice.Coordinator) func(ctx context.Context, deviceID, text, language string) string {
	return func(ctx context.Context, deviceID, text, language string) string {
		_, err := c.Announce(ctx, deviceID, voice.Announcement{Text: text, Language: language})
		switch {
		case err == nil:
			return ""
		case errors.Is(err, voice.ErrOffline):
			return "device_offline"
		case errors.Is(err, voice.ErrNoAudio):
			return "no_audio"
		case errors.Is(err, voice.ErrBusy):
			return "busy"
		case errors.Is(err, voice.ErrTTSUnconfigured):
			return "tts_unconfigured"
		case errors.Is(err, voice.ErrSuperseded):
			return "superseded"
		case errors.Is(err, voice.ErrTTSFailed):
			return "tts_failed"
		case errors.Is(err, voice.ErrTTSTimeout):
			return "tts_timeout"
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return "canceled"
		}
		return "internal"
	}
}
