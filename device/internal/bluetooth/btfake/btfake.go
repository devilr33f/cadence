// Package btfake is an in-memory bluez and bluealsa for tests and the sim board: no bus, no radio (rule 7).
package btfake

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
)

// AdapterPath is the fake adapter's object path.
const AdapterPath = "/org/bluez/hci0"

var ErrFailed = errors.New("btfake: failed")

// Backend is a bluetooth.Backend whose world the test or the sim sets.
type Backend struct {
	mu    sync.Mutex
	snap  bluetooth.Snapshot
	agent bluetooth.Agent
	calls []string
	fail  map[string]bool
	// failN fails a method's next n calls
	failN map[string]int
	// bonded: device paths paired while the adapter was pairable (mgmt's bondable), whose link key survives Reboot
	bonded map[string]bool
	// duringPair runs inside Pair, once the agent agreed and before the pairing completes
	duringPair func()
	changes    chan struct{}
	closed     bool
	// opens hands out each pcm's data end; nil is a fresh pipe whose write end Writer gives
	opens   map[string]func() (*os.File, error)
	writers map[string]*os.File
	// the speakers a discovery finds
	nearby []bluetooth.Device
	// our registered media player and its status
	player       bluetooth.PlayerControl
	playerStatus string
	// the devices whose transport takes absolute volume
	hard map[string]bool
}

// New has an adapter, powered, pairable and discoverable as midi's main.conf leaves it, and bluealsa on the bus.
func New() *Backend {
	return &Backend{
		snap: bluetooth.Snapshot{
			Adapter:  &bluetooth.Adapter{Path: AdapterPath, Address: "00:11:22:33:44:55", Alias: "stock", Powered: true, Pairable: true, Discoverable: true},
			Bluealsa: true,
		},
		fail: map[string]bool{}, failN: map[string]int{}, bonded: map[string]bool{}, changes: make(chan struct{}, 1), opens: map[string]func() (*os.File, error){},
		writers: map[string]*os.File{}, hard: map[string]bool{},
	}
}

// DevicePath is a device's object path from its address, as bluez names it.
func DevicePath(address string) string {
	return AdapterPath + "/dev_" + strings.ReplaceAll(address, ":", "_")
}

// PCMPath is the a2dp sink pcm's path for a device.
func PCMPath(address string) string { return DevicePath(address) + "/a2dpsnk/source" }

// SourcePCMPath is the a2dp source pcm's path for a speaker: the one we play to it through.
func SourcePCMPath(address string) string { return DevicePath(address) + "/a2dpsrc/sink" }

// Nearby is a speaker in range, unpaired: the next discovery finds it at rssi.
func (f *Backend) Nearby(address, name string, rssi int16) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nearby = append(f.nearby, bluetooth.Device{Path: DevicePath(address), Address: address, Name: name,
		Icon: "audio-card", Class: 0x240414, AudioSink: true, RSSI: rssi})
}

// AddSpeaker is a paired (bonded), trusted speaker, connected or not; a connected one has its a2dp source pcm.
func (f *Backend) AddSpeaker(address, name string, connected bool) {
	f.Update(func(s *bluetooth.Snapshot) {
		f.bonded[DevicePath(address)] = true
		s.Devices = append(s.Devices, bluetooth.Device{Path: DevicePath(address), Address: address, Name: name,
			Icon: "audio-card", Class: 0x240414, AudioSink: true, Paired: true, Trusted: true, Connected: connected})
		if connected {
			s.PCMs = append(s.PCMs, sourcePCM(address))
		}
	})
}

func sourcePCM(address string) bluetooth.PCM {
	return bluetooth.PCM{Path: SourcePCMPath(address), Device: DevicePath(address), Transport: bluetooth.TransportA2DPSource,
		Mode: "sink", Rate: 48000, Channels: 2, Volume: 127<<8 | 127, SoftVolume: true}
}

// SetBluealsa puts bluealsa on the bus or takes it off, with its pcms.
func (f *Backend) SetBluealsa(on bool) {
	f.Update(func(s *bluetooth.Snapshot) {
		s.Bluealsa = on
		if !on {
			s.PCMs = nil
		}
	})
}

