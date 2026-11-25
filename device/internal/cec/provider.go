package cec

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
)

// command error tokens the server may see; never args or device text
var (
	errInvalid      = &capability.CommandError{Code: "invalid"}
	errDisabled     = &capability.CommandError{Code: "disabled"}
	errNoBus        = &capability.CommandError{Code: "no_bus"}
	errNoTarget     = &capability.CommandError{Code: "no_target"}
	errUnknownPower = &capability.CommandError{Code: "unknown_power"}
	errNoAddress    = &capability.CommandError{Code: "no_address"}
)

// Descriptor is display.cec's: the schemas both sides share (pkg/capschema).
func (c *Controller) Descriptor() capability.Descriptor {
	return capability.Descriptor{
		ID: capschema.DisplayCecID, Version: 1,
		ConfigSchema: capschema.DisplayCecConfig, StateSchema: capschema.DisplayCecState,
		Commands: capsCommands(),
	}
}

// capsCommands copies the shared table: the descriptor owns its map
func capsCommands() map[string]string {
	cmds := make(map[string]string, len(capschema.DisplayCecCommands))
	for k, v := range capschema.DisplayCecCommands {
		cmds[k] = v
	}
	return cmds
}

// Apply takes enabled (absent keeps it); false keeps the board from ever transmitting.
func (c *Controller) Apply(_ context.Context, _ int64, cfg json.RawMessage) error {
	raw := bytes.TrimSpace(cfg)
	var v struct {
		Enabled *bool `json:"enabled"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if len(raw) == 0 || raw[0] != '{' || dec.Decode(&v) != nil {
		return &capability.ApplyError{Code: "invalid"}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return &capability.ApplyError{Code: "invalid"}
	}
	c.enabled.Store(v.Enabled == nil || *v.Enabled)
	return nil
}

// State is the latest snapshot the server has not taken yet.
func (c *Controller) State() <-chan json.RawMessage { return c.state }

// CommandWait lets refresh walk the wire (core's usual wait is a few seconds).
func (c *Controller) CommandWait(name string) time.Duration {
	if name == "refresh" {
		return 30 * time.Second
	}
	return 0
}

type powerArgs struct {
	Action string `json:"action"`
}

type volumeArgs struct {
	Action string `json:"action"`
	Steps  int    `json:"steps"`
}

type systemAudioArgs struct {
	Enabled *bool `json:"enabled"`
}

// Command runs one of display.cec's: an authenticated caller's press, never the agent's (rule 7).
func (c *Controller) Command(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	switch name {
	case "refresh":
		if err := decodeObject(args, &struct{}{}); err != nil {
			return nil, errInvalid
		}
		if !c.ready() {
			// a board with no bus still answers: its empty snapshot is the answer
			if c.bus == nil {
				c.publish()
				return c.devicesJSON(), nil
			}
			return nil, errDisabled
		}
		c.Scan(ctx)
		return c.devicesJSON(), nil
	case "power":
		var a powerArgs
		if decodeObject(args, &a) != nil || a.Action == "" {
			return nil, errInvalid
		}
		if !c.ready() {
			return nil, c.notReady()
		}
		switch a.Action {
		case "on":
			return nil, c.power(ctx, true)
		case "off":
			return nil, c.power(ctx, false)
		case "toggle":
			c.mu.Lock()
			power := "unknown"
			if tv := c.tv(); tv != nil {
				power = tv.Power
			}
			c.mu.Unlock()
			switch power {
			case "on", "to_on":
				return nil, c.power(ctx, false)
			case "standby", "to_off":
				return nil, c.power(ctx, true)
			default:
				return nil, errUnknownPower
			}
		}
		return nil, errInvalid
	case "volume":
		var a volumeArgs
		if err := decodeObject(args, &a); err != nil || a.Action == "" {
			return nil, errInvalid
		}
		if a.Steps < 0 || a.Steps > capschema.CecVolumeMaxSteps {
			return nil, errInvalid
		}
		if a.Steps == 0 {
			a.Steps = 1
		}
		if !c.ready() {
			return nil, c.notReady()
		}
		return nil, c.volume(ctx, a.Action, a.Steps)
	case "system_audio":
		var a systemAudioArgs
		if err := decodeObject(args, &a); err != nil || a.Enabled == nil {
			return nil, errInvalid
		}
		if !c.ready() {
			return nil, c.notReady()
		}
		return nil, c.systemAudio(ctx, *a.Enabled)
	case "active_source":
		if err := decodeObject(args, &struct{}{}); err != nil {
			return nil, errInvalid
		}
		if !c.ready() {
			return nil, c.notReady()
		}
		return nil, c.activeSource(ctx)
	}
	return nil, errInvalid
}

// ready says the bus is there and the config leaves it free to transmit
func (c *Controller) ready() bool {
	return c.bus != nil && c.bus.Info().Present && c.enabled.Load()
}

// notReady is why a command cannot run: the platform has no cec, or the config switched it off
func (c *Controller) notReady() error {
	if c.bus == nil || !c.bus.Info().Present {
		return errNoBus
	}
	return errDisabled
}

// power is one touch play (wake the tv and be its source) or a standby to the tv alone
func (c *Controller) power(ctx context.Context, on bool) error {
	c.op.Lock()
	defer c.op.Unlock()
	c.mu.Lock()
	tv := c.tv()
	var la byte
	have := tv != nil
	if have {
		la = byte(tv.LogicalAddress) //nolint:gosec // a logical address, 0-15 from scan
	}
	c.mu.Unlock()
	if !have {
		return errNoTarget
	}
	if on {
		// one touch play: the view on wakes the tv, the broadcast makes us its source; a tv that ignores
		// text view on still takes image view on (libcec sends both to pickier tvs)
		_, text := c.bus.Send(ctx, Frame{Dest: la, Opcode: OpTextViewOn}, 0)
		_, image := c.bus.Send(ctx, Frame{Dest: la, Opcode: OpImageViewOn}, 0)
		if !text && !image {
			return errNoTarget
		}
		// a bus that knows no address of its own (the app's: the framework owns the wire) still woke the
		// tv with its one touch play; the broadcast it could not send is not the command's failure
		_ = c.sendActiveSource(ctx)
		c.mu.Lock()
		if tv := c.tv(); tv != nil {
			tv.Power = "on"
		}
		c.mu.Unlock()
	} else {
		if _, ok := c.bus.Send(ctx, Frame{Dest: la, Opcode: OpStandby}, 0); !ok {
			return errNoTarget
		}
		c.mu.Lock()
		if tv := c.tv(); tv != nil {
			tv.Power = "standby"
		}
		c.mu.Unlock()
	}
	c.publish()
	return nil
}

// volume presses the target's keys as a remote would: a press and its release per step, spaced; the target
// is the avr while system audio is on, else the tv
func (c *Controller) volume(ctx context.Context, action string, steps int) error {
	var code byte
	switch action {
	case "up":
		code = UCVolumeUp
	case "down":
		code = UCVolumeDown
	case "mute", "unmute", "toggle_mute":
		code = UCMute
	default:
		return errInvalid
	}
	c.op.Lock()
	defer c.op.Unlock()
	c.mu.Lock()
	target := c.volumeTarget()
	var la byte
	known := target != nil
	if target != nil {
		la = byte(target.LogicalAddress) //nolint:gosec // a logical address, 0-15 from scan
	}
	target = nil // the pointer is the snapshot's, not ours to keep past the lock
	c.mu.Unlock()
	if !known {
		return errNoTarget
	}
	// there is no unmute code on the wire: the key toggles, so a target that answers it is already loud
	// needs no press. the snapshot may not know (a mute only it heard), so ask it now
	if action == "unmute" {
		if f, ok := c.ask(ctx, int(la), OpGiveAudioStatus, OpReportAudioStatus); ok && len(f.Ops) >= 1 && f.Ops[0]&0x80 == 0 {
			return nil
		}
	}
	for i := 0; i < steps; i++ {
		if _, ok := c.bus.Send(ctx, Frame{Dest: la, Opcode: OpUserControlPressed, Ops: []byte{code}}, 0); !ok && i == 0 {
			// the wire refused the first press: nothing there to press
			return errNoTarget
		}
		c.bus.Send(ctx, Frame{Dest: la, Opcode: OpUserControlReleased}, 0)
		if i+1 < steps {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(pressGap):
			}
		}
	}
	return nil
}

// systemAudio asks the avr to take the audio (with our physical address) or give it back (no operand)
func (c *Controller) systemAudio(ctx context.Context, on bool) error {
	c.op.Lock()
	defer c.op.Unlock()
	c.mu.Lock()
	avr := c.avr()
	c.mu.Unlock()
	if avr == nil {
		return errNoTarget
	}
	msg := Frame{Dest: byte(avr.LogicalAddress), Opcode: OpSystemAudioModeRequest} //nolint:gosec // 0-15 from scan
	if on {
		v, ok := ParsePhysical(c.bus.Info().PhysicalAddress)
		if !ok {
			return errNoAddress
		}
		msg.Ops = binary.BigEndian.AppendUint16(nil, v)
	}
	c.bus.Send(ctx, msg, 0)
	// the avr broadcasts its answer; until it arrives the state says what we asked for
	c.mu.Lock()
	c.sam = "off"
	if on {
		c.sam = "on"
	}
	c.mu.Unlock()
	c.publish()
	return nil
}

// activeSource broadcasts us as the source to watch
func (c *Controller) activeSource(ctx context.Context) error {
	c.op.Lock()
	defer c.op.Unlock()
	return c.sendActiveSource(ctx)
}

func (c *Controller) sendActiveSource(ctx context.Context) error {
	v, ok := ParsePhysical(c.bus.Info().PhysicalAddress)
	if !ok {
		return errNoAddress
	}
	c.bus.Send(ctx, Frame{Dest: 0xf, Opcode: OpActiveSource, Ops: binary.BigEndian.AppendUint16(nil, v)}, 0)
	return nil
}

// devicesJSON is refresh's answer: the snapshot's devices as the state spells them
func (c *Controller) devicesJSON() json.RawMessage {
	c.mu.Lock()
	devices := make([]Device, 0, len(c.devices))
	for la := 0; la <= 15; la++ {
		if d := c.devices[la]; d != nil {
			devices = append(devices, *d)
		}
	}
	c.mu.Unlock()
	b, err := json.Marshal(struct {
		Devices []Device `json:"devices"`
	}{devices})
	if err != nil {
		return json.RawMessage(`{"devices":[]}`)
	}
	return b
}

// decodeObject parses a command's args strictly: an object (empty allowed) with no unknown field
func decodeObject(raw []byte, v any) error {
	b := bytes.TrimSpace(raw)
	if len(b) == 0 {
		b = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if b[0] != '{' || dec.Decode(v) != nil {
		return errInvalid
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errInvalid
	}
	return nil
}
