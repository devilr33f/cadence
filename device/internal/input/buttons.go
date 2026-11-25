package input

import "time"

// Kind is what a button event is about. The board maps its own key codes onto these: core never sees a code.
type Kind uint8

const (
	Action Kind = iota + 1
	VolumeUp
	VolumeDown
	// the mute switch's positions
	MuteOn
	MuteOff
)

func (k Kind) String() string {
	switch k {
	case Action:
		return "action"
	case VolumeUp:
		return "volume_up"
	case VolumeDown:
		return "volume_down"
	case MuteOn:
		return "mute_on"
	case MuteOff:
		return "mute_off"
	}
	return "unknown"
}

// ButtonEvent is one edge of a key, or a position of the mute switch. A key acts on its press; its release changes
// nothing. A switch position acts whatever Pressed says, and the board reports the position it starts in first.
type ButtonEvent struct {
	Kind    Kind
	Pressed bool
	At      time.Time
}

// Buttons is the board's input port.
type Buttons interface {
	Events() <-chan ButtonEvent
}
