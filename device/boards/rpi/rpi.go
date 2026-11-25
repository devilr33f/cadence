// Package rpi is a Raspberry Pi with cadence-screen on HDMI, audiod on HDMI audio or a Bluetooth speaker, evdev
// remotes and the TV's remote over CEC (/dev/cec0). It has no microphone of its own; its room's speaker listens.
// Rule 7: only a press moves the level; only a woken session pairs, connects or routes a speaker.
package rpi

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/cec"
	"git.shino.boo/devilreef/cadence/device/internal/evdev"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/screen"
	"git.shino.boo/devilreef/cadence/device/internal/screen/tvboard"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
)

const (
	ID           = "rpi"
	defaultLevel = 30
	// CADENCE_RPI_ALSA_DEVICE names another HDMI or alsa output; a Bluetooth speaker replaces it while active
	alsaDevice = "hdmi:CARD=vc4hdmi0"
	// 20 ms periods into a 120 ms buffer
	playbackPeriod = 960
	playbackBuffer = 5760
	// linux input codes
	keyVolumeDown = 114
	keyVolumeUp   = 115
)

// the input devices whose volume keys are the board's: the pi's IR receiver and the common bluetooth remotes;
// CADENCE_RPI_REMOTES (comma-separated driver names) names others
var defaultRemotes = []string{"gpio_ir_recv", "vc4-hdmi-0", "Xiaomi RC Consumer Control", "BT Remote"}

type options struct {
	root string
	// tests: the screen's unit and the hdmi poll's period
	screenControl screenControl
	hdmiEvery     time.Duration
	hdmiSettle    time.Duration
	audiod        string
	logsEnv       telemetry.LogsEnv
	metricsEnv    telemetry.MetricsEnv
	bluetoothOptions
}

type Option func(*options)

// WithAudiod runs this audiod binary; "" runs none.
func WithAudiod(binary string) Option             { return func(o *options) { o.audiod = binary } }
func WithLogs(env telemetry.LogsEnv) Option       { return func(o *options) { o.logsEnv = env } }
func WithMetrics(env telemetry.MetricsEnv) Option { return func(o *options) { o.metricsEnv = env } }

// withRoot reads the device under root, for tests
func withRoot(root string) Option { return func(o *options) { o.root = root } }

// Board is the pi.
type Board struct {
	*tvboard.Board
	o          options
	log        *slog.Logger
	dataDir    string
	hdmiDevice string
	bt         boardBluetooth
	// the hdmi sink: which capabilities it makes available, the cec controller it rescans and the unit it runs
	avail      *capability.Availability
	cec        *cec.Controller
	screenUnit string
	hp         screenHotplug
}

// New returns the pi board; dataDir is the config's, and the sockets live in its run dir. A nil log discards.
func New(getenv func(string) string, dataDir string, log *slog.Logger, opts ...Option) *Board {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	var o options
	for _, f := range opts {
		f(&o)
	}
	run := filepath.Join(dataDir, "run")
	device := alsaDevice
	if d := getenv("CADENCE_RPI_ALSA_DEVICE"); d != "" {
		device = d
	}
	b := &Board{o: o, log: log, dataDir: dataDir, hdmiDevice: device, avail: capability.NewAvailability(), screenUnit: screenUnit}
	if u := getenv("CADENCE_RPI_SCREEN_UNIT"); u == "none" {
		b.screenUnit = ""
	} else if u != "" {
		b.screenUnit = u
	}
	names := defaultRemotes
	if r := getenv("CADENCE_RPI_REMOTES"); r != "" {
		names = strings.Split(r, ",")
	}
	topts := tvboard.Options{
		ID: ID, Fingerprint: b.fingerprint, DataDir: dataDir, DefaultLevel: defaultLevel,
		ScreenSocket: filepath.Join(run, "screen.sock"), TapPath: filepath.Join(run, "tap.sock"),
		Output:     capschema.MediaGroupOutput{ID: capschema.GroupOutputHDMI, Kind: capschema.GroupOutputHDMI},
		Components: screen.ReadComponents(manifestPath()),
		Telemetry: board.TelemetryProfile{
			Kmsg: true, Mounts: []string{"/", dataDir}, TempSensor: under(o.root, "/sys/class/thermal/thermal_zone0/temp"),
		},
		LogsEnv: o.logsEnv, MetricsEnv: o.metricsEnv, Proc: under(o.root, "/proc"), Log: log,
	}
	// /dev/cec0: its volume keys are the board's buttons, its other keys the screen's, and its tv and avr
	// the display.cec capability the dashboard drives
	wire := cec.OpenLinux(under(o.root, "/dev/cec0"), log)
	b.cec = cec.New(wire, log)
	topts.Extra = append(topts.Extra, b.cec)
	topts.Remotes = &remotes{
		layout: layout(o.root, names),
		cec:    newCECRemote(wire, log, func(e *screenv1.InputEvent) { b.Screen().Inject(e) }),
		log:    log, board: b, events: make(chan input.ButtonEvent, 16),
	}
	if o.audiod != "" {
		topts.AudiodBinary = o.audiod
		topts.AudiodSocket = filepath.Join(run, "audiod.sock")
		topts.Configure = configure(device)
		topts.AudiodHooks = &tvboard.AudiodHooks{Options: b.audiodOptions, NewSupervisor: b.newSupervisor}
	}
	b.Board = tvboard.New(topts)
	return b
}

