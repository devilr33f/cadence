// Package evdev reads a board's buttons and mute switch from linux input devices. The board names its devices,
// where they are and the codes it maps; nothing here names hardware.
package evdev

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/input"
)

// linux input event types and codes (<linux/input-event-codes.h>)
const (
	evSyn = 0x00
	evKey = 0x01
	evSw  = 0x05

	synReport  = 0
	synDropped = 3

	swMax = 0x10
)

// struct input_event: a timeval of two words, then type, code and value; little endian on every board's cpu. a
// 32-bit process on a 64-bit kernel reads the 32-bit layout, which the kernel converts to
const eventSize = 2*strconv.IntSize/8 + 8

// the kernel's sizes, 16 bytes for a 32-bit process and 24 for a 64-bit one, checked whenever the package compiles
// (make cross builds arm v7): a constant that would go negative fails the build
const (
	kernelEventSize = 16 + 8*(strconv.IntSize/64)
	_               = uint(eventSize - kernelEventSize)
	_               = uint(kernelEventSize - eventSize)
)

// Device is one of a board's input devices, found by the name its driver gives it rather than its event number.
type Device struct {
	// Role names the device in logs
	Role string
	Name string
	// Switch says the device holds the mute switch: its position is read when it opens and after the kernel
	// dropped events
	Switch bool
}

// Layout is a board's buttons.
type Layout struct {
	// SysClass is the input class directory of sysfs and Nodes the directory of the device nodes, both as the board
	// finds them under its root
	SysClass, Nodes string
	Devices         []Device
	// Keys are the key codes the board maps; any other key, and a key's repeat, is nothing
	Keys map[uint16]input.Kind
	// MuteSwitch is the switch code of the mute switch
	MuteSwitch uint16
	// MuteToggle is a key whose press flips mute, for a board with a button where others have a switch; keep it
	// out of Keys. MuteToggleStart is the position at boot and MuteToggled hears each flip, for persistence
	MuteToggle      uint16
	MuteToggleStart bool
	MuteToggled     func(on bool)
}

// ReadSwitch is the mute switch's latched position on an open node; false when the node cannot tell.
type ReadSwitch func(*os.File) (on, ok bool)

type event struct {
	typ, code uint16
	value     int32
}

func decodeEvent(b []byte) event {
	at := len(b) - 8
	return event{
		typ:   binary.LittleEndian.Uint16(b[at:]),
		code:  binary.LittleEndian.Uint16(b[at+2:]),
		value: int32(binary.LittleEndian.Uint32(b[at+4:])), //nolint:gosec // the kernel's s32, bit for bit
	}
}

// buttonEvent is a key's press (1) or release (0), or the switch's position; a key's repeat (2) and every other
// code are nothing
func (l *Layout) buttonEvent(e event) (input.ButtonEvent, bool) {
	switch e.typ {
	case evKey:
		if k, ok := l.Keys[e.code]; ok && (e.value == 0 || e.value == 1) {
			return input.ButtonEvent{Kind: k, Pressed: e.value == 1}, true
		}
	case evSw:
		if e.code == l.MuteSwitch {
			return switchEvent(e.value != 0), true
		}
	}
	return input.ButtonEvent{}, false
}

func switchEvent(on bool) input.ButtonEvent {
	if on {
		return input.ButtonEvent{Kind: input.MuteOn, Pressed: true}
	}
	return input.ButtonEvent{Kind: input.MuteOff}
}

// Buttons is the input's Buttons port: a reader per device. A device that is missing or fails is logged and the
// rest go on.
type Buttons struct {
	layout     Layout
	log        *slog.Logger
	now        func() time.Time
	readSwitch ReadSwitch
	events     chan input.ButtonEvent
	devices    []*device
	toggleMu   sync.Mutex
	toggleOn   bool
}

type device struct {
	Device
	f *os.File
	// the mute switch's position when it was opened, unknown when nil
	initial *bool
}

