package cec

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// how long one transmit waits for its reply, and how long a whole scan may run
const (
	replyWait  = 800 * time.Millisecond
	scanWait   = 25 * time.Second
	pressGap   = 60 * time.Millisecond
	powerEvery = 5 * time.Minute
)

// power states as Report Power Status carries them and the state names them (the kernel's
// CEC_OP_POWER_STATUS_*: on 0, standby 1, in transition to on 2, to standby 3)
func powerName(b byte) string {
	switch b {
	case 0x00:
		return "on"
	case 0x01:
		return "standby"
	case 0x02:
		return "to_on"
	case 0x03:
		return "to_off"
	}
	return "unknown"
}

// AudioStatus is Report Audio Status: the volume a device plays at (0-100) and its mute
type AudioStatus struct {
	Volume int  `json:"volume"`
	Muted  bool `json:"muted"`
}

// Device is one device the last scan heard on the wire
type Device struct {
	LogicalAddress int          `json:"logicalAddress"`
	Type           string       `json:"type"`
	Physical       string       `json:"physicalAddress,omitempty"`
	VendorID       uint32       `json:"vendorId,omitempty"`
	Name           string       `json:"name,omitempty"`
	Power          string       `json:"power"`
	Audio          *AudioStatus `json:"audioStatus,omitempty"`
	Supports       []string     `json:"supports,omitempty"`
}

// Controller is display.cec's state: the bus's adapter, the devices a scan found on it and the events it
// hears. Commands and scans run one at a time (an op mutex); state publishes the latest snapshot only.
type Controller struct {
	bus Bus
	log *slog.Logger

	state   chan json.RawMessage
	enabled atomic.Bool

	// op serializes every transmit flow (scan, power, volume); mu guards the snapshot
	op sync.Mutex
	mu sync.Mutex
	// snapshot fields, under mu
	sam     string // "on"|"off"|"unknown"
	devices map[int]*Device
}

// New is the display.cec provider over bus. A nil bus is a board without cec: the capability still reports
// (present false) and answers refresh. A nil log discards.
func New(bus Bus, log *slog.Logger) *Controller {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	c := &Controller{bus: bus, log: log, state: make(chan json.RawMessage, 1), sam: "unknown", devices: map[int]*Device{}}
	c.enabled.Store(true)
	if bus != nil {
		bus.Subscribe(c.inbound)
	}
	return c
}

// Run starts the bus (idempotent, as a board that started it itself may call it again), scans the wire and
// follows it until ctx ends: the events the bus reports and a light power refresh keep the snapshot honest.
// Boards start it (tvboard starts an Extra provider that runs).
func (c *Controller) Run(ctx context.Context, wg *sync.WaitGroup) {
	if c.bus != nil {
		c.bus.Start(ctx, wg)
	}
	c.Scan(context.Background())
	wg.Go(func() {
		t := time.NewTicker(powerEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.refreshPower(ctx)
			}
		}
	})
}

// Scan polls every logical address and asks each device that answers what it is; boards may also call it
// outside a command (their start).
func (c *Controller) Scan(ctx context.Context) {
	if c.bus == nil {
		c.publish()
		return
	}
	c.op.Lock()
	defer c.op.Unlock()
	ctx, cancel := context.WithTimeout(ctx, scanWait)
	defer cancel()
	c.scan(ctx)
	c.publish()
}

