// Package micro is the minimal Yandex Station Micro v1.2 (Lite) board. Its
// 87.5 MiB data partition cannot hold the speakers' media runtimes, so this
// experimental profile keeps voice, URL playback, alarms, buttons and telemetry.
package micro

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"

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
	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	"git.shino.boo/devilreef/cadence/device/internal/wakeword"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
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
	senders               *soloist.Senders
	bt                    *bluetooth.Bluetooth
	group                 *group.Group
	ringer                *alarms.Ringer
	lights                *lightSink
	caps                  []capability.Provider
	logsEnv               telemetry.LogsEnv
	logs                  *telemetry.Logs
	metricsEnv            telemetry.MetricsEnv
	metrics               *telemetry.Metrics
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

func (*Board) ID() string { return "yandex-micro" }

// Shell is the vendor's busybox an update plan's exec steps run through.
func (*Board) Shell() string  { return "/system/bin/sh" }
func (b *Board) Paths() Paths { return b.paths }

func (b *Board) setup() {
	b.setupOnce.Do(func() {
		if b.dataDir != "" && filepath.Clean(b.dataDir) != filepath.Clean(b.paths.DataRoot) {
			b.log.Warn("data_dir_mismatch")
		}
		b.ctx, b.cancel = context.WithCancel(context.Background())
		b.vol = volume.NewSetter(volume.NewStore(b.paths.DataRoot), defaultLevel, b.log)
		b.sessions = &session.Recent{}
		b.buttons, _ = evdev.Open(buttons(b.root), b.log, nil)
		countRings := func(o *alarms.RingerOptions) { o.Counts = b.metricsEnv.App }
		b.ringer = alarms.Open(b.paths.DataRoot, b.vol, b.sessions, b.log, countRings)
		b.input = input.New(input.Options{Buttons: b.buttons, Volume: b.vol, Ringer: b.ringer,
			Counts: b.metricsEnv.App, Log: b.log})
		b.buttons.Start(b.ctx, &b.wg)
		b.media = media.New(b.log)
		b.group = group.New(b.log)
		b.group.SetOutput(capschema.MediaGroupOutput{ID: capschema.GroupOutputSpeaker, Kind: capschema.GroupOutputSpeaker})
		b.media.WatchSession(b.group.MediaSession)
		b.senders = soloist.NewSenders(b.vol, b.ringer.Ringing, b.log)
		b.bt = bluetooth.New(bluetooth.Options{Dial: b.Bluetooth().Dial, Senders: b.senders,
			Level: b.vol.Current, Sessions: b.sessions}, b.log)
		b.lights = &lightSink{root: b.root}
		output := volume.NewOutput(b.vol, b.sessions, outputChannels, maxBitrate,
			volume.WithDelay(0, b.media.SetOutputDelay))
		b.caps = append([]capability.Provider{output, b.input.Capability()}, b.media.Capabilities()...)
		// the custom wake word, off until a config enables it
		b.caps = append(b.caps, wakeword.Attach(b.input, wakeword.Options{DataDir: b.paths.DataRoot,
			Get: wakeword.ServerGetter(b.metricsEnv.Client), Log: b.log}))
		b.caps = append(b.caps, b.bt.Capability(), b.group.Capability(), b.ringer.Capability())
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
func (b *Board) Input() *input.Input                 { b.setup(); return b.input }
func (b *Board) SetDeviceName(name string)           { b.setup(); b.bt.SetDeviceName(name) }

var _ board.DeviceNamer = (*Board)(nil)

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

func (b *Board) Sessions() *session.Recent { b.setup(); return b.sessions }
func (b *Board) Alarms() *alarms.Ringer    { b.setup(); return b.ringer }
func (b *Board) MediaSessions() <-chan *cadencev2.MediaSessionState {
	b.setup()
	return b.media.Sessions()
}
func (b *Board) MediaViz() <-chan media.VizFrame      { b.setup(); return b.media.Viz() }
func (b *Board) MediaPlaying() bool                   { b.setup(); return b.media.Playing() }
func (b *Board) MediaPause(ctx context.Context) error { b.setup(); return b.media.Action(ctx, "pause") }
func (b *Board) MediaNext(ctx context.Context) error  { b.setup(); return b.media.Action(ctx, "next") }
func (b *Board) MediaPrevious(ctx context.Context) error {
	b.setup()
	return b.media.Action(ctx, "previous")
}
func (b *Board) MediaResume(ctx context.Context) error {
	b.setup()
	return b.media.Action(ctx, "resume")
}
func (b *Board) Volume() *volume.Setter    { b.setup(); return b.vol }
func (b *Board) Lights() (leds.Sink, bool) { b.setup(); return b.lights, true }
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
	b.lights.close()
	return nil
}
func (b *Board) Telemetry() board.TelemetryProfile {
	return board.TelemetryProfile{Kmsg: true, Mounts: []string{"/data", "/tmp"}}
}
func (b *Board) ServerDuck(d *cadencev2.Duck) {
	if b.media != nil {
		b.media.ServerDuck(d)
	}
}
