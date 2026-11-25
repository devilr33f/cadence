// Package midi is the Yandex Station Midi board: its fingerprint, the paths cadence keeps there, audiod's
// configuration for its microphones, speaker and stock voice engines, its buttons and mute switch, and its ring. Its
// userland is 32-bit arm on a 64-bit kernel. The vendor assistant stays installed, and suspending it is the boot
// wrapper's (M2m-D2): cadenced never signals a vendor process. It is the one place that names the midi's hardware.
package midi

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"git.shino.boo/devilreef/cadence/device/internal/airplay"
	"git.shino.boo/devilreef/cadence/device/internal/alarms"
	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
	"git.shino.boo/devilreef/cadence/device/internal/bluetooth/bluez"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/evdev"
	"git.shino.boo/devilreef/cadence/device/internal/group"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/leds"
	"git.shino.boo/devilreef/cadence/device/internal/media"
	"git.shino.boo/devilreef/cadence/device/internal/session"
	"git.shino.boo/devilreef/cadence/device/internal/soloist"
	"git.shino.boo/devilreef/cadence/device/internal/soloist/updater"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	"git.shino.boo/devilreef/cadence/device/internal/wakeword"
	"git.shino.boo/devilreef/cadence/device/internal/wifi"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// Board is the midi. Every board is built at each start and only the chosen one runs, so New touches nothing on the
