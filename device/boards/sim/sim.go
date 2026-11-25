package sim

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/airplay"
	"git.shino.boo/devilreef/cadence/device/internal/alarms"
	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/group"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/media"
	"git.shino.boo/devilreef/cadence/device/internal/session"
	"git.shino.boo/devilreef/cadence/device/internal/soloist"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	"git.shino.boo/devilreef/cadence/device/internal/wakeword"
	"git.shino.boo/devilreef/cadence/device/internal/wifi"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// the sim's system metrics are the host's, or its container's
const procRoot = "/proc"

type sim struct {
	getenv       func(string) string
	dataDir      string
	log          *slog.Logger
	vol          *volume.Setter
	sessions     *session.Recent
	caps         []capability.Provider
	audiodBinary string
	media        *media.Media         // audiod mode only
	soloist      *soloist.Soloist     // audiod mode only
	airplay      *airplay.Airplay     // audiod mode only
	bt           *bluetooth.Bluetooth // audiod mode with a data dir only
	btSim        *btSim               // audiod mode with a data dir only
	wifiCap      *wifi.Capability     // audiod mode with a data dir only
	wifiSim      *wifiSim             // audiod mode with a data dir only
	group        *group.Group         // audiod mode only
	groupOutput  capschema.MediaGroupOutput
	ringer       *alarms.Ringer // audiod mode only
	input        *input.Input   // audiod mode only
	buttons      *buttons       // audiod mode with a data dir only
	soloistCDN   *SoloistCDN
	// the e2e's (CADENCE_SIM_TIMER_SECONDS); zero is a real board's minute
	timerLimit time.Duration

	// with a pipeline only
	logsEnv telemetry.LogsEnv
	logs    *telemetry.Logs
	// with a pipeline and a data dir only
	probe *logProbe

	// with a client only
	metricsEnv telemetry.MetricsEnv
	metrics    *telemetry.Metrics

	// the logs and metrics capabilities run from the first look at the capabilities
	telemetryOnce   sync.Once
	telemetryCancel context.CancelFunc
	// network.wifi's watcher ends with the board
	wifiCancel context.CancelFunc

	once   sync.Once
	audio  *simAudio
	io     audio.IO
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type Option func(*sim)

// WithLogs gives the sim its logs capability, which hands audiod's lines to env's pipeline; a zero env gives none.
func WithLogs(env telemetry.LogsEnv) Option { return func(s *sim) { s.logsEnv = env } }

// WithMetrics gives the sim its system.metrics capability, which reads the host's /proc and exports through env's
// client, and has its input, ringer and audiod count into env's counts; a zero env gives none.
func WithMetrics(env telemetry.MetricsEnv) Option { return func(s *sim) { s.metricsEnv = env } }

// WithAudiod runs the sim's microphone and speaker through the audiod binary.
func WithAudiod(binary string) Option { return func(s *sim) { s.audiodBinary = binary } }

// WithGroupOutput sets the active output the sim reports through media.group; the default is its built-in speaker.
func WithGroupOutput(out capschema.MediaGroupOutput) Option {
	return func(s *sim) { s.groupOutput = out }
}

// New returns the sim board. It has audio only with a dataDir, where the say
// file goes in and the reply files come out; without one it keeps its level in
// memory. A nil log discards.
func New(getenv func(string) string, dataDir string, log *slog.Logger, opts ...Option) board.Board {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	var store *volume.Store
	if dataDir != "" {
		store = volume.NewStore(dataDir)
	}
	vol := volume.NewSetter(store, defaultLevel, log)
	sessions := &session.Recent{}
	s := &sim{
		getenv: getenv, dataDir: dataDir, log: log, vol: vol, sessions: sessions,
		groupOutput: capschema.MediaGroupOutput{ID: capschema.GroupOutputSpeaker, Kind: capschema.GroupOutputSpeaker},
		caps:        []capability.Provider{newOutput(vol, sessions)},
	}
	for _, o := range opts {
		o(s)
	}
	if s.audiodBinary != "" {
		// alarms ring through audiod, which needs the data dir as the sim's audio does; built first, as no media sender
		// moves the level while one rings
		if dataDir != "" {
			s.ringer = alarms.Open(dataDir, vol, sessions, log, func(o *alarms.RingerOptions) {
				o.TimerLimit, o.Counts = s.timerLimit, s.metricsEnv.App
			})
		}
		// the press file's buttons beside audiod's input events, and no mute switch; a pressed action's session has no
		// fixture to speak (see buttons)
		in := input.Options{Volume: vol, Counts: s.metricsEnv.App, Log: log}
		if dataDir != "" {
			s.buttons = newButtons(dataDir, log)
			in.Buttons = s.buttons
		}
		if s.ringer != nil {
			in.Ringer = s.ringer
		}
		s.input = input.New(in)
		s.caps = append(s.caps, s.input.Capability())
		if dataDir != "" {
			// the custom wake word, on the sim's capture file
			s.caps = append(s.caps, wakeword.Attach(s.input, wakeword.Options{DataDir: dataDir,
				Get: wakeword.ServerGetter(s.metricsEnv.Client), Log: log}))
		}
		s.media = media.New(log)
		s.soloist = soloist.New(vol, s.soloistOptions(), log)
		s.caps = append(s.caps, s.media.Capabilities()...)
		s.airplay = airplay.New(s.airplayOptions(), log)
		s.group = group.New(log)
		s.group.SetOutput(s.groupOutput)
		s.media.WatchSession(s.group.MediaSession)
		s.caps = append(s.caps, s.soloist.Capability(), s.airplay.Capability())
		if dataDir != "" {
			s.btSim = newBTSim(dataDir, log)
			s.bt = bluetooth.New(bluetooth.Options{
				Dial: s.btSim.profile().Dial, Senders: s.soloist, Level: vol.Current, Sessions: sessions,
			}, log)
			s.caps = append(s.caps, s.bt.Capability())
			s.wifiSim = newWiFiSim()
			var wctx context.Context
			wctx, s.wifiCancel = context.WithCancel(context.Background())
			s.wifiCap = wifi.NewCapability(wctx, s.WiFi().Dial, wifi.SettingsPath(dataDir), log)
			s.caps = append(s.caps, s.wifiCap)
		}
		s.caps = append(s.caps, s.group.Capability())
		if s.ringer != nil {
			s.caps = append(s.caps, s.ringer.Capability())
		}
	}
	if s.logsEnv.Sink != nil {
		s.logs = telemetry.NewLogs(telemetry.LogsOptions{Sink: s.logsEnv.Sink, Profile: s.Telemetry(), Log: log})
		s.caps = append(s.caps, s.logs)
		if dataDir != "" {
			s.probe = newLogProbe(dataDir, s.logsEnv.Sink, log)
		}
	}
	if s.metricsEnv.Client != nil {
		s.metrics = telemetry.NewMetrics(telemetry.MetricsOptions{
			Env: s.metricsEnv, Profile: s.Telemetry(), Proc: procRoot, MediaPlaying: s.MediaPlaying,
		})
		s.caps = append(s.caps, s.metrics)
	}
	return s
}

func (*sim) ID() string { return "sim" }

// Capabilities starts the logs and metrics capabilities, and the log probe, on its first call: core asks only the
// chosen board, after its fingerprint.
func (s *sim) Capabilities() []capability.Provider {
	s.telemetryOnce.Do(func() {
		var ctx context.Context
		ctx, s.telemetryCancel = context.WithCancel(context.Background())
		if s.logs != nil {
			s.wg.Go(func() { s.logs.Run(ctx) })
		}
		if s.probe != nil {
			s.wg.Go(func() { s.probe.poll(ctx) })
		}
		if s.metrics != nil {
			s.wg.Go(func() { s.metrics.Run(ctx) })
		}
	})
	return s.caps
}

// Sessions is where core records the sessions it begins.
func (s *sim) Sessions() *session.Recent { return s.sessions }

// SetDeviceName is the device's name, which the receivers advertise while their configs set none (board.DeviceNamer);
// without audiod there are none.
func (s *sim) SetDeviceName(name string) {
	if s.soloist != nil {
		s.soloist.SetDeviceName(name)
	}
	if s.airplay != nil {
		s.airplay.SetDeviceName(name)
	}
	if s.bt != nil {
		s.bt.SetDeviceName(name)
	}
}

// Bluetooth is a fake adapter in audiod mode with a data dir (btSim); the sim has none else.
func (s *sim) Bluetooth() board.BluetoothProfile {
	if s.btSim == nil {
		return board.BluetoothProfile{}
	}
	return s.btSim.profile()
}

var _ board.DeviceNamer = (*sim)(nil)

// Input is nil without audiod.
func (s *sim) Input() *input.Input { return s.input }

// Alarms is nil without audiod: the sim rings through it alone.
func (s *sim) Alarms() *alarms.Ringer { return s.ringer }

// MediaSessions is nil without audiod.
func (s *sim) MediaSessions() <-chan *cadencev2.MediaSessionState {
	if s.media == nil {
		return nil
	}
	return s.media.Sessions()
}

// MediaPlaying is never true without audiod.
func (s *sim) MediaPlaying() bool { return s.media != nil && s.media.Playing() }

// MediaPause pauses the playing media session: the offline stop words.
func (s *sim) MediaPause(ctx context.Context) error {
	if s.media == nil {
		return errors.New("sim: no audiod")
	}
	return s.media.Action(ctx, "pause")
}
func (s *sim) MediaNext(ctx context.Context) error {
	if s.media == nil {
		return errors.New("sim: no audiod")
	}
	return s.media.Action(ctx, "next")
}

func (s *sim) MediaPrevious(ctx context.Context) error {
	if s.media == nil {
		return errors.New("sim: no audiod")
	}
	return s.media.Action(ctx, "previous")
}

func (s *sim) MediaResume(ctx context.Context) error {
	if s.media == nil {
		return errors.New("sim: no audiod")
	}
	return s.media.Action(ctx, "resume")
}

// Volume is the level the sim keeps, which core puts back on audiod's output.
func (s *sim) Volume() *volume.Setter { return s.vol }

func (s *sim) Fingerprint(context.Context) error {
	if s.getenv("CADENCE_SIM") != "1" {
		return board.ErrFingerprint
	}
	return nil
}

// AudioIO starts the say-file poller, and audiod when it is on, on its first
// call; it runs after the stored configs were restored, so a legacy level has
// seeded the output's. Close stops both.
func (s *sim) AudioIO() (audio.IO, bool) {
	if s.dataDir == "" {
		return audio.IO{}, false
	}
	s.once.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.audio = newSimAudio(s.dataDir, s.log, s.audiodBinary != "")
		// the say file needs its directory before any reply has made it
		if err := os.MkdirAll(s.audio.dir, 0o750); err != nil {
			s.log.Warn("sim_dir_failed")
		}
		s.wg.Go(func() { s.audio.poll(ctx) })
		if s.buttons != nil {
			s.wg.Go(func() { s.buttons.poll(ctx) })
		}
		s.io = audio.IO{Triggers: s.audio.triggers, Mic: s.audio, Speaker: s.audio}
		if s.audiodBinary != "" {
			s.io = s.startAudiod(ctx)
		}
	})
	return s.io, true
}

func (s *sim) Close() error {
	s.once.Do(func() {})
	if s.cancel != nil {
		s.cancel()
	}
	s.telemetryOnce.Do(func() {})
	if s.telemetryCancel != nil {
		s.telemetryCancel()
	}
	if s.wifiCancel != nil {
		s.wifiCancel()
	}
	s.wg.Wait()
	return nil
}

// Telemetry reports the usage of the data dir, if there is one; no kernel log, no sensor.
func (s *sim) Telemetry() board.TelemetryProfile {
	var mounts []string
	if s.dataDir != "" {
		mounts = []string{s.dataDir}
	}
	return board.TelemetryProfile{Mounts: mounts}
}

// ServerDuck lowers the music while a turn in the room listens or speaks; never a raise (rule 7)
func (s *sim) ServerDuck(d *cadencev2.Duck) {
	if s.media != nil {
		s.media.ServerDuck(d)
	}
}
