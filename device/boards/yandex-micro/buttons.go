package micro

import (
	"git.shino.boo/devilreef/cadence/device/internal/evdev"
	"git.shino.boo/devilreef/cadence/device/internal/input"
)

const (
	keyVolumeDown   = 114
	keyVolumeUp     = 115
	keyVoiceCommand = 0x246
)

// sar0_pad0 carries action and volume. gpio-keys-filter's KEY_MUTE is a button,
// not a latched switch, so it is deliberately unmapped in the minimal profile.
func buttons(root string) evdev.Layout {
	return evdev.Layout{
		SysClass: under(root, "/sys/class/input"), Nodes: under(root, "/dev/input"),
		Devices: []evdev.Device{{Role: "keys", Name: "sar0_pad0"}},
		Keys:    map[uint16]input.Kind{keyVoiceCommand: input.Action, keyVolumeUp: input.VolumeUp, keyVolumeDown: input.VolumeDown},
	}
}