func (f *Backend) changed() {
	select {
	case f.changes <- struct{}{}:
	default:
	}
}

func (f *Backend) record(call string) error {
	f.calls = append(f.calls, call)
	method := strings.Fields(call)[0]
	if f.failN[method] > 0 {
		f.failN[method]--
		return ErrFailed
	}
	if f.fail[method] {
		return ErrFailed
	}
	return nil
}

// FailNext makes the next n calls of method fail.
func (f *Backend) FailNext(method string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failN[method] = n
}

// Bonded says whether bluez keeps the device's link key: it was paired while the adapter was pairable.
func (f *Backend) Bonded(address string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bonded[DevicePath(address)]
}

// DuringPair runs during each later Pair, once the agent agreed and before the pairing completes.
func (f *Backend) DuringPair(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.duringPair = fn
}

// Reboot is the host restarted: every device disconnected with its pcms gone, a device that was never bonded no
// longer paired (bluez stored no link key for it), its trust kept, and the adapter powered off until set again.
func (f *Backend) Reboot() {
	f.Update(func(s *bluetooth.Snapshot) {
		for i := range s.Devices {
			d := &s.Devices[i]
			d.Connected, d.RSSI = false, 0
			if !f.bonded[d.Path] {
				d.Paired = false
			}
		}
		s.PCMs, s.Players = nil, nil
		if s.Adapter != nil {
			s.Adapter.Powered, s.Adapter.Discoverable = false, false
		}
	})
}

// Fail makes every later call of method (its name as Calls shows it) fail.
func (f *Backend) Fail(method string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[method] = true
}

// Calls are the setters and actions called so far, in order.
func (f *Backend) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// Update changes the world and tells the capability.
func (f *Backend) Update(change func(s *bluetooth.Snapshot)) {
	f.mu.Lock()
	change(&f.snap)
	f.mu.Unlock()
	f.changed()
}

// AddPhone is a paired, trusted, connected phone with its a2dp sink pcm, not running yet, at volume 127 (bluez 5.70's
// placeholder).
func (f *Backend) AddPhone(address, name string) {
	f.Update(func(s *bluetooth.Snapshot) {
		path := DevicePath(address)
		s.Devices = append(s.Devices, bluetooth.Device{Path: path, Address: address, Name: name, Icon: "phone",
			Class: 0x5a020c, Paired: true, Trusted: true, Connected: true})
		s.PCMs = append(s.PCMs, bluetooth.PCM{Path: PCMPath(address), Device: path, Transport: bluetooth.TransportA2DPSink,
			Mode: "source", Rate: 48000, Channels: 2, Volume: 127<<8 | 127, SoftVolume: true})
	})
}

// RemovePhone takes a device, its pcms and its players away.
func (f *Backend) RemovePhone(address string) {
	path := DevicePath(address)
	f.Update(func(s *bluetooth.Snapshot) {
		s.Devices = slices.DeleteFunc(s.Devices, func(d bluetooth.Device) bool { return d.Path == path })
		s.PCMs = slices.DeleteFunc(s.PCMs, func(p bluetooth.PCM) bool { return p.Device == path })
		s.Players = slices.DeleteFunc(s.Players, func(p bluetooth.Player) bool { return p.Device == path })
	})
}

// SetPCM changes the phone's pcm.
func (f *Backend) SetPCM(address string, change func(p *bluetooth.PCM)) {
	f.Update(func(s *bluetooth.Snapshot) {
		for i := range s.PCMs {
			if s.PCMs[i].Path == PCMPath(address) {
				change(&s.PCMs[i])
			}
		}
	})
}

// SetPlayer sets the phone's player, adding it at the first call.
func (f *Backend) SetPlayer(address string, p bluetooth.Player) {
	p.Device = DevicePath(address)
	p.Path = p.Device + "/player0"
	f.Update(func(s *bluetooth.Snapshot) {
		s.Players = slices.DeleteFunc(s.Players, func(x bluetooth.Player) bool { return x.Path == p.Path })
		s.Players = append(s.Players, p)
	})
}

