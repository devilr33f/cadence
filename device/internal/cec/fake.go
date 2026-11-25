package cec

import (
	"context"
	"encoding/binary"
	"sync"
)

// fakeDevice is one device on the fake wire, in the domain's terms
type fakeDevice struct {
	typ    string
	phys   uint16
	vendor uint32
	name   string
	power  string
	audio  *AudioStatus
	sam    bool // an avr's system audio mode
}

// Fake is a wire for the sim and the tests: devices that answer what the scan asks and take what the flows
// send, as the wire's devices do. NewFake's tv answers audio status (many do) and the avr pushes its own
// Report Audio Status after its volume moves, as one in system audio mode does.
type Fake struct {
	mu      sync.Mutex
	self    Adapter
	devices map[byte]*fakeDevice
	active  string
	started bool
	sinks   sinks
}

// NewFake is the wire with a tv at 0 and an avr at 5, ourselves a playback device at 4 on 2.0.0.0.
func NewFake() *Fake {
	return &Fake{
		self: Adapter{Present: true, PhysicalAddress: "2.0.0.0", LogicalAddress: 4, DeviceType: "playback"},
		devices: map[byte]*fakeDevice{
			0: {typ: "tv", phys: 0x0000, vendor: 0x0000f0, name: "SIM TV", power: "on",
				audio: &AudioStatus{Volume: 40, Muted: false}},
			5: {typ: "audio_system", phys: 0x1000, vendor: 0x000925, name: "SIM AVR", power: "on",
				audio: &AudioStatus{Volume: 25, Muted: false}},
		},
	}
}

// Info is our own side.
func (f *Fake) Info() Adapter {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.self
}

// Subscribe adds a sink for the frames the wire carries to it.
func (f *Fake) Subscribe(sink func(Frame)) { f.sinks.add(sink) }

// Start runs until ctx ends; the fake wire carries nothing on its own. Starting twice (a board beside the
// controller) is one run.
func (f *Fake) Start(ctx context.Context, wg *sync.WaitGroup) {
	f.mu.Lock()
	started := f.started
	f.started = true
	f.mu.Unlock()
	if started {
		return
	}
	wg.Go(func() { <-ctx.Done() })
}

// device is the destination's copy, false when no device answers there
func (f *Fake) device(la byte) (fakeDevice, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devices[la]
	if !ok {
		return fakeDevice{}, false
	}
	return *d, true
}

// Send answers as the destination would: a poll by its presence, a question by its state, a command by
// taking it. ok false is the wire's nack or a reply that never came. A broadcast reaches the wire itself,
// so it is never a nack.
func (f *Fake) Send(_ context.Context, msg Frame, want byte) (Frame, bool) {
	d, ok := f.device(msg.Dest)
	if !ok && msg.Dest != 0xf {
		return Frame{}, false
	}
	if msg.Polling {
		return Frame{}, true
	}
	switch msg.Opcode {
	case OpGivePhysicalAddress:
		report := append(binary.BigEndian.AppendUint16(nil, d.phys), typeOperand(d.typ))
		return f.reply(msg.Dest, OpReportPhysicalAddress, report), true
	case OpGiveDeviceVendorID:
		// the wire's vendor id is 24 bits
		return f.reply(msg.Dest, OpDeviceVendorID, binary.BigEndian.AppendUint32(nil, d.vendor)[1:]), true
	case OpGiveOSDName:
		return f.reply(msg.Dest, OpSetOSDName, []byte(d.name)), true
	case OpGiveDevicePowerStatus:
		return f.reply(msg.Dest, OpReportPowerStatus, []byte{powerOperand(d.power)}), true
	case OpGiveAudioStatus:
		if d.audio == nil {
			return f.reply(msg.Dest, OpFeatureAbort, []byte{OpGiveAudioStatus, AbortNotInMode}), true
		}
		return f.reply(msg.Dest, OpReportAudioStatus, []byte{audioOperand(*d.audio)}), true
	case OpGiveSystemAudioModeStatus:
		v := byte(0)
		if d.sam {
			v = 1
		}
		return f.reply(msg.Dest, OpReportSystemAudioMode, []byte{v}), true
	case OpTextViewOn, OpImageViewOn:
		if d.typ == "tv" {
			f.setPower(msg.Dest, "on")
		}
	case OpStandby:
		f.setPower(msg.Dest, "standby")
	case OpUserControlPressed:
		f.press(msg, d)
	case OpSystemAudioModeRequest:
		f.setSAM(msg.Dest, len(msg.Ops) >= 2)
	case OpActiveSource:
		if len(msg.Ops) >= 2 {
			f.mu.Lock()
			f.active = FormatPhysical(uint16(msg.Ops[0])<<8 | uint16(msg.Ops[1]))
			f.mu.Unlock()
		}
	}
	if want == 0 {
		return Frame{}, true
	}
	// a command the destination cannot answer: the reply the controller waits for never comes
	return Frame{}, false
}

