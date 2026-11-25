// Package cec is hdmi-cec as a screen board reaches it: the wire's frames, the devices a scan finds on it
// and the flows that move them (one touch play, standby, volume keys, system audio), all behind a Bus seam
// a board fills in (linux /dev/cec, the android app's bridge, the sim's fake).
package cec

import (
	"fmt"
	"strings"
)

// the wire's opcodes (hdmi-cec 1.4): the ones the flows carry
const (
	OpFeatureAbort              = 0x00
	OpImageViewOn               = 0x04
	OpTextViewOn                = 0x0d
	OpStandby                   = 0x36
	OpUserControlPressed        = 0x44
	OpUserControlReleased       = 0x45
	OpGiveOSDName               = 0x46
	OpSetOSDName                = 0x47
	OpSystemAudioModeRequest    = 0x70
	OpGiveAudioStatus           = OpSystemAudioModeRequest + 1
	OpSetSystemAudioMode        = OpSystemAudioModeRequest + 2
	OpReportAudioStatus         = 0x7a
	OpGiveSystemAudioModeStatus = 0x7d
	OpReportSystemAudioMode     = 0x7e
	OpActiveSource              = 0x82
	OpGivePhysicalAddress       = 0x83
	OpReportPhysicalAddress     = 0x84
	OpDeviceVendorID            = 0x87
	OpGiveDeviceVendorID        = 0x8c
	OpGiveDevicePowerStatus     = 0x8f
	OpReportPowerStatus         = 0x90
)

// User Control Pressed codes (cec table 27) the flows send; the screen's keys keep their own table
const (
	UCVolumeUp   = 0x41
	UCVolumeDown = 0x42
	UCMute       = 0x43
)

// Feature Abort reasons
const (
	AbortUnrecognized = 0x00
	AbortNotInMode    = 0x01
	AbortNoSource     = 0x02
	AbortInvalid      = 0x03
	AbortRefused      = 0x04
)

// Frame is one cec message: the header's initiator and destination logical addresses (0-15, 15 broadcast),
// the opcode and its operands. A polling message carries no opcode at all.
type Frame struct {
	Init    byte
	Dest    byte
	Opcode  byte
	Ops     []byte
	Polling bool
}

// Parse takes a message as it rides /dev/cec's struct cec_msg: the header byte, then the opcode, then the
// operands.
func Parse(msg []byte) (Frame, error) {
	if len(msg) < 1 {
		return Frame{}, fmt.Errorf("cec: empty message")
	}
	f := Frame{Init: msg[0] >> 4, Dest: msg[0] & 0xf}
	if len(msg) == 1 {
		f.Polling = true
		return f, nil
	}
	f.Opcode = msg[1]
	f.Ops = append([]byte(nil), msg[2:]...)
	return f, nil
}

// Bytes is the message as the wire takes it.
func (f Frame) Bytes() []byte {
	if f.Polling {
		return []byte{f.Init<<4 | f.Dest&0xf}
	}
	out := make([]byte, 0, 2+len(f.Ops))
	out = append(out, f.Init<<4|f.Dest&0xf, f.Opcode)
	return append(out, f.Ops...)
}

// abort names the opcode a Feature Abort refused, false for any other frame
func (f Frame) abort() (byte, bool) {
	if f.Opcode != OpFeatureAbort || len(f.Ops) < 2 {
		return 0, false
	}
	return f.Ops[0], true
}

// device types as Report Physical Address's operand and the state name them
const (
	typeTV     = 0x0
	typeRec    = 0x1
	typeRes    = 0x2
	typeTuner  = 0x3
	typePlay   = 0x4
	typeAudio  = 0x5
	typeSwitch = 0x6
	typeProc   = 0x7
)

// typeName maps a Report Physical Address device type operand to the state's name
func typeName(t byte) string {
	switch t {
	case typeTV:
		return "tv"
	case typeRec:
		return "recording"
	case typeRes:
		return "reserved"
	case typeTuner:
		return "tuner"
	case typePlay:
		return "playback"
	case typeAudio:
		return "audio_system"
	case typeSwitch:
		return "switch"
	case typeProc:
		return "processor"
	}
	return "free"
}

// typeByAddress is the type a device that never answers Report Physical Address is known by: its logical
// address's own (cec 1.4, logical addresses)
func typeByAddress(la byte) string {
	switch la {
	case 0:
		return "tv"
	case 1, 2, 9:
		return "recording"
	case 3, 6, 7, 10:
		return "tuner"
	case 4, 8, 11:
		return "playback"
	case 5:
		return "audio_system"
	}
	return "free"
}

// FormatPhysical is the 16-bit physical address as its dotted form: 0x1000 -> "1.0.0.0", 0x0000 -> "0.0.0.0".
// The spec drops trailing zero nibbles ("2"), the wire keeps them; we show them all, as cec-ctl does.
func FormatPhysical(v uint16) string {
	return fmt.Sprintf("%d.%d.%d.%d", v>>12&0xf, v>>8&0xf, v>>4&0xf, v&0xf)
}

// ParsePhysical is FormatPhysical's inverse; ok false for anything but four nibbles
func ParsePhysical(s string) (uint16, bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return 0, false
	}
	var v uint16
	for _, p := range parts {
		if len(p) != 1 || p[0] < '0' || p[0] > '9' {
			return 0, false
		}
		v = v<<4 | uint16(p[0]-'0')
	}
	return v, true
}