// configure is audiod's HDMI Configure on a pi: playback alone, a software gain (no mixer control: the tv keeps its
// own volume), stereo. routeConfigure swaps its device for the active Bluetooth speaker.
func configure(device string) *audiodv1.Configure {
	return &audiodv1.Configure{
		Output: &audiodv1.OutputConfig{
			Backend: audiodv1.Backend_BACKEND_ALSA, Device: device, Channels: 2,
			PeriodFrames: playbackPeriod, BufferFrames: playbackBuffer,
		},
		CaFile: "/etc/ssl/certs/ca-certificates.crt",
	}
}

// manifestPath is the artifact's MANIFEST beside cadenced's bin dir
func manifestPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(filepath.Dir(exe)), "MANIFEST")
}

func under(root, p string) string {
	if root == "" {
		return p
	}
	return filepath.Join(root, p)
}

// Availability is which capabilities the hdmi sink makes available now (board.Availabilities).
func (b *Board) Availability() *capability.Availability { return b.avail }

// fingerprint takes a pi by its device tree's model
func (b *Board) fingerprint(context.Context) error {
	model, err := os.ReadFile(under(b.o.root, "/proc/device-tree/model"))
	if err != nil || !strings.HasPrefix(string(model), "Raspberry Pi") {
		return board.ErrFingerprint
	}
	return nil
}

func layout(root string, names []string) evdev.Layout {
	l := evdev.Layout{
		SysClass: under(root, "/sys/class/input"), Nodes: under(root, "/dev/input"),
		Keys: map[uint16]input.Kind{keyVolumeUp: input.VolumeUp, keyVolumeDown: input.VolumeDown},
	}
	for i, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			l.Devices = append(l.Devices, evdev.Device{Role: "remote" + string(rune('0'+i%10)), Name: n})
		}
	}
	return l
}

// remotes are the evdev remotes' and cec's volume keys as one Buttons; cec's other keys go to the screen
// (the cecRemote's subscription), and the wire itself is display.cec's to start
type remotes struct {
	layout evdev.Layout
	cec    *cecRemote
	log    *slog.Logger
	board  *Board
	events chan input.ButtonEvent
}

func (r *remotes) Events() <-chan input.ButtonEvent { return r.events }

func (r *remotes) Start(ctx context.Context, wg *sync.WaitGroup) {
	out := r.events
	buttons, _ := evdev.Open(r.layout, r.log, func(*os.File) (bool, bool) { return false, false })
	buttons.Start(ctx, wg)
	for _, src := range []<-chan input.ButtonEvent{buttons.Events(), r.cec.Events()} {
		wg.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case e, ok := <-src:
					if !ok {
						return
					}
					select {
					case out <- e:
					case <-ctx.Done():
						return
					}
				}
			}
		})
	}
	wg.Go(func() {
		<-ctx.Done()
		buttons.Close()
	})
}
