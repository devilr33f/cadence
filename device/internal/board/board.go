package board

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"git.shino.boo/devilreef/cadence/device/internal/alarms"
	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/discovery"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/leds"
	"git.shino.boo/devilreef/cadence/device/internal/media"
	"git.shino.boo/devilreef/cadence/device/internal/session"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	"git.shino.boo/devilreef/cadence/device/internal/wifi"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

type Board interface {
	ID() string
	Fingerprint(ctx context.Context) error
	Capabilities() []capability.Provider
	// AudioIO is false on a board without a microphone and speaker.
	AudioIO() (audio.IO, bool)
}

var (
	ErrUnknownBoard = errors.New("unknown board")
	ErrFingerprint  = errors.New("board fingerprint mismatch")
)

func Find(boards []Board, id string) (Board, error) {
	for _, b := range boards {
		if b.ID() == id {
			return b, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrUnknownBoard, id)
}

// Sheller is optionally implemented by a board whose shell is elsewhere than /bin/sh (the yandex
// boards' busybox at /system/bin/sh); an update plan's exec steps run through it.
type Sheller interface{ Shell() string }

// SessionTracker is optionally implemented by a board whose capabilities ask
// how a session began (rule 7): core records each session it begins there.
type SessionTracker interface {
	Sessions() *session.Recent
}

// MediaReporter is optionally implemented by a board that plays media.
type MediaReporter interface {
	// MediaSessions delivers the latest media session state, dropping older
	// unread ones; it is nil while the board has no media.
	MediaSessions() <-chan *cadencev2.MediaSessionState
}

// MediaLinker is optionally implemented by a board whose media keeps covers on the server: core calls MediaLinked on
// each new link, as the server may have restarted and lost the covers it keeps in memory alone.
type MediaLinker interface{ MediaLinked() }

// Inputs is optionally implemented by a board with buttons, a mute switch or a wake engine. The board builds the
// input with its buttons, the switch's boot position and its volume, and hands it to its audiod supervisor; core
// runs it from boot, takes its triggers beside AudioIO's, and shows what it does on the lights.
type Inputs interface {
	// nil while the board has none
	Input() *input.Input
}

// Alarms is optionally implemented by a board whose speaker rings alarms and timers. The board builds the ringer over
// its book, its volume and its sessions, hands it to its input and its audiod supervisor, and reports its capability;
// core runs it from boot beside the input, ends the session a ring begins over, refuses activations while one rings
// and shows it on the lights.
type Alarms interface {
	// nil while the board has none
	Alarms() *alarms.Ringer
}

// Volume is optionally implemented by a board whose output level cadenced keeps: core puts the level in force back on
// the hardware before each reply and each minute while media plays (M6a-D8), and тише steps it down.
type Volume interface {
	// nil while the board has none
	Volume() *volume.Setter
}

// MediaPlayback is optionally implemented by a board that plays media: whether its media session plays now, and
// the player's actions, which the offline command words take while nothing streams (M6a-D6).
type MediaPlayback interface {
	MediaPlaying() bool
	// MediaPause pauses the playing session; the offline stop words
	MediaPause(ctx context.Context) error
	// MediaNext and MediaPrevious skip in the playing session; MediaResume continues it,
	// which restarts a queue that ended; the offline media words
	MediaNext(ctx context.Context) error
	MediaPrevious(ctx context.Context) error
	MediaResume(ctx context.Context) error
}

// StockCommandModel is the command spotter's model a stock root carries under
// spotter_models/command, the first one that has it; "" when none does. The
// stock firmware ships the model there, so no copy needs to be pushed.
func StockCommandModel(roots []string) string {
	for _, r := range roots {
		m := filepath.Join(r, "spotter_models", "command")
		if st, err := os.Stat(m); err == nil && st.IsDir() {
			return m
		}
	}
	return ""
}

// Visualizer is optionally implemented by a board whose audiod sends the music's spectrum: core draws it on the
// lights while they rest.
type Visualizer interface {
	// the latest frame, dropping older unread ones; nil while the board has no media
	MediaViz() <-chan media.VizFrame
}

// LinkPeer is optionally implemented by a board with a screen plane: core tells it when a link is up, with the
// link's send and the device's info from the Welcome, and hands it each link frame core does not take (rpc answers,
// assistant turns, cards, ducks, the device's info). LinkMessage runs on the link's read path and must never block;
// LinkDown follows each LinkUp once its link ended.
type LinkPeer interface {
	LinkUp(send func(*cadencev2.Envelope) error, info *cadencev2.DeviceInfo)
	LinkMessage(env *cadencev2.Envelope) bool
	LinkDown()
}

// DeviceNamer is optionally implemented by a board whose receivers (AirPlay, Spotify Connect) advertise the device's
// name while their configs set none: core hands it the name kept from the last link at boot, then the server's at
// each link and at each rename ("" while the device has none). It runs on the link's read path and must never block.
type DeviceNamer interface {
	SetDeviceName(name string)
}

// ServerOrigin is optionally implemented by a board with a screen plane: core names the origin it links to (the
// config's serverUrl) before the link runs, which the screen fetches images from.
type ServerOrigin interface {
	SetServerURL(origin string)
}

// ServerDucker is optionally implemented by a board that plays media: the server's Duck lowers its own mix while a
// turn in its room listens or speaks. It never raises a level (rule 7) and must never block.
type ServerDucker interface {
	ServerDuck(d *cadencev2.Duck)
}

// Bluetooth is optionally implemented by a board whose Bluetooth adapter cadenced runs (capability bluetooth, receive
// only): the board builds the capability on this profile beside its other receivers, hands it to its audiod
// supervisor and its device name, and reports it; a board without one reports no bluetooth.
type Bluetooth interface {
	Bluetooth() BluetoothProfile
}

// WiFi is optionally implemented by a board whose wi-fi cadenced can manage (capability network.wifi): the board
// builds the capability on this profile and reports it; a board without one reports no network.wifi. the yandex
// boards run plain wpa_supplicant with its control socket, so their profile dials wpa_cli; nothing else on those
// rewrites the supplicant's conf, and the capability never removes a network: the vendor's own stays as the
// fallback a failed switch rolls back to.
type WiFi interface {
	WiFi() WiFiProfile
}

// LinkGate is optionally implemented by a board that fakes a network with no route to the server (the sim's
// no-server wi-fi): while LinkBlocked says so, every dial of the link fails as an unreachable server's would.
type LinkGate interface {
	LinkBlocked() bool
}

// WiFiProfile is how to reach a board's wi-fi.
type WiFiProfile struct {
	// the wireless interface, wlan0
	Interface string
	// Dial reaches the board's wi-fi mechanism (the sim's is a fake)
	Dial func(ctx context.Context) (wifi.Backend, error)
}

// BluetoothProfile is where a board's adapter is and how to reach it.
type BluetoothProfile struct {
	// the system bus's address, "" for the usual /var/run/dbus/system_bus_socket
	Bus string
	// the adapter's name, hci0
	Adapter string
	// Dial reaches bluez and bluealsa on the bus (the sim's is a fake)
	Dial func(ctx context.Context) (bluetooth.Backend, error)
}

// Availabilities is optionally implemented by a board whose capabilities' hardware comes and goes while it runs (a
// screen on a hot-plugged hdmi port): core answers an unavailable capability's commands "unavailable" and tells the
// server on its state deltas; nothing is reported again. nil means every capability is always available.
type Availabilities interface {
	Availability() *capability.Availability
}

// Lights is optionally implemented by a board with lights: the sink (which
// also says, through its geometry, which parts the board has) is drawn on by
// the leds renderer, and false means no lights.
type Lights interface {
	Lights() (leds.Sink, bool)
}

// Advertiser is optionally implemented by a board whose system answers mDNS itself (android's NsdManager, in the
// app): network.discovery advertises through it instead of cadenced's own responder.
type Advertiser interface {
	Advertise(ctx context.Context, info discovery.Info, log *slog.Logger, ready func()) error
}

// Telemetry is optionally implemented by a board that has more to report than cadenced's own logs.
type Telemetry interface {
	Telemetry() TelemetryProfile
}

// TelemetryProfile says what a board adds to the logs and system metrics it reports.
type TelemetryProfile struct {
	// the kernel log is readable and worth shipping
	Kmsg bool
	// extra files tailed as log sources
	LogFiles []LogFile
	// filesystems whose usage is reported
	Mounts []string
	// a file in milli-degrees celsius, "" for none
	TempSensor string
}

// LogFile is a file tailed as the log source named Source.
type LogFile struct{ Source, Path string }