// device: the first call after the fingerprint opens the devices and builds the rest.
type Board struct {
	log *slog.Logger
	// the config's data dir, which core keeps its own state under
	dataDir    string
	root       string
	audiod     string
	readSwitch func(*os.File) (on, ok bool)
	// soloist's cdn for the board's paths; a nil one never downloads a build
	updates func(Paths) *soloist.Updates
	paths   Paths

	setupOnce sync.Once
	vol       *volume.Setter
	sessions  *session.Recent
	buttons   *evdev.Buttons
	input     *input.Input
	media     *media.Media
	soloist   *soloist.Soloist
	airplay   *airplay.Airplay
	bt        *bluetooth.Bluetooth
	wifiCap   *wifi.Capability
	group     *group.Group
	ringer    *alarms.Ringer
	lights    *lightSink
	caps      []capability.Provider
	// with a pipeline only
	logsEnv telemetry.LogsEnv
	logs    *telemetry.Logs
	// with a client only
	metricsEnv telemetry.MetricsEnv
	metrics    *telemetry.Metrics

	audioOnce sync.Once
	io        audio.IO
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

type Option func(*Board)

// WithLogs gives the board its logs capability, which reads its kernel log and hands audiod's lines to env's pipeline;
// a zero env gives none.
func WithLogs(env telemetry.LogsEnv) Option { return func(b *Board) { b.logsEnv = env } }

// WithMetrics gives the board its system.metrics capability, which exports through env's client, and has its input,
// ringer and audiod count into env's counts; a zero env gives none.
func WithMetrics(env telemetry.MetricsEnv) Option { return func(b *Board) { b.metricsEnv = env } }

// WithAudiod runs this audiod binary instead of the one under the data root; "" keeps that one.
func WithAudiod(binary string) Option {
	return func(b *Board) {
		if binary != "" {
			b.audiod = binary
		}
	}
}

// New returns the midi board. dataDir is the config's: the board keeps its state under its own data root, where its
// installer and boot wrapper look, and logs a config that names another once it runs. A nil log discards.
func New(dataDir string, log *slog.Logger, opts ...Option) *Board {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	b := &Board{log: log, dataDir: dataDir, readSwitch: readSwitch, updates: spotifyCDN}
	for _, o := range opts {
		o(b)
	}
	b.paths = pathsUnder(b.root)
	if b.audiod == "" {
		b.audiod = b.paths.Audiod
	}
	return b
}

// spotifyCDN is where spotify serves soloist for arm64, verified against the shipped bundle alone: soloist runs on
// the 64-bit kernel beside the 32-bit userland, in its own arm64 runtime
func spotifyCDN(p Paths) *soloist.Updates {
	return &soloist.Updates{URL: updater.SpotifyARM64, CAFile: p.CABundle, Machine: updater.MachineAArch64}
}

func (*Board) ID() string { return "yandex-midi" }

// Paths are where cadence lives on this midi.
func (b *Board) Paths() Paths { return b.paths }

// Shell is the vendor's busybox an update plan's exec steps run through.
func (b *Board) Shell() string { return "/system/bin/sh" }

// setup opens the buttons, reading the mute switch before the input exists, so the first MuteState any audiod gets
// is the real position, and builds everything the capabilities need.
func (b *Board) setup() {
	b.setupOnce.Do(func() {
		if b.dataDir != "" && filepath.Clean(b.dataDir) != filepath.Clean(b.paths.DataRoot) {
			b.log.Warn("data_dir_mismatch")
		}
		b.ctx, b.cancel = context.WithCancel(context.Background())
		b.vol = volume.NewSetter(volume.NewStore(b.paths.DataRoot), defaultLevel, b.log)
		b.sessions = &session.Recent{}
		var muted bool
		b.buttons, muted = evdev.Open(buttons(b.root), b.log, b.readSwitch)
		countRings := func(o *alarms.RingerOptions) { o.Counts = b.metricsEnv.App }
		b.ringer = alarms.Open(b.paths.DataRoot, b.vol, b.sessions, b.log, countRings)
		b.input = input.New(input.Options{
			Buttons: b.buttons, Muted: muted, Volume: b.vol, Ringer: b.ringer, Counts: b.metricsEnv.App, Log: b.log,
		})
		b.buttons.Start(b.ctx, &b.wg)
		b.media = media.New(b.log)
		// airplay's covers: audiod writes them into its scratch, cadenced uploads them
		b.media.UseArtwork(filepath.Join(b.paths.AirplayScratch, "run", "artwork"), media.ServerUploader(b.metricsEnv.Client))
		b.soloist = soloist.New(b.vol, soloist.Options{
			Dir: b.paths.Soloist, ScratchDir: b.paths.SoloistScratch, CAFile: b.paths.CABundle, Updates: b.updates(b.paths),
			Ringing: b.ringer.Ringing,
		}, b.log)
		b.airplay = airplay.New(airplay.Options{Dir: b.paths.Airplay, ScratchDir: b.paths.AirplayScratch}, b.log)
		b.lights = &lightSink{root: b.root, open: openFramebuffer}
		// no trim in the board's Configure: audio.output's delayMs alone sets one
		output := volume.NewOutput(b.vol, b.sessions, outputChannels, maxBitrate, volume.WithDelay(0, b.media.SetOutputDelay))
		b.caps = append([]capability.Provider{output, b.input.Capability()}, b.media.Capabilities()...)
		// the custom wake word, off until a config enables it
		b.caps = append(b.caps, wakeword.Attach(b.input, wakeword.Options{DataDir: b.paths.DataRoot,
			Get: wakeword.ServerGetter(b.metricsEnv.Client), Log: b.log}))
		b.group = group.New(b.log)
		b.group.SetOutput(capschema.MediaGroupOutput{ID: capschema.GroupOutputSpeaker, Kind: capschema.GroupOutputSpeaker})
		b.media.WatchSession(b.group.MediaSession)
		b.bt = bluetooth.New(bluetooth.Options{
			Dial: b.Bluetooth().Dial, Senders: b.soloist, Level: b.vol.Current, Sessions: b.sessions,
		}, b.log)
		b.wifiCap = wifi.NewCapability(b.ctx, b.WiFi().Dial, wifi.SettingsPath(b.dataDir), b.log)
		b.caps = append(b.caps, b.soloist.Capability(), b.airplay.Capability(), b.bt.Capability(), b.group.Capability(),
			b.ringer.Capability(), b.wifiCap)
		if b.logsEnv.Sink != nil {
			b.logs = telemetry.NewLogs(telemetry.LogsOptions{
				Sink: b.logsEnv.Sink, Profile: b.Telemetry(), KmsgPath: b.paths.Kmsg, Log: b.log,
			})
			b.caps = append(b.caps, b.logs)
			b.wg.Go(func() { b.logs.Run(b.ctx) })
		}
		if b.metricsEnv.Client != nil {
			b.metrics = telemetry.NewMetrics(telemetry.MetricsOptions{
				Env: b.metricsEnv, Profile: b.Telemetry(), Proc: b.paths.Proc, MediaPlaying: b.media.Playing,
			})
			b.caps = append(b.caps, b.metrics)
			b.wg.Go(func() { b.metrics.Run(b.ctx) })
		}
	})
}

func (b *Board) Capabilities() []capability.Provider {
	b.setup()
	return b.caps
}

// Input acts on the buttons, the mute switch and audiod's wake and command words.
func (b *Board) Input() *input.Input {
	b.setup()
	return b.input
}

// SetDeviceName is the device's name, which AirPlay and Spotify Connect advertise while their configs set none
// (board.DeviceNamer).
func (b *Board) SetDeviceName(name string) {
	b.setup()
	b.soloist.SetDeviceName(name)
	b.airplay.SetDeviceName(name)
	b.bt.SetDeviceName(name)
}

var _ board.DeviceNamer = (*Board)(nil)

// Bluetooth is the w1 combo's adapter, with stock bluez 5.70 and bluealsa 4.1.1 on the system bus (midi's /var/run
// links to /run). The bus is under the board's root, so a test's tree never reaches a host's bus.
func (b *Board) Bluetooth() board.BluetoothProfile {
	p := board.BluetoothProfile{Bus: "unix:path=" + under(b.root, systemBus), Adapter: "hci0"}
	p.Dial = func(ctx context.Context) (bluetooth.Backend, error) {
		be, err := bluez.Dial(ctx, p.Bus, p.Adapter)
		if err != nil {
			return nil, err
		}
		return be, nil
	}
	return p
}

var _ board.Bluetooth = (*Board)(nil)

// WiFi is the station's own link. the midi has no android framework wifi: S40wifi starts a plain wpa_supplicant
// whose conf lives on the data partition with update_config=1, so save_config persists and nothing else rewrites
// it while the stock assistant stays suspended. wpa_cli speaks to its control socket; cadenced runs as root
// beside it. the path sits under the board's root, so a test's tree can hold a fake wpa_cli.
func (b *Board) WiFi() board.WiFiProfile {
	cli := under(b.root, wifi.WpaCLIPath)
	p := board.WiFiProfile{Interface: "wlan0"}
	p.Dial = func(context.Context) (wifi.Backend, error) {
		if _, err := os.Stat(cli); err != nil {
			return nil, err
		}
		return wifi.NewWpaCLI(cli, p.Interface), nil
	}
	return p
}

var _ board.WiFi = (*Board)(nil)

// Sessions is where core records the sessions it begins.
func (b *Board) Sessions() *session.Recent {
	b.setup()
	return b.sessions
}

// Alarms rings the alarms and timers on the speaker.
func (b *Board) Alarms() *alarms.Ringer {
	b.setup()
	return b.ringer
}

// MediaLinked puts the covers the server keeps back after it restarted (board.MediaLinker)
func (b *Board) MediaLinked() {
	if b.media != nil {
		b.media.Relinked()
	}
}

func (b *Board) MediaSessions() <-chan *cadencev2.MediaSessionState {
	b.setup()
	return b.media.Sessions()
}

// MediaViz delivers the music's spectrum frames for the lights.
func (b *Board) MediaViz() <-chan media.VizFrame {
	b.setup()
	return b.media.Viz()
}

// MediaPlaying is whether the media session plays now.
func (b *Board) MediaPlaying() bool {
	b.setup()
	return b.media.Playing()
}

// MediaPause pauses the playing media session: the offline stop words.
func (b *Board) MediaPause(ctx context.Context) error {
	b.setup()
	return b.media.Action(ctx, "pause")
}
func (b *Board) MediaNext(ctx context.Context) error { b.setup(); return b.media.Action(ctx, "next") }
func (b *Board) MediaPrevious(ctx context.Context) error {
	b.setup()
	return b.media.Action(ctx, "previous")
}
func (b *Board) MediaResume(ctx context.Context) error {
	b.setup()
	return b.media.Action(ctx, "resume")
}

// Volume is the level the board keeps, which core puts back on the hardware.
func (b *Board) Volume() *volume.Setter {
	b.setup()
	return b.vol
}

// Lights are the ring alone.
func (b *Board) Lights() (leds.Sink, bool) {
	b.setup()
	return b.lights, true
}

// AudioIO starts audiod on its first call; it runs after the stored configs were restored. Close stops it.
func (b *Board) AudioIO() (audio.IO, bool) {
	b.setup()
	b.audioOnce.Do(func() { b.io = b.startAudiod() })
	return b.io, true
}

// Close stops audiod, soloist and the button readers, and lets the framebuffer go; nothing starts after it.
func (b *Board) Close() error {
	b.audioOnce.Do(func() {})
	if b.cancel == nil {
		return nil
	}
	b.cancel()
	b.buttons.Close()
	b.wg.Wait()
	b.lights.close()
	return nil
}

// Telemetry reports the kernel log and the usage of /data and /tmp. v1 read no temperature sensor and shipped no extra
// log file, so there are none.
func (b *Board) Telemetry() board.TelemetryProfile {
	return board.TelemetryProfile{Kmsg: true, Mounts: []string{"/data", "/tmp"}}
}

// ServerDuck lowers the music while a turn in the room listens or speaks; never a raise (rule 7)
func (b *Board) ServerDuck(d *cadencev2.Duck) {
	if b.media != nil {
		b.media.ServerDuck(d)
	}
}