// scan is the wire walk itself, under op
func (c *Controller) scan(ctx context.Context) {
	found := map[int]*Device{}
	for la := 0; la <= 15; la++ {
		if la == c.bus.Info().LogicalAddress {
			continue
		}
		if !c.ack(ctx, Frame{Dest: byte(la), Polling: true}) {
			continue
		}
		d := &Device{LogicalAddress: la, Type: typeByAddress(byte(la)), Power: "unknown"}
		if f, ok := c.ask(ctx, la, OpGivePhysicalAddress, OpReportPhysicalAddress); ok && len(f.Ops) >= 3 {
			d.Physical = FormatPhysical(uint16(f.Ops[0])<<8 | uint16(f.Ops[1]))
			d.Type = typeName(f.Ops[2])
		}
		if f, ok := c.ask(ctx, la, OpGiveDeviceVendorID, OpDeviceVendorID); ok && len(f.Ops) >= 3 {
			d.VendorID = uint32(f.Ops[0])<<16 | uint32(f.Ops[1])<<8 | uint32(f.Ops[2])
		}
		if f, ok := c.ask(ctx, la, OpGiveOSDName, OpSetOSDName); ok {
			d.Name = string(f.Ops)
		}
		if f, ok := c.ask(ctx, la, OpGiveDevicePowerStatus, OpReportPowerStatus); ok && len(f.Ops) >= 1 {
			d.Power = powerName(f.Ops[0])
		}
		if f, ok := c.ask(ctx, la, OpGiveAudioStatus, OpReportAudioStatus); ok && len(f.Ops) >= 1 {
			d.Audio = &AudioStatus{Volume: int(f.Ops[0] & 0x7f), Muted: f.Ops[0]&0x80 != 0}
		}
		d.Supports = supports(d)
		found[la] = d
	}
	sam := "unknown"
	if avr := findType(found, "audio_system"); avr != nil {
		if f, ok := c.ask(ctx, avr.LogicalAddress, OpGiveSystemAudioModeStatus, OpReportSystemAudioMode); ok && len(f.Ops) >= 1 {
			if f.Ops[0] == 0x01 {
				sam = "on"
			} else {
				sam = "off"
			}
		}
	}
	c.mu.Lock()
	c.devices, c.sam = found, sam
	c.mu.Unlock()
	c.log.Info("cec_scan", "devices", len(found), "sam", sam)
}

// refreshPower re-asks the devices it knows for power and audio alone: the snapshot follows a tv its own
// remote turned off, an avr whose volume moved
func (c *Controller) refreshPower(ctx context.Context) {
	c.mu.Lock()
	las := make([]int, 0, len(c.devices))
	for la := range c.devices {
		las = append(las, la)
	}
	c.mu.Unlock()
	if len(las) == 0 {
		return
	}
	c.op.Lock()
	defer c.op.Unlock()
	for _, la := range las {
		if f, ok := c.ask(ctx, la, OpGiveDevicePowerStatus, OpReportPowerStatus); ok && len(f.Ops) >= 1 {
			c.mu.Lock()
			if d := c.devices[la]; d != nil {
				d.Power = powerName(f.Ops[0])
			}
			c.mu.Unlock()
		}
		if f, ok := c.ask(ctx, la, OpGiveAudioStatus, OpReportAudioStatus); ok && len(f.Ops) >= 1 {
			c.mu.Lock()
			if d := c.devices[la]; d != nil {
				d.Audio = &AudioStatus{Volume: int(f.Ops[0] & 0x7f), Muted: f.Ops[0]&0x80 != 0}
			}
			c.mu.Unlock()
		}
	}
	c.publish()
}

// inbound tracks what the wire says besides the answers Send took: an avr's system audio broadcasts, audio
// status it pushes, a broadcast standby, a power status answered out of band
func (c *Controller) inbound(f Frame) {
	switch f.Opcode {
	case OpSetSystemAudioMode:
		if f.Dest == 0xf && len(f.Ops) >= 1 {
			v := "off"
			if f.Ops[0] == 0x01 {
				v = "on"
			}
			c.mu.Lock()
			c.sam = v
			c.mu.Unlock()
			c.publish()
		}
	case OpReportAudioStatus:
		if len(f.Ops) >= 1 {
			c.mu.Lock()
			if d := c.devices[int(f.Init)]; d != nil {
				d.Audio = &AudioStatus{Volume: int(f.Ops[0] & 0x7f), Muted: f.Ops[0]&0x80 != 0}
			}
			c.mu.Unlock()
			c.publish()
		}
	case OpStandby:
		if f.Dest == 0xf {
			c.mu.Lock()
			for _, d := range c.devices {
				d.Power = "standby"
			}
			c.mu.Unlock()
			c.publish()
		}
	case OpReportPowerStatus:
		if len(f.Ops) >= 1 {
			c.mu.Lock()
			if d := c.devices[int(f.Init)]; d != nil {
				d.Power = powerName(f.Ops[0])
			}
			c.mu.Unlock()
			c.publish()
		}
	}
}

