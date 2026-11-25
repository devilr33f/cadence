package rpi

import (
	"log/slog"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/cec"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
)

// the tv's remote as hdmi-cec carries it (/dev/cec0, device/internal/cec): its keys arrive as User Control
// Pressed and Released. Volume keys are the board's presses (rule 7: only a press moves the level); every
// other key is the screen's.
const (
	opUserControlPressed  = 0x44
	opUserControlReleased = 0x45
)

var cecKeys = map[byte]screenv1.Key{
	0x00: screenv1.Key_KEY_OK, 0x01: screenv1.Key_KEY_UP, 0x02: screenv1.Key_KEY_DOWN, 0x03: screenv1.Key_KEY_LEFT,
	0x04: screenv1.Key_KEY_RIGHT, 0x09: screenv1.Key_KEY_MENU, 0x0d: screenv1.Key_KEY_BACK,
	0x41: screenv1.Key_KEY_VOLUME_UP, 0x42: screenv1.Key_KEY_VOLUME_DOWN, 0x43: screenv1.Key_KEY_VOLUME_MUTE,
	0x44: screenv1.Key_KEY_PLAY, 0x45: screenv1.Key_KEY_STOP, 0x46: screenv1.Key_KEY_PAUSE,
	0x48: screenv1.Key_KEY_REWIND, 0x49: screenv1.Key_KEY_FAST_FORWARD, 0x4b: screenv1.Key_KEY_NEXT,
	0x4c: screenv1.Key_KEY_PREVIOUS, 0x61: screenv1.Key_KEY_PLAY_PAUSE, 0x6b: screenv1.Key_KEY_POWER,
}

var cecVolume = map[screenv1.Key]input.Kind{
	screenv1.Key_KEY_VOLUME_UP: input.VolumeUp, screenv1.Key_KEY_VOLUME_DOWN: input.VolumeDown,
}

// cecDecoder turns cec frames into keys: a press names its key, a release the last key pressed
type cecDecoder struct {
	last screenv1.Key
}

// frame is a received cec frame; ok false for anything but a known key
func (d *cecDecoder) frame(f cec.Frame, now time.Time) (*screenv1.InputEvent, bool) {
	switch f.Opcode {
	case opUserControlPressed:
		if len(f.Ops) < 1 {
			return nil, false
		}
		k, ok := cecKeys[f.Ops[0]]
		if !ok {
			return nil, false
		}
		d.last = k
		return &screenv1.InputEvent{Key: k, Action: screenv1.KeyAction_KEY_ACTION_DOWN, Source: screenv1.InputSource_INPUT_SOURCE_CEC, AtMs: now.UnixMilli()}, true
	case opUserControlReleased:
		if d.last == screenv1.Key_KEY_UNSPECIFIED {
			return nil, false
		}
		k := d.last
		d.last = screenv1.Key_KEY_UNSPECIFIED
		return &screenv1.InputEvent{Key: k, Action: screenv1.KeyAction_KEY_ACTION_UP, Source: screenv1.InputSource_INPUT_SOURCE_CEC, AtMs: now.UnixMilli()}, true
	}
	return nil, false
}

// route sends a volume key to the board's buttons, any other to keys
func route(e *screenv1.InputEvent, buttons chan<- input.ButtonEvent, keys func(*screenv1.InputEvent)) {
	if kind, ok := cecVolume[e.GetKey()]; ok {
		select {
		case buttons <- input.ButtonEvent{Kind: kind, Pressed: e.GetAction() == screenv1.KeyAction_KEY_ACTION_DOWN, At: time.UnixMilli(e.GetAtMs())}:
		default:
		}
		return
	}
	if keys != nil {
		keys(e)
	}
}

// cecRemote is the wire's keys as one Buttons source: the display.cec controller (tvboard's Extra) owns the
// wire itself; this only takes its frames
type cecRemote struct {
	wire    cec.Bus
	log     *slog.Logger
	buttons chan input.ButtonEvent
}

// newCECRemote subscribes before the wire ever starts; keys takes the screen's keys once the board runs
func newCECRemote(wire cec.Bus, log *slog.Logger, keys func(*screenv1.InputEvent)) *cecRemote {
	r := &cecRemote{wire: wire, log: log, buttons: make(chan input.ButtonEvent, 8)}
	var dec cecDecoder
	wire.Subscribe(func(f cec.Frame) {
		if e, ok := dec.frame(f, time.Now()); ok {
			route(e, r.buttons, keys)
		}
	})
	return r
}

func (r *cecRemote) Events() <-chan input.ButtonEvent { return r.buttons }
