package pickle

import (
	"os"

	"git.shino.boo/devilreef/cadence/device/internal/evdev"
	"git.shino.boo/devilreef/cadence/device/internal/input"
)

// linux input event codes (<linux/input-event-codes.h>)
const (
	keyVolumeDown   = 114
	keyVolumeUp     = 115
	keyVoiceCommand = 0x246
	swMuteDevice    = 0x0e
)

// buttons are found by the name their driver gives them: the touch keys (action, volume up and down), and the gpio
// keys (the mute button, which does nothing, and the mute switch it latches)
func buttons(root string) evdev.Layout {
	return evdev.Layout{
		SysClass:   under(root, "/sys/class/input"),
		Nodes:      under(root, "/dev/input"),
		Devices:    []evdev.Device{{Role: "keys", Name: "aw933xx"}, {Role: "mute", Name: "mute", Switch: true}},
		Keys:       map[uint16]input.Kind{keyVoiceCommand: input.Action, keyVolumeUp: input.VolumeUp, keyVolumeDown: input.VolumeDown},
		MuteSwitch: swMuteDevice,
	}
}

func readSwitch(f *os.File) (on, ok bool) { return evdev.Switch(f, swMuteDevice) }