// ack sends msg and says the wire took it
func (c *Controller) ack(ctx context.Context, msg Frame) bool {
	if msg.Polling {
		_, ok := c.bus.Send(ctx, msg, 0)
		return ok
	}
	return false
}

// ask sends an opcode and waits for want's reply, taking a Feature Abort of it as no
func (c *Controller) ask(ctx context.Context, la int, op, want byte) (Frame, bool) {
	ctx, cancel := context.WithTimeout(ctx, replyWait)
	defer cancel()
	f, ok := c.bus.Send(ctx, Frame{Dest: byte(la), Opcode: op}, want) //nolint:gosec // 0-15 from scan
	if !ok {
		return f, false
	}
	if aborted, is := f.abort(); is && aborted == want {
		return f, false
	}
	if f.Opcode != want {
		return f, false
	}
	return f, true
}

// supports names what a device takes, by its type and what it answered
func supports(d *Device) []string {
	var out []string
	switch d.Type {
	case "tv":
		out = append(out, "power", "volume")
	case "audio_system":
		out = append(out, "power", "volume", "audio_status", "system_audio")
	default:
		if d.Audio != nil {
			out = append(out, "audio_status")
		}
	}
	if d.Type != "tv" && d.Type != "audio_system" && d.Audio == nil && d.Power != "unknown" {
		out = append(out, "power")
	}
	return out
}

// findType picks the device of a type, the lowest logical address first (0 tv, 5 audio system on the wire)
func findType(devices map[int]*Device, typ string) *Device {
	for la := 0; la <= 15; la++ {
		if d := devices[la]; d != nil && d.Type == typ {
			return d
		}
	}
	return nil
}

// tv and avr are the flows' targets, under mu
func (c *Controller) tv() *Device  { return findType(c.devices, "tv") }
func (c *Controller) avr() *Device { return findType(c.devices, "audio_system") }

// volumeTarget answers whom the volume keys go to, under mu: the avr while system audio is on
func (c *Controller) volumeTarget() *Device {
	if c.sam == "on" {
		if d := c.avr(); d != nil {
			return d
		}
	}
	return c.tv()
}

// publish replaces the state the server has not taken yet
func (c *Controller) publish() {
	c.mu.Lock()
	devices := make([]Device, 0, len(c.devices))
	for la := 0; la <= 15; la++ {
		if d := c.devices[la]; d != nil {
			devices = append(devices, *d)
		}
	}
	sam, samAvail := c.sam, c.avr() != nil
	c.mu.Unlock()
	s := stateJSON{
		Adapter:     c.adapterState(),
		SystemAudio: systemAudioJSON{Available: samAvail, Enabled: sam},
		Devices:     devices,
	}
	if len(devices) > 0 {
		s.VolumeTarget = "tv"
		if sam == "on" && samAvail {
			s.VolumeTarget = "audio_system"
		}
	}
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	select {
	case <-c.state:
	default:
	}
	c.state <- json.RawMessage(b)
}

// adapterState is the bus's own side; a logical address of 0 (the tv's) is a value, never an omission
func (c *Controller) adapterState() adapterJSON {
	if c.bus == nil {
		return adapterJSON{Present: false}
	}
	info := c.bus.Info()
	if !info.Present {
		return adapterJSON{Present: false}
	}
	a := adapterJSON{Present: true, PhysicalAddress: info.PhysicalAddress, DeviceType: info.DeviceType}
	if info.LogicalAddress >= 0 {
		la := info.LogicalAddress
		a.LogicalAddress = &la
	}
	return a
}

type adapterJSON struct {
	Present         bool   `json:"present"`
	PhysicalAddress string `json:"physicalAddress,omitempty"`
	LogicalAddress  *int   `json:"logicalAddress,omitempty"`
	DeviceType      string `json:"deviceType,omitempty"`
}

type systemAudioJSON struct {
	Available bool   `json:"available"`
	Enabled   string `json:"enabled"`
}

type stateJSON struct {
	Adapter      adapterJSON     `json:"adapter"`
	SystemAudio  systemAudioJSON `json:"systemAudio"`
	VolumeTarget string          `json:"volumeTarget,omitempty"`
	Devices      []Device        `json:"devices"`
}
