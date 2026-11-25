// Package simtv is the screen simulator: a screen board with no hardware, for tests and the e2e. Its screen plane is
// whatever connects to its socket, or, with CADENCE_SIM_TV_FAKE_SCREEN=1, a fake screen in this process
// (screentest) that answers as cadence-screen would and plays nothing. With audiod it runs the file backend under its
// data dir, as the sim does; it has no microphone (its room's speaker listens).
package simtv

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/cec"
	"git.shino.boo/devilreef/cadence/device/internal/screen/screentest"
	"git.shino.boo/devilreef/cadence/device/internal/screen/tvboard"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

const (
	ID           = "sim-tv"
	defaultLevel = 30
	// the sim's system metrics are the host's, or its container's
	procRoot = "/proc"
)

type options struct {
	audiod     string
	logsEnv    telemetry.LogsEnv
	metricsEnv telemetry.MetricsEnv
}

type Option func(*options)

// WithAudiod runs the screen's speaker through the audiod binary, on the file backend.
func WithAudiod(binary string) Option { return func(o *options) { o.audiod = binary } }

func WithLogs(env telemetry.LogsEnv) Option       { return func(o *options) { o.logsEnv = env } }
func WithMetrics(env telemetry.MetricsEnv) Option { return func(o *options) { o.metricsEnv = env } }

// Board is the screen simulator.
type Board struct {
	*tvboard.Board
	getenv func(string) string
	socket string
	log    *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}
}

// New returns the screen simulator; its sockets live under dataDir/run, and without a data dir in the system temp
// dir. A nil log discards.
func New(getenv func(string) string, dataDir string, log *slog.Logger, opts ...Option) *Board {
	var o options
	for _, f := range opts {
		f(&o)
	}
	run := filepath.Join(dataDir, "run")
	if dataDir == "" {
		run = filepath.Join(os.TempDir(), "cadence-sim-tv")
	}
	b := &Board{getenv: getenv, socket: filepath.Join(run, "screen.sock"), log: log}
	var mounts []string
	if dataDir != "" {
		mounts = []string{dataDir}
	}
	topts := tvboard.Options{
		ID: ID, Fingerprint: b.fingerprint, DataDir: dataDir, DefaultLevel: defaultLevel,
		ScreenSocket: b.socket, TapPath: filepath.Join(run, "tap.sock"), ForwardKeys: true,
		Output:    capschema.MediaGroupOutput{ID: capschema.GroupOutputHDMI, Kind: capschema.GroupOutputHDMI},
		Telemetry: board.TelemetryProfile{Mounts: mounts}, LogsEnv: o.logsEnv, MetricsEnv: o.metricsEnv,
		Proc: procRoot, Log: log,
	}
	// a fake cec wire (a tv and an avr) keeps the display.cec capability exercisable without hardware
	if getenv("CADENCE_SIM_TV_FAKE_CEC") == "1" {
		topts.Extra = append(topts.Extra, cec.New(cec.NewFake(), log))
	}
	if o.audiod != "" && dataDir != "" {
		out := filepath.Join(dataDir, "sim", "out")
		topts.AudiodBinary = o.audiod
		topts.AudiodSocket = filepath.Join(run, "audiod.sock")
		topts.Configure = &audiodv1.Configure{
			Output: &audiodv1.OutputConfig{Backend: audiodv1.Backend_BACKEND_FILE, Dir: out, Channels: 2},
		}
	}
	b.Board = tvboard.New(topts)
	return b
}

func (b *Board) fingerprint(context.Context) error {
	if b.getenv("CADENCE_SIM_TV") != "1" {
		return board.ErrFingerprint
	}
	return os.MkdirAll(filepath.Dir(b.socket), 0o700)
}

// Capabilities starts the fake screen, when asked for, beside the screen plane, on the first call: core asks only
// the chosen board, after its fingerprint.
func (b *Board) Capabilities() []capability.Provider {
	caps := b.Board.Capabilities()
	if b.getenv("CADENCE_SIM_TV_FAKE_SCREEN") == "1" && b.done == nil {
		ctx, cancel := context.WithCancel(context.Background())
		b.cancel, b.done = cancel, make(chan struct{})
		go b.fakeScreen(ctx)
	}
	return caps
}

// the fake screen reconnects whenever cadenced drops it, until the board closes
func (b *Board) fakeScreen(ctx context.Context) {
	defer close(b.done)
	for ctx.Err() == nil {
		s, err := screentest.Connect(ctx, b.socket, screentest.Options{})
		if err != nil {
			return
		}
		if b.log != nil {
			b.log.Info("sim_fake_screen_connected")
		}
		select {
		case <-ctx.Done():
			s.Close()
			return
		case <-s.Done():
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (b *Board) Close() error {
	if b.cancel != nil {
		b.cancel()
		<-b.done
	}
	return b.Board.Close()
}
