// Package bluez is the bluetooth backend on the system bus (godbus, pure go): BlueZ's adapter, devices, players and
// agent manager, and bluealsa 4.x's pcms through its object manager. It reads link keys never; they stay in bluez.
package bluez

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"

	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
)

const (
	// DefaultBus is the system bus where most systems keep it (midi's /var/run links to /run)
	DefaultBus = "unix:path=/var/run/dbus/system_bus_socket"

	bluezName    = "org.bluez"
	bluealsaName = "org.bluealsa"
	bluealsaRoot = "/org/bluealsa"
	agentPath    = "/org/cadence/agent"

	ifaceAdapter   = "org.bluez.Adapter1"
	ifaceDevice    = "org.bluez.Device1"
	ifacePlayer    = "org.bluez.MediaPlayer1"
	ifaceTransport = "org.bluez.MediaTransport1"
	ifaceAgent     = "org.bluez.Agent1"
	ifaceAgentMgr  = "org.bluez.AgentManager1"
	ifacePCM       = "org.bluealsa.PCM1"
	ifaceManager   = "org.bluealsa.Manager1"

	// the a2dp sink's service class: a device that plays what we send
	uuidAudioSink = "0000110b-0000-1000-8000-00805f9b34fb"

	objectManager = "org.freedesktop.DBus.ObjectManager.GetManagedObjects"
	propertiesSet = "org.freedesktop.DBus.Properties.Set"
	propertiesGet = "org.freedesktop.DBus.Properties.Get"
)

type managed = map[dbus.ObjectPath]map[string]map[string]dbus.Variant

// Backend is one connection to the system bus.
type Backend struct {
	conn    *dbus.Conn
	adapter dbus.ObjectPath
	changes chan struct{}

	agentMu sync.Mutex
	agent   *agent

	playerMu sync.Mutex
	player   *mediaPlayer
}

