package app

import (
	"context"

	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/media"
)

// mediaPlaying says whether the device's media session plays: a stop request
// pauses it first (M6a-D6).
func mediaPlaying(sessions *media.Store) func(deviceID string) bool {
	return func(id string) bool {
		s, ok := sessions.Get(id)
		return ok && s.State == "playing"
	}
}

// pauseMedia pauses the device's media session through its media.player capability.
func pauseMedia(cmds *commands.Service) func(ctx context.Context, deviceID string) error {
	return func(ctx context.Context, id string) error {
		_, err := cmds.Send(ctx, id, "media.player", "pause", []byte("{}"))
		return err
	}
}
