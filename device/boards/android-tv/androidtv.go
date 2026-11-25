// Package androidtv is an Android TV device (the android-tv apk: the Xiaomi stick first). The app hosts cadenced in
// its foreground service and cadence-screen in its activity, and starts the board with the fingerprint it read itself:
// an app may not read the device tree, the input nodes or the kernel log (S2), so the board trusts the app's word, set
// in the environment it gives cadenced. Keys reach the app, which hands the volume keys on over the screen plane; the
// stick has no microphone a third-party assistant may use (S4b), so its room's speaker listens. Signals to children go
// through syscall.Kill on android (S2: pidfd_send_signal is refused), as audiod's supervisor does.
package androidtv

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"

	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/cec"
	"git.shino.boo/devilreef/cadence/device/internal/discovery"
	"git.shino.boo/devilreef/cadence/device/internal/screen"
	"git.shino.boo/devilreef/cadence/device/internal/screen/tvboard"
	"git.shino.boo/devilreef/cadence/device/internal/soloist"
	"git.shino.boo/devilreef/cadence/device/internal/soloist/updater"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

const (
	ID           = "android-tv"
	defaultLevel = 30
	// abstract names: an app's sockets live in no shared directory
	screenSocket = "@cadence-screen"
	tapSocket    = "@cadence-tap"
	// the app's cec bridge (the framework owns the wire an app may not reach, DaemonService serves it)
	cecSocket = "@cadence-cec"
	// audiod's engine runs in the app's service process (android/, the AudiodHost) and listens here; cadenced only
	// dials it
	audiodSocket = "@cadence-audiod"
	// CADENCE_ANDROID_TV=1 says the app started cadenced; CADENCE_ANDROID_FINGERPRINT is Build.FINGERPRINT as the
	// app read it, never logged
	envApp         = "CADENCE_ANDROID_TV"
	envFingerprint = "CADENCE_ANDROID_FINGERPRINT"
)

// brand/product/device:release/id/incremental:type/tags, as Build.FINGERPRINT has it
var fingerprintShape = regexp.MustCompile(`^[^/\s]+/[^/\s]+/[^/:\s]+:[^/\s]+/[^/\s]+/[^:\s]+:[^/\s]+/[^\s]+$`)

type options struct {
	logsEnv    telemetry.LogsEnv
	metricsEnv telemetry.MetricsEnv
}

type Option func(*options)

func WithLogs(env telemetry.LogsEnv) Option       { return func(o *options) { o.logsEnv = env } }
func WithMetrics(env telemetry.MetricsEnv) Option { return func(o *options) { o.metricsEnv = env } }

// Board is the android tv.
type Board struct {
	*tvboard.Board
	getenv  func(string) string
	dataDir string
}

// New returns the android tv board; dataDir is the app's files dir the config names. audiod runs in the app's own
// process (S2), not as cadenced's child, so the board dials it and starts none. A nil log discards.
func New(getenv func(string) string, dataDir string, log *slog.Logger, opts ...Option) *Board {
	var o options
	for _, f := range opts {
		f(&o)
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	b := &Board{getenv: getenv, dataDir: dataDir}
	var mounts []string
	if dataDir != "" {
		mounts = []string{dataDir}
	}
	topts := tvboard.Options{
		ID: ID, Fingerprint: b.fingerprint, DataDir: dataDir, DefaultLevel: defaultLevel,
		ScreenSocket: screenSocket, TapPath: tapSocket, ForwardKeys: true,
		Output:       capschema.MediaGroupOutput{ID: capschema.GroupOutputHDMI, Kind: capschema.GroupOutputHDMI},
		AudiodSocket: audiodSocket, AudiodExternal: true, Configure: configure(),
		Components: screen.ReadComponents(filepath.Join(dataDir, "MANIFEST")),
		// no kernel log, no thermal zone an app may read, and /proc/stat is closed to it: the logs and the app's own
		// processes alone
		Telemetry: board.TelemetryProfile{Mounts: mounts},
		LogsEnv:   o.logsEnv, MetricsEnv: o.metricsEnv, Proc: "/proc", Log: log,
	}
	// hdmi-cec as the app can reach it (the framework's api keeps an app from the wire): display.cec over the
	// bridge; a stick whose framework has cec off reports present:false and the commands refuse
	topts.Extra = append(topts.Extra, cec.New(cec.NewBridge(cecSocket, log), log))
	if runs, how := kernelRunsArm64(getenv(envKernelMachine),
		filepath.Join(dataDir, "soloist", "runtime", arm64Loader)); runs {
		// spotify's soloist is arm64 alone: it runs beside the armv7 app as on midi, in its own runtime the app
		// ships in files/cadence/soloist/runtime, downloaded from spotify's cdn by cadenced as on the speakers
		topts.Soloist = func(vol *volume.Setter) *soloist.Soloist {
			return soloist.New(vol, b.soloistOptions(), log)
		}
		log.Info("soloist_kernel", "how", how)
	} else {
		log.Warn("soloist_kernel_refused", "how", how)
	}
	b.Board = tvboard.New(topts)
	return b
}

// soloistOptions are the app's soloist paths: its builds and runtime under the data dir, its scratch beside audiod's
// other run dirs, and the ca bundle the app writes from the system's certificates (the cdn and spotify's own tls
// verify against it alone, as a speaker's shipped bundle does)
func (b *Board) soloistOptions() soloist.Options {
	ca := filepath.Join(b.dataDir, "ca-bundle.crt")
	return soloist.Options{
		Dir:        filepath.Join(b.dataDir, "soloist"),
		ScratchDir: filepath.Join(b.dataDir, "run", "soloist"),
		CAFile:     ca,
		Updates:    &soloist.Updates{URL: updater.SpotifyARM64, CAFile: ca, Machine: updater.MachineAArch64},
	}
}

// configure is the app's audiod: aaudio playback alone (the stick has no microphone a third-party assistant may use,
// S4b), stereo, and the level is android's STREAM_MUSIC volume (the app's hook in libcadence-daemon.so): one level,
// moved by cadenced and read back when the system moves it, with audiod's own gain at unity, so the mix leaves
// audiod at full resolution and android's mixer applies the one attenuation. a stick whose stream volume is fixed
// (hdmi-cec passthrough) has audiod fall back to the software gain. aaudio picks its burst, so no layout
func configure() *audiodv1.Configure {
	return &audiodv1.Configure{Output: &audiodv1.OutputConfig{
		Backend: audiodv1.Backend_BACKEND_AAUDIO, Channels: 2, SystemVolume: true,
	}}
}

// Advertise hands the device's _cadence._tcp advertisement to the app, whose NsdManager registers it (an app has the
// system's responder on 5353, and Android has no multicast for an app's own without a lock): the file is there while
// cadenced advertises (board.Advertiser)
func (b *Board) Advertise(ctx context.Context, info discovery.Info, log *slog.Logger, ready func()) error {
	return discovery.AdvertiseHandoff(HandoffPath(b.dataDir))(ctx, info, log, ready)
}

// HandoffPath is where the app reads the advertisement (android/: NsdBridge)
func HandoffPath(dataDir string) string { return filepath.Join(dataDir, "run", "discovery.json") }

func (b *Board) fingerprint(context.Context) error {
	if b.getenv(envApp) != "1" || !fingerprintShape.MatchString(b.getenv(envFingerprint)) {
		return board.ErrFingerprint
	}
	return nil
}

var _ board.Advertiser = (*Board)(nil)