// Dial connects to bus ("" for DefaultBus) for the adapter named hci ("hci0"), and watches bluez's and bluealsa's
// signals and owners. The connection lives until Close or ctx ends.
func Dial(ctx context.Context, bus, hci string) (*Backend, error) {
	conn, err := connect(ctx, bus)
	if err != nil {
		return nil, err
	}
	if !conn.SupportsUnixFDs() {
		_ = conn.Close()
		return nil, errors.New("bluez: the bus passes no fds")
	}
	b := &Backend{conn: conn, adapter: dbus.ObjectPath("/org/bluez/" + hci), changes: make(chan struct{}, 1)}
	rules := [][]dbus.MatchOption{
		{dbus.WithMatchSender(bluezName)},
		{dbus.WithMatchSender(bluealsaName)},
		{dbus.WithMatchInterface("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, bluezName)},
		{dbus.WithMatchInterface("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, bluealsaName)},
	}
	for _, r := range rules {
		if err := conn.AddMatchSignalContext(ctx, r...); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	signals := make(chan *dbus.Signal, 64)
	conn.Signal(signals)
	go func() {
		// the connection's end closes signals
		defer close(b.changes)
		for range signals {
			select {
			case b.changes <- struct{}{}:
			default:
			}
		}
	}()
	return b, nil
}

// connect opens and authenticates a connection to bus ("" for DefaultBus)
func connect(ctx context.Context, bus string) (*dbus.Conn, error) {
	if bus == "" {
		bus = DefaultBus
	}
	conn, err := dbus.Dial(bus, dbus.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	if err := conn.Auth(nil); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.Hello(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// Output reasons, as QualifyOutput gives them.
const (
	OutputNoBus      = "bus"
	OutputNoBluez    = "bluez"
	OutputNoBluealsa = "bluealsa"
	OutputNoProfile  = "profile"
)

// QualifyOutput says whether the stack on bus can play to a speaker: bluez on the bus, and bluealsa with its
// a2dp-source profile. "" when it can, else the reason (OutputNo*).
func QualifyOutput(ctx context.Context, bus string) string {
	conn, err := connect(ctx, bus)
	if err != nil {
		return OutputNoBus
	}
	defer func() { _ = conn.Close() }()
	var owned bool
	if conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, bluezName).Store(&owned) != nil || !owned {
		return OutputNoBluez
	}
	var profiles dbus.Variant
	err = conn.Object(bluealsaName, bluealsaRoot).CallWithContext(ctx, propertiesGet, 0, ifaceManager, "Profiles").Store(&profiles)
	if err != nil {
		return OutputNoBluealsa
	}
	return profileReason(profiles)
}

// profileReason is "" when bluealsa's profiles hold a2dp-source
func profileReason(profiles dbus.Variant) string {
	list, _ := profiles.Value().([]string)
	if slices.Contains(list, bluetooth.TransportA2DPSource) {
		return ""
	}
	return OutputNoProfile
}

func (b *Backend) Changes() <-chan struct{} { return b.changes }

func (b *Backend) Close() error { return b.conn.Close() }

// gone: the service is not on the bus, or has no such object
func gone(err error) bool {
	var e dbus.Error
	if !errors.As(err, &e) {
		return false
	}
	switch e.Name {
	case "org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.NameHasNoOwner",
		"org.freedesktop.DBus.Error.UnknownObject", "org.freedesktop.DBus.Error.UnknownMethod":
		return true
	}
	return false
}

func (b *Backend) objects(ctx context.Context, service string, path dbus.ObjectPath) (managed, error) {
	var out managed
	err := b.conn.Object(service, path).CallWithContext(ctx, objectManager, 0).Store(&out)
	return out, err
}

func (b *Backend) Snapshot(ctx context.Context) (bluetooth.Snapshot, error) {
	objs, err := b.objects(ctx, bluezName, "/")
	switch {
	case gone(err):
		objs = nil
	case err != nil:
		return bluetooth.Snapshot{}, err
	}
	pcms, err := b.objects(ctx, bluealsaName, bluealsaRoot)
	if gone(err) {
		// an older bluealsa keeps its object manager at the root
		pcms, err = b.objects(ctx, bluealsaName, "/")
	}
	switch {
	case gone(err):
		return parse(b.adapter, objs, nil, false), nil
	case err != nil:
		return bluetooth.Snapshot{}, err
	}
	return parse(b.adapter, objs, pcms, true), nil
}

// parse is the snapshot from bluez's objects and bluealsa's, for the adapter alone
func parse(adapter dbus.ObjectPath, objs, pcms managed, bluealsa bool) bluetooth.Snapshot {
	snap := bluetooth.Snapshot{Bluealsa: bluealsa}
	devPrefix := string(adapter) + "/"
	// the devices whose transport takes avrcp absolute volume: bluez shows Volume only then
	hard := map[string]bool{}
	// MediaTransport1.Delay is in units of 1/10 ms; an absent property is no hint, not zero
	delays := map[string]int{}
	for path, ifaces := range objs {
		switch {
		case path == adapter && ifaces[ifaceAdapter] != nil:
			p := ifaces[ifaceAdapter]
			snap.Adapter = &bluetooth.Adapter{Path: string(path), Address: str(p, "Address"), Alias: str(p, "Alias"),
				Powered: boolean(p, "Powered"), Pairable: boolean(p, "Pairable"), Discoverable: boolean(p, "Discoverable")}
		case ifaces[ifaceDevice] != nil && strings.HasPrefix(string(path), devPrefix):
			p := ifaces[ifaceDevice]
			name := str(p, "Name")
			if name == "" {
				name = str(p, "Alias")
			}
			uuids, _ := p["UUIDs"].Value().([]string)
			rssi, _ := p["RSSI"].Value().(int16)
			snap.Devices = append(snap.Devices, bluetooth.Device{Path: string(path), Address: str(p, "Address"), Name: name,
				Icon: str(p, "Icon"), Class: u32(p, "Class"), Paired: boolean(p, "Paired"), Trusted: boolean(p, "Trusted"),
				Connected: boolean(p, "Connected"), AudioSink: slices.ContainsFunc(uuids, func(u string) bool {
					return strings.EqualFold(u, uuidAudioSink)
				}), RSSI: rssi})
		case ifaces[ifacePlayer] != nil && strings.HasPrefix(string(path), devPrefix):
			snap.Players = append(snap.Players, player(string(path), ifaces[ifacePlayer]))
		case ifaces[ifaceTransport] != nil && strings.HasPrefix(string(path), devPrefix):
			p := ifaces[ifaceTransport]
			if _, ok := p["Volume"]; ok {
				hard[objectPath(p, "Device")] = true
			}
			if raw, ok := p["Delay"]; ok {
				if delay, ok := raw.Value().(uint16); ok {
					delays[objectPath(p, "Device")] = int(delay) * 100
				}
			}
		}
	}
	for path, ifaces := range pcms {
		p := ifaces[ifacePCM]
		if p == nil || !strings.HasPrefix(objectPath(p, "Device"), devPrefix) {
			continue
		}
		device := objectPath(p, "Device")
		delay, known := delays[device]
		snap.PCMs = append(snap.PCMs, bluetooth.PCM{Path: string(path), Device: device,
			Transport: str(p, "Transport"), Mode: str(p, "Mode"), Running: boolean(p, "Running"), Rate: u32(p, "Sampling"),
			Channels: u8(p, "Channels"), Volume: u16(p, "Volume"), SoftVolume: boolean(p, "SoftVolume"),
			HardVolume: hard[device], DelayHintUs: delay, DelayKnown: known})
	}
	sortSnapshot(&snap)
	return snap
}

// map order aside, the same objects make the same snapshot
func sortSnapshot(s *bluetooth.Snapshot) {
	slices.SortFunc(s.Devices, func(x, y bluetooth.Device) int { return strings.Compare(x.Path, y.Path) })
	slices.SortFunc(s.Players, func(x, y bluetooth.Player) int { return strings.Compare(x.Path, y.Path) })
	slices.SortFunc(s.PCMs, func(x, y bluetooth.PCM) int { return strings.Compare(x.Path, y.Path) })
}

func player(path string, p map[string]dbus.Variant) bluetooth.Player {
	out := bluetooth.Player{Path: path, Device: objectPath(p, "Device"), Status: str(p, "Status"), PositionMs: u32(p, "Position")}
	if v, ok := p["Track"]; ok {
		if track, ok := v.Value().(map[string]dbus.Variant); ok {
			out.Title, out.Artist, out.Album = str(track, "Title"), str(track, "Artist"), str(track, "Album")
			out.DurationMs = u32(track, "Duration")
		}
	}
	return out
}

func (b *Backend) set(ctx context.Context, service string, path dbus.ObjectPath, iface, prop string, v any) error {
	if !path.IsValid() {
		return errors.New("bluez: invalid path")
	}
	return b.conn.Object(service, path).CallWithContext(ctx, propertiesSet, 0, iface, prop, dbus.MakeVariant(v)).Err
}

func (b *Backend) call(ctx context.Context, service string, path dbus.ObjectPath, method string, args ...any) error {
	if !path.IsValid() {
		return errors.New("bluez: invalid path")
	}
	return b.conn.Object(service, path).CallWithContext(ctx, method, 0, args...).Err
}

func (b *Backend) SetPowered(ctx context.Context, on bool) error {
	return b.set(ctx, bluezName, b.adapter, ifaceAdapter, "Powered", on)
}

func (b *Backend) SetAlias(ctx context.Context, alias string) error {
	return b.set(ctx, bluezName, b.adapter, ifaceAdapter, "Alias", alias)
}

func (b *Backend) SetWindowTimeouts(ctx context.Context, seconds uint32) error {
	if err := b.set(ctx, bluezName, b.adapter, ifaceAdapter, "DiscoverableTimeout", seconds); err != nil {
		return err
	}
	return b.set(ctx, bluezName, b.adapter, ifaceAdapter, "PairableTimeout", seconds)
}

func (b *Backend) SetPairableTimeout(ctx context.Context, seconds uint32) error {
	return b.set(ctx, bluezName, b.adapter, ifaceAdapter, "PairableTimeout", seconds)
}

func (b *Backend) SetPairable(ctx context.Context, on bool) error {
	return b.set(ctx, bluezName, b.adapter, ifaceAdapter, "Pairable", on)
}

func (b *Backend) SetDiscoverable(ctx context.Context, on bool) error {
	return b.set(ctx, bluezName, b.adapter, ifaceAdapter, "Discoverable", on)
}

func (b *Backend) SetDiscoveryFilter(ctx context.Context, transport string) error {
	filter := map[string]dbus.Variant{"Transport": dbus.MakeVariant(transport)}
	return b.call(ctx, bluezName, b.adapter, ifaceAdapter+".SetDiscoveryFilter", filter)
}

func (b *Backend) StartDiscovery(ctx context.Context) error {
	return b.call(ctx, bluezName, b.adapter, ifaceAdapter+".StartDiscovery")
}

func (b *Backend) StopDiscovery(ctx context.Context) error {
	return b.call(ctx, bluezName, b.adapter, ifaceAdapter+".StopDiscovery")
}

func (b *Backend) Pair(ctx context.Context, device string) error {
	return b.call(ctx, bluezName, dbus.ObjectPath(device), ifaceDevice+".Pair")
}

func (b *Backend) SetTrusted(ctx context.Context, device string, on bool) error {
	return b.set(ctx, bluezName, dbus.ObjectPath(device), ifaceDevice, "Trusted", on)
}

func (b *Backend) Connect(ctx context.Context, device string) error {
	return b.call(ctx, bluezName, dbus.ObjectPath(device), ifaceDevice+".Connect")
}

func (b *Backend) Disconnect(ctx context.Context, device string) error {
	return b.call(ctx, bluezName, dbus.ObjectPath(device), ifaceDevice+".Disconnect")
}

func (b *Backend) Remove(ctx context.Context, device string) error {
	return b.call(ctx, bluezName, b.adapter, ifaceAdapter+".RemoveDevice", dbus.ObjectPath(device))
}

func (b *Backend) SetSoftVolume(ctx context.Context, pcm string, on bool) error {
	return b.set(ctx, bluealsaName, dbus.ObjectPath(pcm), ifacePCM, "SoftVolume", on)
}

func (b *Backend) SetVolume(ctx context.Context, pcm string, v uint8) error {
	v = min(v, 127)
	return b.set(ctx, bluealsaName, dbus.ObjectPath(pcm), ifacePCM, "Volume", uint16(v)<<8|uint16(v))
}

func (b *Backend) Open(ctx context.Context, pcm string) (data, ctrl *os.File, err error) {
	path := dbus.ObjectPath(pcm)
	if !path.IsValid() {
		return nil, nil, errors.New("bluez: invalid path")
	}
	var d, c dbus.UnixFD
	if err := b.conn.Object(bluealsaName, path).CallWithContext(ctx, ifacePCM+".Open", 0).Store(&d, &c); err != nil {
		return nil, nil, err
	}
	if d < 0 || c < 0 {
		return nil, nil, errors.New("bluez: no fds")
	}
	return os.NewFile(uintptr(d), "bluealsa-pcm"), os.NewFile(uintptr(c), "bluealsa-ctrl"), nil //nolint:gosec // checked above
}

func (b *Backend) Control(ctx context.Context, player, method string) error {
	switch method {
	case "Play", "Pause", "Stop", "Next", "Previous":
	default:
		return errors.New("bluez: unknown player method")
	}
	return b.call(ctx, bluezName, dbus.ObjectPath(player), ifacePlayer+"."+method)
}

// RegisterAgent exports our agent once and makes it bluez's default, NoInputNoOutput
func (b *Backend) RegisterAgent(ctx context.Context, a bluetooth.Agent) error {
	b.agentMu.Lock()
	if b.agent == nil {
		b.agent = &agent{decide: a}
		if err := b.conn.Export(b.agent, agentPath, ifaceAgent); err != nil {
			b.agent = nil
			b.agentMu.Unlock()
			return err
		}
	}
	b.agentMu.Unlock()
	err := b.call(ctx, bluezName, "/org/bluez", ifaceAgentMgr+".RegisterAgent", dbus.ObjectPath(agentPath), "NoInputNoOutput")
	var e dbus.Error
	if err != nil && (!errors.As(err, &e) || e.Name != "org.bluez.Error.AlreadyExists") {
		return err
	}
	return b.call(ctx, bluezName, "/org/bluez", ifaceAgentMgr+".RequestDefaultAgent", dbus.ObjectPath(agentPath))
}

var _ bluetooth.Backend = (*Backend)(nil)

func str(p map[string]dbus.Variant, k string) string {
	s, _ := p[k].Value().(string)
	return s
}

func boolean(p map[string]dbus.Variant, k string) bool {
	v, _ := p[k].Value().(bool)
	return v
}

func u32(p map[string]dbus.Variant, k string) uint32 {
	v, _ := p[k].Value().(uint32)
	return v
}

func u16(p map[string]dbus.Variant, k string) uint16 {
	v, _ := p[k].Value().(uint16)
	return v
}

func u8(p map[string]dbus.Variant, k string) uint8 {
	v, _ := p[k].Value().(byte)
	return v
}

func objectPath(p map[string]dbus.Variant, k string) string {
	v, _ := p[k].Value().(dbus.ObjectPath)
	return string(v)
}