// OpenWith has the pcm's Open hand out what open returns as its data end.
func (f *Backend) OpenWith(pcm string, open func() (*os.File, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens[pcm] = open
}

// Writer is the write end of the pipe the pcm's last Open handed out, nil before one.
func (f *Backend) Writer(pcm string) *os.File {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writers[pcm]
}

// PairIncoming asks the agent as bluez would for an incoming pairing, and pairs the device (untrusted) when it agrees.
func (f *Backend) PairIncoming(address, name string) bool {
	f.mu.Lock()
	a := f.agent
	f.mu.Unlock()
	path := DevicePath(address)
	if a == nil || !a.Authorize(bluetooth.AgentRequest{Device: path, Address: address, Kind: bluetooth.RequestPairing}) {
		return false
	}
	f.Update(func(s *bluetooth.Snapshot) {
		f.bonded[path] = s.Adapter != nil && s.Adapter.Pairable
		s.Devices = append(s.Devices, bluetooth.Device{Path: path, Address: address, Name: name, Icon: "phone",
			Class: 0x5a020c, Paired: true, Connected: true})
	})
	return true
}

func (f *Backend) Snapshot(context.Context) (bluetooth.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail["Snapshot"] {
		return bluetooth.Snapshot{}, ErrFailed
	}
	s := f.snap
	if s.Adapter != nil {
		a := *s.Adapter
		s.Adapter = &a
	}
	s.Devices, s.PCMs, s.Players = slices.Clone(s.Devices), slices.Clone(s.PCMs), slices.Clone(s.Players)
	for i := range s.PCMs {
		s.PCMs[i].HardVolume = f.hard[s.PCMs[i].Device]
	}
	return s, nil
}

func (f *Backend) Changes() <-chan struct{} { return f.changes }

// Lose ends the backend, as a bus gone away does.
func (f *Backend) Lose() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.changes)
	}
}

func (f *Backend) setAdapter(call string, set func(a *bluetooth.Adapter)) error {
	f.mu.Lock()
	err := f.record(call)
	if err == nil && f.snap.Adapter != nil {
		set(f.snap.Adapter)
	}
	f.mu.Unlock()
	if err == nil {
		f.changed()
	}
	return err
}

func (f *Backend) SetPowered(_ context.Context, on bool) error {
	return f.setAdapter(fmt.Sprintf("SetPowered %t", on), func(a *bluetooth.Adapter) { a.Powered = on })
}

func (f *Backend) SetAlias(_ context.Context, alias string) error {
	return f.setAdapter("SetAlias "+alias, func(a *bluetooth.Adapter) { a.Alias = alias })
}

func (f *Backend) SetWindowTimeouts(_ context.Context, seconds uint32) error {
	return f.setAdapter(fmt.Sprintf("SetWindowTimeouts %d", seconds), func(*bluetooth.Adapter) {})
}

func (f *Backend) SetPairableTimeout(_ context.Context, seconds uint32) error {
	return f.setAdapter(fmt.Sprintf("SetPairableTimeout %d", seconds), func(*bluetooth.Adapter) {})
}

func (f *Backend) SetPairable(_ context.Context, on bool) error {
	return f.setAdapter(fmt.Sprintf("SetPairable %t", on), func(a *bluetooth.Adapter) { a.Pairable = on })
}

func (f *Backend) SetDiscoverable(_ context.Context, on bool) error {
	return f.setAdapter(fmt.Sprintf("SetDiscoverable %t", on), func(a *bluetooth.Adapter) { a.Discoverable = on && a.Powered })
}

func (f *Backend) RegisterAgent(_ context.Context, a bluetooth.Agent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("RegisterAgent"); err != nil {
		return err
	}
	f.agent = a
	return nil
}

