package vkcapsula

import (
	"git.shino.boo/devilreef/cadence/device/internal/evdev"
	"git.shino.boo/devilreef/cadence/device/internal/input"
)

const (
	keyVolumeDown = 114
	keyVolumeUp   = 115
	keyPlayPause  = 164
	keyMicMute    = 248
)

// csk05t's centre touch (704) and aml_vkeypad's power key (116) are unmarked or unverified, so unmapped. the mic
// key is a button, not a switch: a toggle the board persists
func buttons(root string, start bool, saved func(on bool)) evdev.Layout {
	return evdev.Layout{
		SysClass: under(root, "/sys/class/input"), Nodes: under(root, "/dev/input"),
		Devices:    []evdev.Device{{Role: "keys", Name: "csk05t"}},
		Keys:       map[uint16]input.Kind{keyPlayPause: input.Action, keyVolumeUp: input.VolumeUp, keyVolumeDown: input.VolumeDown},
		MuteToggle: keyMicMute, MuteToggleStart: start, MuteToggled: saved,
	}
}
