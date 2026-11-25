package vkcapsula

import (
	"os"
	"path/filepath"

	"git.shino.boo/devilreef/cadence/device/internal/evdev"
)

func (b *Board) loadMute() bool { return evdev.LoadToggle(b.paths.MuteState) }

func (b *Board) saveMute(on bool) {
	err := os.MkdirAll(filepath.Dir(b.paths.MuteState), 0o750)
	if err == nil {
		err = evdev.SaveToggle(b.paths.MuteState, on)
	}
	if err != nil {
		b.log.Warn("mute_state_save_failed", "muted", on)
	}
}