func (f *Backend) device(call, path string, set func(d *bluetooth.Device)) error {
	f.mu.Lock()
	err := f.record(call + " " + path)
	if err == nil {
		i := slices.IndexFunc(f.snap.Devices, func(d bluetooth.Device) bool { return d.Path == path })
		if i < 0 {
			err = ErrFailed
		} else {
			set(&f.snap.Devices[i])
		}
	}
	f.mu.Unlock()
	if err == nil {
		f.changed()
	}
	return err
}

func (f *Backend) SetTrusted(_ context.Context, device string, on bool) error {
	return f.device(fmt.Sprintf("SetTrusted %t", on), device, func(d *bluetooth.Device) { d.Trusted = on })
}

// Connect connects the device; a speaker gets its a2dp source pcm, as bluealsa makes one. An unpowered adapter
// connects nothing.
func (f *Backend) Connect(_ context.Context, device string) error {
	f.mu.Lock()
	off := f.snap.Adapter == nil || !f.snap.Adapter.Powered
	f.mu.Unlock()
	if off {
		f.mu.Lock()
		f.calls = append(f.calls, "Connect "+device)
		f.mu.Unlock()
		return ErrFailed
	}
	return f.device("Connect", device, func(d *bluetooth.Device) {
		if !d.Connected && d.AudioSink && f.snap.Bluealsa {
			f.snap.PCMs = append(f.snap.PCMs, sourcePCM(d.Address))
		}
		d.Connected = true
	})
}

// Disconnect disconnects the device; its pcms go with it
func (f *Backend) Disconnect(_ context.Context, device string) error {
	return f.device("Disconnect", device, func(d *bluetooth.Device) {
		d.Connected = false
		f.snap.PCMs = slices.DeleteFunc(f.snap.PCMs, func(p bluetooth.PCM) bool { return p.Device == device })
	})
}

// Drop is the speaker gone out of range or switched off: disconnected, its pcms gone.
func (f *Backend) Drop(address string) {
	path := DevicePath(address)
	f.Update(func(s *bluetooth.Snapshot) {
		for i := range s.Devices {
			if s.Devices[i].Path == path {
				s.Devices[i].Connected = false
			}
		}
		s.PCMs = slices.DeleteFunc(s.PCMs, func(p bluetooth.PCM) bool { return p.Device == path })
	})
}

func (f *Backend) SetDiscoveryFilter(_ context.Context, transport string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.record("SetDiscoveryFilter " + transport)
}

// StartDiscovery finds the nearby speakers
func (f *Backend) StartDiscovery(context.Context) error {
	f.mu.Lock()
	err := f.record("StartDiscovery")
	if err == nil {
		for _, d := range f.nearby {
			if !slices.ContainsFunc(f.snap.Devices, func(x bluetooth.Device) bool { return x.Path == d.Path }) {
				f.snap.Devices = append(f.snap.Devices, d)
			}
		}
	}
	f.mu.Unlock()
	if err == nil {
		f.changed()
	}
	return err
}

func (f *Backend) StopDiscovery(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.record("StopDiscovery")
}

// Pair asks the agent as bluez does a just-works confirmation, and pairs the device when it agrees: bonded when the
// adapter is pairable as the pairing completes, else paired for this boot alone
func (f *Backend) Pair(_ context.Context, device string) error {
	f.mu.Lock()
	a, during := f.agent, f.duringPair
	if err := f.record("Pair " + device); err != nil {
		f.mu.Unlock()
		return err
	}
	i := slices.IndexFunc(f.snap.Devices, func(d bluetooth.Device) bool { return d.Path == device })
	if i < 0 {
		f.mu.Unlock()
		return ErrFailed
	}
	address := f.snap.Devices[i].Address
	f.mu.Unlock()
	if a == nil || !a.Authorize(bluetooth.AgentRequest{Device: device, Address: address, Kind: bluetooth.RequestPairing}) {
		return ErrFailed
	}
	if during != nil {
		during()
	}
	return f.device("Paired", device, func(d *bluetooth.Device) {
		d.Paired = true
		f.bonded[device] = f.snap.Adapter != nil && f.snap.Adapter.Pairable
	})
}

