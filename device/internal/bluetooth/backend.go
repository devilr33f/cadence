package bluetooth

import (
	"context"
	"os"
)

// Backend is what the capability needs of BlueZ and bluealsa. Its methods are safe for concurrent use and return once
// ctx is done. Paths are D-Bus object paths (a fake's are its own); Snapshot reads everything at once, and Changes
// says when it may have changed, so the capability reconciles from one read and never tracks signals itself.
type Backend interface {
	Snapshot(ctx context.Context) (Snapshot, error)
	// Changes gets a value, coalesced, whenever a snapshot may differ: a property, an object added or removed, a
	// service on the bus come or gone. It closes once the backend is lost (the bus went away).
	Changes() <-chan struct{}

	SetPowered(ctx context.Context, on bool) error
	SetAlias(ctx context.Context, alias string) error
	// SetWindowTimeouts sets DiscoverableTimeout and PairableTimeout, so bluez itself closes a window
	SetWindowTimeouts(ctx context.Context, seconds uint32) error
	// SetPairableTimeout sets PairableTimeout alone: bluez turns pairable off itself after it, should we die first
	SetPairableTimeout(ctx context.Context, seconds uint32) error
	// SetPairable is mgmt's bondable setting too: a pairing made while it is off stores no link key
	SetPairable(ctx context.Context, on bool) error
	SetDiscoverable(ctx context.Context, on bool) error
	// RegisterAgent makes a the default agent (NoInputNoOutput); called again, it registers anew (bluez restarted)
	RegisterAgent(ctx context.Context, a Agent) error

	// SetDiscoveryFilter limits discovery to a transport: "bredr" for a2dp speakers
	SetDiscoveryFilter(ctx context.Context, transport string) error
	StartDiscovery(ctx context.Context) error
	StopDiscovery(ctx context.Context) error
	// Pair pairs a device as the initiator; our agent answers bluez's questions about it
	Pair(ctx context.Context, device string) error
	SetTrusted(ctx context.Context, device string, on bool) error
	Connect(ctx context.Context, device string) error
	Disconnect(ctx context.Context, device string) error
	Remove(ctx context.Context, device string) error

	SetSoftVolume(ctx context.Context, pcm string, on bool) error
	// SetVolume sets both channels to v, 0-127, unmuted
	SetVolume(ctx context.Context, pcm string, v uint8) error
	// Open opens the pcm: data is the read end of its audio pipe, ctrl its control socket, which keeps the pcm open
	Open(ctx context.Context, pcm string) (data, ctrl *os.File, err error)
	// Control runs a MediaPlayer1 method on player: Play, Pause, Stop, Next or Previous
	Control(ctx context.Context, player, method string) error

	// RegisterPlayer exports our own media player and registers it on the adapter, so a speaker's play, pause, next
	// and previous buttons reach c rather than a key device nobody reads; called again, it registers anew (bluez
	// restarted)
	RegisterPlayer(ctx context.Context, c PlayerControl) error
	// SetPlayerStatus is our player's status as the speaker sees it: PlayerPlaying, PlayerPaused or PlayerStopped
	SetPlayerStatus(ctx context.Context, status string) error

	Close() error
}

// Agent decides bluez's pairing and authorization requests; it must never block.
type Agent interface {
	Authorize(req AgentRequest) bool
}

// PlayerControl hears our player's methods as a speaker called them (Play, Pause, PlayPause, Stop, Next, Previous);
// it must never block.
type PlayerControl interface {
	PlayerCommand(method string)
}

// our player's statuses, mpris's PlaybackStatus
const (
	PlayerPlaying = "Playing"
	PlayerPaused  = "Paused"
	PlayerStopped = "Stopped"
)

// AgentRequest is one call of our org.bluez.Agent1.
type AgentRequest struct {
	// the device's object path and address
	Device, Address string
	// pairing (RequestAuthorization, RequestConfirmation, RequestPinCode, RequestPasskey) or service
	// (AuthorizeService)
	Kind string
}

const (
	RequestPairing = "pairing"
	RequestService = "service"
)

// Snapshot is the adapter, its devices and players, and bluealsa's pcms.
type Snapshot struct {
	// nil while bluez has no such adapter (or bluez is not on the bus)
	Adapter *Adapter
	Devices []Device
	Players []Player
	// Bluealsa is false while org.bluealsa is not on the bus
	Bluealsa bool
	PCMs     []PCM
}

type Adapter struct {
	Path, Address, Alias            string
	Powered, Pairable, Discoverable bool
}

type Device struct {
	Path, Address, Name, Icon  string
	Class                      uint32
	Paired, Trusted, Connected bool
	// AudioSink: its UUIDs name the a2dp sink (0000110b), so we can play to it
	AudioSink bool
	// dBm while discovery sees it, 0 when bluez has none
	RSSI int16
}

// PCM is an org.bluealsa.PCM1.
type PCM struct {
	Path string
	// the Device1 path it belongs to
	Device string
	// "A2DP-sink" for a phone playing to us
	Transport, Mode string
	Running         bool
	Rate            uint32
	Channels        uint8
	// bluealsa's: channel 1 in the upper byte, channel 2 in the lower, each 0-127 with 0x80 for mute
	Volume     uint16
	SoftVolume bool
	// HardVolume: the remote takes avrcp absolute volume (its bluez transport has a Volume), so with SoftVolume off
	// Volume is the speaker's own
	HardVolume bool
	// the bluez MediaTransport1.Delay of an a2dp source, in us; DelayKnown is false when bluez does not report it
	DelayHintUs int
	DelayKnown  bool
}

// Player is an org.bluez.MediaPlayer1.
type Player struct {
	Path, Device string
	// playing, paused, stopped, forward-seek, reverse-seek or error
	Status                 string
	Title, Artist, Album   string
	DurationMs, PositionMs uint32
}

const (
	// TransportA2DPSink is the pcm a phone streams into.
	TransportA2DPSink = "A2DP-sink"
	// TransportA2DPSource is the pcm we stream to a speaker through.
	TransportA2DPSource = "A2DP-source"
)
