// Package vkcapsula is the VK Capsula Mini board: voice, URL playback, alarms, buttons with a mute toggle and
// telemetry on an Amlogic A113. Lights come separately.
package vkcapsula

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"git.shino.boo/devilreef/cadence/device/internal/alarms"
	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/evdev"
	"git.shino.boo/devilreef/cadence/device/internal/group"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/leds"
	"git.shino.boo/devilreef/cadence/device/internal/media"
	"git.shino.boo/devilreef/cadence/device/internal/session"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	"git.shino.boo/devilreef/cadence/device/internal/wakeword"
	"git.shino.boo/devilreef/cadence/device/internal/wifi"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

type Board struct {
	log                   *slog.Logger
	dataDir, root, audiod string
	paths                 Paths
	setupOnce             sync.Once
	vol                   *volume.Setter
	sessions              *session.Recent
	buttons               *evdev.Buttons
	input                 *input.Input
	media                 *media.Media
	group                 *group.Group
	ringer                *alarms.Ringer
	caps                  []capability.Provider
	logsEnv               telemetry.LogsEnv
	logs                  *telemetry.Logs
	metricsEnv            telemetry.MetricsEnv
	metrics               *telemetry.Metrics
	lightsOnce            sync.Once
	lights                *lightSink
	audioOnce             sync.Once
	io                    audio.IO
	ctx                   context.Context
	cancel                context.CancelFunc
	wg                    sync.WaitGroup
}

type Option func(*Board)

func WithLogs(env telemetry.LogsEnv) Option       { return func(b *Board) { b.logsEnv = env } }
func WithMetrics(env telemetry.MetricsEnv) Option { return func(b *Board) { b.metricsEnv = env } }
func WithAudiod(binary string) Option {
	return func(b *Board) {
		if binary != "" {
			b.audiod = binary
		}
	}
}

func New(dataDir string, log *slog.Logger, opts ...Option) *Board {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	b := &Board{log: log, dataDir: dataDir}
	for _, o := range opts {
		o(b)
	}
	b.paths = pathsUnder(b.root)
	if b.audiod == "" {
		b.audiod = b.paths.Audiod
	}
	return b
}

func (*Board) ID() string     { return "vk-capsula-mini" }
func (b *Board) Paths() Paths { return b.paths }

func (b *Board) setup() {
	b.setupOnce.Do(func() {
		if b.dataDir != "" && filepath.Clean(b.dataDir) != filepath.Clean(b.paths.DataRoot) {
			b.log.Warn("data_dir_mismatch")
		}
		b.ctx, b.cancel = context.WithCancel(context.Background())
		b.vol = volume.NewSetter(volume.NewStore(b.paths.DataRoot), defaultLevel, b.log)
		b.sessions = &session.Recent{}
		var muted bool
		b.buttons, muted = evdev.Open(buttons(b.root, b.loadMute(), b.saveMute), b.log, nil)
		countRings := func(o *alarms.RingerOptions) { o.Counts = b.metricsEnv.App }
		b.ringer = alarms.Open(b.paths.DataRoot, b.vol, b.sessions, b.log, countRings)
		b.input = input.New(input.Options{Buttons: b.buttons, Muted: muted, Volume: b.vol, Ringer: b.ringer,
			Counts: b.metricsEnv.App, Log: b.log})
		b.buttons.Start(b.ctx, &b.wg)
		b.media = media.New(b.log)
		b.group = group.New(b.log)
		b.media.WatchSession(b.group.MediaSession)
		output := volume.NewOutput(b.vol, b.sessions, outputChannels, maxBitrate,
			volume.WithDelay(0, b.media.SetOutputDelay))
		b.caps = append([]capability.Provider{output, b.input.Capability()}, b.media.Capabilities()...)
		// the custom wake word, off until a config enables it
		b.caps = append(b.caps, wakeword.Attach(b.input, wakeword.Options{DataDir: b.paths.DataRoot,
			Get: wakeword.ServerGetter(b.metricsEnv.Client), Log: b.log}))
		b.caps = append(b.caps, b.group.Capability(), b.ringer.Capability(),
			wifi.NewCapability(b.ctx, b.WiFi().Dial, wifi.SettingsPath(b.dataDir), b.log))
		if b.logsEnv.Sink != nil {
			b.logs = telemetry.NewLogs(telemetry.LogsOptions{Sink: b.logsEnv.Sink, Profile: b.Telemetry(),
				KmsgPath: b.paths.Kmsg, Log: b.log})
			b.caps = append(b.caps, b.logs)
			b.wg.Go(func() { b.logs.Run(b.ctx) })
		}
		if b.metricsEnv.Client != nil {
			b.metrics = telemetry.NewMetrics(telemetry.MetricsOptions{Env: b.metricsEnv, Profile: b.Telemetry(),
				Proc: b.paths.Proc, MediaPlaying: b.media.Playing})
			b.caps = append(b.caps, b.metrics)
			b.wg.Go(func() { b.metrics.Run(b.ctx) })
		}
	})
}

func (b *Board) Capabilities() []capability.Provider { b.setup(); return b.caps }

// WiFi is the capsula's own link, the same on both firmwares: S41wpa_supplicant starts a plain wpa_supplicant on
// wlan0 with /etc/wpa_supplicant.conf (update_config=1, on the overlay, so save_config persists to /data) and dhcpcd
// leases. wpa_cli speaks to its control socket; cadenced runs as root beside it. the path sits under the board's
// root, so a test's tree can hold a fake wpa_cli.
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

func (b *Board) Input() *input.Input       { b.setup(); return b.input }
func (b *Board) Sessions() *session.Recent { b.setup(); return b.sessions }
func (b *Board) Alarms() *alarms.Ringer    { b.setup(); return b.ringer }
func (b *Board) MediaSessions() <-chan *cadencev2.MediaSessionState {
	b.setup()
	return b.media.Sessions()
}
func (b *Board) MediaViz() <-chan media.VizFrame { b.setup(); return b.media.Viz() }
func (b *Board) MediaPlaying() bool              { b.setup(); return b.media.Playing() }
func (b *Board) Volume() *volume.Setter          { b.setup(); return b.vol }
func (b *Board) Lights() (leds.Sink, bool) {
	b.lightsOnce.Do(func() { b.lights = newLights(b.root) })
	return b.lights, true
}
func (b *Board) AudioIO() (audio.IO, bool) {
	b.setup()
	b.audioOnce.Do(func() { b.io = b.startAudiod() })
	return b.io, true
}
func (b *Board) Close() error {
	b.audioOnce.Do(func() {})
	if b.cancel == nil {
		return nil
	}
	b.cancel()
	b.buttons.Close()
	b.wg.Wait()
	return nil
}
func (*Board) Telemetry() board.TelemetryProfile {
	return board.TelemetryProfile{Kmsg: true, Mounts: []string{"/data", "/tmp"}}
}
func (b *Board) ServerDuck(d *cadencev2.Duck) {
	if b.media != nil {
		b.media.ServerDuck(d)
	}
}
