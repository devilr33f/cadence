package midi

import (
	"os"

	"git.shino.boo/devilreef/cadence/device/internal/evdev"
	"git.shino.boo/devilreef/cadence/device/internal/input"
)

// linux input event codes (<linux/input-event-codes.h>), the same as the pickle's
const (
	keyVolumeDown   = 114
	keyVolumeUp     = 115
	keyVoiceCommand = 0x246
	swMuteDevice    = 0x0e
)

// buttons are found by the name their driver gives them: the touch keys (action, volume up and down; its two
// program keys are left unmapped), and the gpio keys (the mute button, which does nothing, and the mute switch it
// latches)
func buttons(root string) evdev.Layout {
	return evdev.Layout{
		SysClass:   under(root, "/sys/class/input"),
		Nodes:      under(root, "/dev/input"),
		Devices:    []evdev.Device{{Role: "keys", Name: "midi_touch"}, {Role: "mute", Name: "gpio-keys", Switch: true}},
		Keys:       map[uint16]input.Kind{keyVoiceCommand: input.Action, keyVolumeUp: input.VolumeUp, keyVolumeDown: input.VolumeDown},
		MuteSwitch: swMuteDevice,
	}
}

func readSwitch(f *os.File) (on, ok bool) { return evdev.Switch(f, swMuteDevice) }