// reply is a frame back to us from la
func (f *Fake) reply(la byte, op byte, ops []byte) Frame {
	dest := byte(4)
	if self := f.self.LogicalAddress; self >= 0 && self <= 15 {
		dest = byte(self)
	}
	return Frame{Init: la, Dest: dest, Opcode: op, Ops: ops}
}

func (f *Fake) setPower(la byte, power string) {
	f.mu.Lock()
	if la == 0xf {
		for _, d := range f.devices {
			d.power = power
		}
	} else if d := f.devices[la]; d != nil {
		d.power = power
	}
	f.mu.Unlock()
}

// press takes a volume key as the destination's own remote would, and an avr whose volume moved says so
func (f *Fake) press(msg Frame, d fakeDevice) {
	if len(msg.Ops) < 1 || d.audio == nil {
		return
	}
	f.mu.Lock()
	dev := f.devices[msg.Dest]
	if dev == nil || dev.audio == nil {
		f.mu.Unlock()
		return
	}
	switch msg.Ops[0] {
	case UCVolumeUp:
		dev.audio.Volume = min(dev.audio.Volume+1, 100)
	case UCVolumeDown:
		dev.audio.Volume = max(dev.audio.Volume-1, 0)
	case UCMute:
		dev.audio.Muted = !dev.audio.Muted
	}
	audio, typ := *dev.audio, dev.typ
	f.mu.Unlock()
	if typ == "audio_system" {
		f.sinks.deliver(f.reply(msg.Dest, OpReportAudioStatus, []byte{audioOperand(audio)}))
	}
}

// setSAM takes a System Audio Mode Request and broadcasts the answer, as an avr does
func (f *Fake) setSAM(la byte, on bool) {
	f.mu.Lock()
	if d := f.devices[la]; d != nil && d.typ == "audio_system" {
		d.sam = on
	}
	f.mu.Unlock()
	v := byte(0)
	if on {
		v = 1
	}
	f.sinks.deliver(Frame{Init: la, Dest: 0xf, Opcode: OpSetSystemAudioMode, Ops: []byte{v}})
}

// powerOperand and audioOperand are the wire's encodings
func powerOperand(power string) byte {
	switch power {
	case "on":
		return 0x00
	case "standby":
		return 0x01
	case "to_on":
		return 0x02
	case "to_off":
		return 0x03
	}
	return 0x01
}

func audioOperand(a AudioStatus) byte {
	v := byte(a.Volume & 0x7f)
	if a.Muted {
		v |= 0x80
	}
	return v
}

// typeOperand is the wire's name for a device type
func typeOperand(typ string) byte {
	switch typ {
	case "tv":
		return typeTV
	case "recording":
		return typeRec
	case "reserved":
		return typeRes
	case "tuner":
		return typeTuner
	case "playback":
		return typePlay
	case "audio_system":
		return typeAudio
	case "switch":
		return typeSwitch
	case "processor":
		return typeProc
	}
	return typePlay
}

// DeviceSnapshot is one fake device as tests read it back.
type DeviceSnapshot struct {
	Type    string
	Power   string
	Audio   *AudioStatus
	SAM     bool
	Present bool
}

// Snapshot is the fake's own state for tests: a device by logical address.
func (f *Fake) Snapshot(la byte) DeviceSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devices[la]
	if !ok {
		return DeviceSnapshot{}
	}
	s := DeviceSnapshot{Type: d.typ, Power: d.power, SAM: d.sam, Present: true}
	if d.audio != nil {
		a := *d.audio
		s.Audio = &a
	}
	return s
}

// Active is the physical address that last said <Active Source>, "" for none.
func (f *Fake) Active() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active
}