// Open opens the layout's devices and reads the mute switch, before anything runs: muted is its position at boot.
func Open(l Layout, log *slog.Logger, readSwitch ReadSwitch) (b *Buttons, muted bool) {
	b = &Buttons{layout: l, log: log, now: time.Now, readSwitch: readSwitch, events: make(chan input.ButtonEvent, 16)}
	if l.MuteToggle != 0 {
		b.toggleOn, muted = l.MuteToggleStart, l.MuteToggleStart
	}
	for _, d := range l.Devices {
		f, reason := openByName(l.SysClass, l.Nodes, d.Name)
		if f == nil {
			log.Warn("input_unavailable", "device", d.Role, "reason", reason)
			continue
		}
		dev := &device{Device: d, f: f}
		if d.Switch {
			// opened first: a change after the read is an event on it
			if on, ok := readSwitch(f); ok {
				dev.initial, muted = &on, on
			} else {
				log.Warn("mute_switch_unknown")
			}
		}
		b.devices = append(b.devices, dev)
	}
	return b, muted
}

// openByName opens the first node, in name order, whose sysfs device name is name
func openByName(sys, nodesDir, name string) (*os.File, string) {
	entries, err := os.ReadDir(sys)
	if err != nil {
		return nil, "not_found"
	}
	nodes := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "event") {
			nodes = append(nodes, e.Name())
		}
	}
	slices.Sort(nodes)
	for _, node := range nodes {
		raw, err := os.ReadFile(filepath.Join(sys, node, "device", "name")) //nolint:gosec // sysfs, under the board's root
		if err != nil || strings.TrimSuffix(string(raw), "\n") != name {
			continue
		}
		f, err := os.Open(filepath.Join(nodesDir, node)) //nolint:gosec // an evdev node found by name
		if err != nil {
			return nil, "open_failed"
		}
		return f, ""
	}
	return nil, "not_found"
}

func (b *Buttons) Events() <-chan input.ButtonEvent { return b.events }

// Start runs a reader per open device on wg until ctx ends or Close.
func (b *Buttons) Start(ctx context.Context, wg *sync.WaitGroup) {
	opened := make([]any, 0, 2*len(b.layout.Devices))
	for _, d := range b.layout.Devices {
		n := 0
		if slices.ContainsFunc(b.devices, func(o *device) bool { return o.Role == d.Role }) {
			n = 1
		}
		opened = append(opened, d.Role, n)
	}
	if b.layout.MuteToggle != 0 {
		// before any reader; the events channel holds it, so this does not wait
		b.send(ctx, switchEvent(b.toggleOn))
	}
	for _, d := range b.devices {
		wg.Go(func() { b.read(ctx, d) })
	}
	b.log.Info("input_started", opened...)
}

// Close ends the readers: a read waiting on a node returns.
func (b *Buttons) Close() {
	for _, d := range b.devices {
		_ = d.f.Close()
	}
}

// read reports the switch's first position, then each event, until the node fails or ctx ends. After the kernel
// dropped events, what follows up to the next report is partial and the switch is read afresh.
func (b *Buttons) read(ctx context.Context, d *device) {
	if d.initial != nil && !b.send(ctx, switchEvent(*d.initial)) {
		return
	}
	r := bufio.NewReaderSize(d.f, 64*eventSize)
	buf := make([]byte, eventSize)
	dropped := false
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			if ctx.Err() == nil {
				b.log.Warn("input_lost", "device", d.Role)
			}
			return
		}
		ev := decodeEvent(buf)
		switch {
		case ev.typ == evSyn && ev.code == synDropped:
			dropped = true
		case dropped:
			if ev.typ == evSyn && ev.code == synReport {
				dropped = false
				if !b.resync(ctx, d) {
					return
				}
			}
		case b.layout.MuteToggle != 0 && ev.typ == evKey && ev.code == b.layout.MuteToggle:
			if ev.value == 1 && !b.toggle(ctx) {
				return
			}
		default:
			if be, ok := b.layout.buttonEvent(ev); ok && !b.send(ctx, be) {
				return
			}
		}
	}
}

func (b *Buttons) resync(ctx context.Context, d *device) bool {
	b.log.Info("input_resync", "device", d.Role)
	if !d.Switch {
		return true
	}
	on, ok := b.readSwitch(d.f)
	if !ok {
		b.log.Warn("mute_switch_unknown")
		return true
	}
	return b.send(ctx, switchEvent(on))
}

// send waits for the input, which reads all the time: the switch is never dropped
func (b *Buttons) send(ctx context.Context, be input.ButtonEvent) bool {
	be.At = b.now()
	select {
	case b.events <- be:
		return true
	case <-ctx.Done():
		return false
	}
}