func (f *Backend) Remove(_ context.Context, device string) error {
	f.mu.Lock()
	err := f.record("Remove " + device)
	if err == nil {
		f.snap.Devices = slices.DeleteFunc(f.snap.Devices, func(d bluetooth.Device) bool { return d.Path == device })
		f.snap.PCMs = slices.DeleteFunc(f.snap.PCMs, func(p bluetooth.PCM) bool { return p.Device == device })
		delete(f.bonded, device)
	}
	f.mu.Unlock()
	if err == nil {
		f.changed()
	}
	return err
}

func (f *Backend) pcm(call, path string, set func(p *bluetooth.PCM)) error {
	f.mu.Lock()
	err := f.record(call + " " + path)
	if err == nil {
		for i := range f.snap.PCMs {
			if f.snap.PCMs[i].Path == path {
				set(&f.snap.PCMs[i])
			}
		}
	}
	f.mu.Unlock()
	if err == nil {
		f.changed()
	}
	return err
}

func (f *Backend) SetSoftVolume(_ context.Context, pcm string, on bool) error {
	return f.pcm(fmt.Sprintf("SetSoftVolume %t", on), pcm, func(p *bluetooth.PCM) { p.SoftVolume = on })
}

func (f *Backend) SetVolume(_ context.Context, pcm string, v uint8) error {
	return f.pcm(fmt.Sprintf("SetVolume %d", v), pcm, func(p *bluetooth.PCM) { p.Volume = uint16(v)<<8 | uint16(v) })
}

func (f *Backend) Open(_ context.Context, pcm string) (data, ctrl *os.File, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("Open " + pcm); err != nil {
		return nil, nil, err
	}
	if open := f.opens[pcm]; open != nil {
		data, err = open()
	} else {
		var w *os.File
		data, w, err = os.Pipe()
		if err == nil {
			if old := f.writers[pcm]; old != nil {
				_ = old.Close()
			}
			f.writers[pcm] = w
		}
	}
	if err != nil {
		return nil, nil, err
	}
	// the control end: a pipe stands in for bluealsa's socket
	ctrl, w, err := os.Pipe()
	if err != nil {
		_ = data.Close()
		return nil, nil, err
	}
	_ = w.Close()
	return data, ctrl, nil
}

func (f *Backend) Control(_ context.Context, player, method string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.record(method + " " + player)
}

// SetHardVolume says whether a speaker's transport takes avrcp absolute volume.
func (f *Backend) SetHardVolume(address string, on bool) {
	f.Update(func(*bluetooth.Snapshot) { f.hard[DevicePath(address)] = on })
}

// SpeakerVolume is the speaker's own button setting its absolute volume, as bluealsa then shows it on the pcm.
func (f *Backend) SpeakerVolume(address string, v uint8) {
	f.Update(func(s *bluetooth.Snapshot) {
		for i := range s.PCMs {
			if s.PCMs[i].Path == SourcePCMPath(address) {
				s.PCMs[i].Volume = uint16(v)<<8 | uint16(v)
			}
		}
	})
}

// Press is a speaker's button reaching our registered player; false when none is registered.
func (f *Backend) Press(method string) bool {
	f.mu.Lock()
	p := f.player
	f.mu.Unlock()
	if p == nil {
		return false
	}
	p.PlayerCommand(method)
	return true
}

// PlayerStatus is our player's status as bluez has it, "" before any.
func (f *Backend) PlayerStatus() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.playerStatus
}

func (f *Backend) RegisterPlayer(_ context.Context, c bluetooth.PlayerControl) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("RegisterPlayer"); err != nil {
		return err
	}
	f.player = c
	return nil
}

func (f *Backend) SetPlayerStatus(_ context.Context, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("SetPlayerStatus " + status); err != nil {
		return err
	}
	f.playerStatus = status
	return nil
}

func (f *Backend) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.writers {
		_ = w.Close()
	}
	clear(f.writers)
	return nil
}

var _ bluetooth.Backend = (*Backend)(nil)
