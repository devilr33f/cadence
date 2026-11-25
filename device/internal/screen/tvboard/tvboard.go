// Package tvboard is what the screen boards (sim-tv, rpi, android-tv) share: the level, the input with the
// screen's keys (and the board's own remotes), the media plane with the screen's player as an external session, the
// screen plane itself and audiod when the board runs one. Each board package names its paths, its fingerprint, its
// audiod Configure and its remotes; nothing here names hardware.
package tvboard

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"

	"git.shino.boo/devilreef/cadence/device/internal/airplay"
	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/audiod"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/group"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/media"
	"git.shino.boo/devilreef/cadence/device/internal/screen"
	"git.shino.boo/devilreef/cadence/device/internal/session"
	"git.shino.boo/devilreef/cadence/device/internal/soloist"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// Remotes are a board's own key sources beside the screen's (evdev remotes, cec): the input's Buttons, started once
// the board runs.
type Remotes interface {
	input.Buttons
	Start(ctx context.Context, wg *sync.WaitGroup)
}

// AudiodHooks let a screen board add another audiod handler and route its output. NewSupervisor owns the same
// volume-follow contract as audiod.NewSupervisor plus Supervisor.SetVolume; nil keeps the board's fixed output.
type AudiodHooks struct {
	Options       func() []audiod.SupervisorOption
	NewSupervisor SupervisorFactory
}

type SupervisorFactory func(spec audiod.Spec, level int, log *slog.Logger, opts ...audiod.SupervisorOption) (*audiod.Supervisor, func(int))

// Options say what a screen board is.
type Options struct {
	ID          string
	Fingerprint func(context.Context) error
	// where the level is kept; "" keeps it in memory
	DataDir string
	// the level when none was kept
	DefaultLevel int
	// the screen plane's socket and audiod's tap, as screen.Options take them
	ScreenSocket string
	TapPath      string
	// the screen's volume keys act on the input; off for a board whose Remotes read the same keys
	ForwardKeys bool
	Remotes     Remotes
	Components  screen.Components
	// audiod: its binary and socket, and its Configure; an empty binary (and not AudiodExternal) runs none (none
	// is shipped), and the screen's player is then the session on its own
	AudiodBinary string
	AudiodSocket string
	// Soloist builds the board's media.soloist manager once the level exists (its capability, its builds and its
	// sender-volume policy); nil keeps the senders alone, the policy with no capability of its own
	Soloist func(vol *volume.Setter) *soloist.Soloist
	// AudiodExternal: audiod is hosted outside cadenced (android: the app's own process) and only dialled at
	// AudiodSocket, never spawned (audiod.Spec.External); AudiodBinary is then unused
	AudiodExternal bool
	Configure      *audiodv1.Configure
	// the active output media.group reports; a routable board updates it with SetGroupOutput
	Output      capschema.MediaGroupOutput
	AudiodHooks *AudiodHooks
	// Extra are capabilities only this board has (rpi's display.cec over /dev/cec, the sim's fake one); one
	// that also runs (cec's controller scans the wire) is started with the board
	Extra []capability.Provider
	// the audio.output stream the server sends: channels and the most bitrate
	OutputChannels int
	MaxBitrate     int
	Telemetry      board.TelemetryProfile
	LogsEnv        telemetry.LogsEnv
	MetricsEnv     telemetry.MetricsEnv
	Proc           string
	Version        string
	Log            *slog.Logger
}

// Board is a screen board. Every board is built at each start and only the chosen one runs, so New touches nothing:
// the first call after the fingerprint builds the rest.
type Board struct {
	o   Options
	log *slog.Logger

	setupOnce sync.Once
	vol       *volume.Setter
	sessions  *session.Recent
	input     *input.Input
	media     *media.Media
	screen    *screen.Server
	caps      []capability.Provider
	logs      *telemetry.Logs
	metrics   *telemetry.Metrics
	// libairplay in audiod, from media.screencast, and rule 7's policy for its sender's slider; nil without audiod
	airplay *airplay.Airplay
	senders *soloist.Senders
	// media.group: a multiroom member's playout runs in audiod; nil without audiod
	group *group.Group
	// the board's media.soloist manager, when it has one (Options.Soloist); it carries its own senders policy
	soloist *soloist.Soloist

	// the system stream's mute, where the level is the system volume (android): shown with the level, never stored
	muted atomic.Bool

	audioOnce sync.Once
	io        audio.IO
	hasAudio  bool
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

func New(o Options) *Board {
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.OutputChannels == 0 {
		o.OutputChannels = 2
	}
	if o.MaxBitrate == 0 {
		o.MaxBitrate = 128000
	}
	return &Board{o: o, log: o.Log}
}

func (b *Board) ID() string { return b.o.ID }

func (b *Board) Fingerprint(ctx context.Context) error {
	if b.o.Fingerprint == nil {
		return board.ErrFingerprint
	}
	return b.o.Fingerprint(ctx)
}

func (b *Board) setup() {
	b.setupOnce.Do(func() {
		b.ctx, b.cancel = context.WithCancel(context.Background())
		var store *volume.Store
		if b.o.DataDir != "" {
			store = volume.NewStore(b.o.DataDir)
		}
		b.vol = volume.NewSetter(store, b.o.DefaultLevel, b.log)
		b.sessions = &session.Recent{}
		b.media = media.New(b.log)
		sopts := screen.Options{
			Path: b.o.ScreenSocket, Media: b.media, ForwardKeys: b.o.ForwardKeys, TapPath: b.o.TapPath,
			Components: b.o.Components, DaemonVersion: b.o.Version, Log: b.log,
			// without audiod nobody else confirms the level: the screen's player holds it, and opens the gate
			DirectGate: !b.runsAudiod(),
		}
		if b.runsAudiod() {
			// airplay's covers (audiod writes each to its scratch): uploaded for the server, the dashboard and home
			// assistant, and shown on the screen's audio page from a copy in the run dir
			if scratch := b.airplayScratch(); scratch != "" {
				b.media.UseArtwork(filepath.Join(scratch, "run", "artwork"), media.ServerUploader(b.o.MetricsEnv.Client))
			}
			// the covers audiod holds none of (a multiroom group's stream source's, an artwork url's): the server's
			b.media.UseServerCovers(b.o.MetricsEnv.Client)
			if b.o.DataDir != "" {
				sopts.CoverDir = filepath.Join(b.o.DataDir, "run", "screen")
			}
			b.airplay = airplay.New(airplay.Options{
				ScratchDir: b.airplayScratch(), Backend: audiodv1.AirplayBackend_AIRPLAY_BACKEND_LIBAIRPLAY,
				OnState: func(state, code string) {
					// New's first state comes before the screen exists; the screen starts at starting itself
					if b.screen != nil {
						b.screen.ScreencastReceiver(state, code)
					}
				},
			}, b.log)
			b.senders = soloist.NewSenders(b.vol, nil, b.log)
			b.group = group.New(b.log)
			if b.o.Output.ID != "" {
				b.group.SetOutput(b.o.Output)
			}
			b.media.WatchSession(b.group.MediaSession)
			sopts.Screencast = b.screencast
		}
		if b.o.Soloist != nil {
			b.soloist = b.o.Soloist(b.vol)
		}
		b.screen = screen.New(sopts)
		if sopts.DirectGate {
			// the level in force, then every later one: the player is the board's only output
			b.vol.Follow(func(level int) func(int) {
				b.screen.DirectLevel(level)
				return b.screen.DirectLevel
			})
		}
		// the overlay follows the level in force, whoever set it, once applied
		b.screen.ShowVolume(b.vol.Level(), false, false)
		b.vol.Listen(func(level int, _ volume.Origin) { b.screen.ShowVolume(level, b.muted.Load(), true) })
		b.useTap(false)
		var buttons input.Buttons = b.screen
		if b.o.Remotes != nil {
			buttons = merged(b.ctx, b.screen, b.o.Remotes)
			b.o.Remotes.Start(b.ctx, &b.wg)
		}
		b.input = input.New(input.Options{Buttons: buttons, Volume: b.vol, Counts: b.o.MetricsEnv.App, Log: b.log})
		output := volume.NewOutput(b.vol, b.sessions, b.o.OutputChannels, b.o.MaxBitrate, volume.ScreenOutput(),
			volume.WithDelay(int(b.o.Configure.GetOutput().GetDelayMs()), b.media.SetOutputDelay))
		b.caps = []capability.Provider{output, b.input.Capability()}
		for _, c := range b.media.Capabilities() {
			// a screen's audiod is built without the url source (CADENCE_SCREEN: screens play urls with mpv), so it
			// answers every url play unsupported: the server plays music on the screen's own player instead
			if c.Descriptor().ID == "media.url" {
				continue
			}
			b.caps = append(b.caps, c)
		}
		if b.soloist != nil {
			b.caps = append(b.caps, b.soloist.Capability())
		}
		if b.group != nil {
			b.caps = append(b.caps, b.group.Capability())
		}
		b.caps = append(b.caps, b.screen.Capabilities()...)
		for _, p := range b.o.Extra {
			b.caps = append(b.caps, p)
			if r, ok := p.(interface {
				Run(context.Context, *sync.WaitGroup)
			}); ok {
				prov := r
				b.wg.Go(func() { prov.Run(b.ctx, &b.wg) })
			}
		}
		if b.o.LogsEnv.Sink != nil {
			b.logs = telemetry.NewLogs(telemetry.LogsOptions{Sink: b.o.LogsEnv.Sink, Profile: b.o.Telemetry, Log: b.log})
			b.caps = append(b.caps, b.logs)
			b.wg.Go(func() { b.logs.Run(b.ctx) })
		}
		if b.o.MetricsEnv.Client != nil {
			b.metrics = telemetry.NewMetrics(telemetry.MetricsOptions{
				Env: b.o.MetricsEnv, Profile: b.o.Telemetry, Proc: b.o.Proc, MediaPlaying: b.media.Playing,
				Extra: b.screen.PlayerPoints,
			})
			b.caps = append(b.caps, b.metrics)
			b.wg.Go(func() { b.metrics.Run(b.ctx) })
		}
		b.wg.Go(func() { _ = b.screen.Run(b.ctx) })
	})
}

func (b *Board) Capabilities() []capability.Provider {
	b.setup()
	return b.caps
}

// SetGroupOutput follows a board's audio route without rebuilding the group.
func (b *Board) SetGroupOutput(out capschema.MediaGroupOutput) {
	if b.group != nil {
		b.group.SetOutput(out)
	}
}

// AudioIO runs audiod, when the board has one, on its first call: the screen's speaker plays replies and
// announcements. Without audiod the board has no microphone or speaker of its own; its room's speaker listens.
func (b *Board) AudioIO() (audio.IO, bool) {
	b.setup()
	b.audioOnce.Do(func() {
		if !b.runsAudiod() {
			return
		}
		spec := audiod.Spec{
			Binary: b.o.AudiodBinary, Socket: b.o.AudiodSocket, Configure: b.o.Configure, External: b.o.AudiodExternal,
		}
		opts := []audiod.SupervisorOption{
			audiod.WithInput(b.input), audiod.WithMedia(b.media), audiod.WithCounts(b.o.MetricsEnv.App),
			audiod.WithGroup(b.group),
		}
		if b.logs != nil {
			opts = append(opts, audiod.WithLogs(b.logs, b.o.LogsEnv.Local))
		}
		if b.soloist != nil {
			opts = append(opts, audiod.WithSoloist(b.soloist), audiod.WithAirplay(b.airplay))
			b.wg.Go(func() { b.soloist.Run(b.ctx) })
		} else {
			opts = append(opts, audiod.WithSoloist(b.senders), audiod.WithAirplay(b.airplay))
			b.wg.Go(func() { b.senders.Run(b.ctx) })
		}
		if b.o.Configure.GetOutput().GetSystemVolume() {
			opts = append(opts, audiod.WithSystemVolume(b.systemVolume))
		}
		var factory SupervisorFactory = func(spec audiod.Spec, level int, log *slog.Logger, opts ...audiod.SupervisorOption) (*audiod.Supervisor, func(int)) {
			sup := audiod.NewSupervisor(spec, level, log, opts...)
			return sup, sup.SetVolume
		}
		if hooks := b.o.AudiodHooks; hooks != nil {
			if hooks.Options != nil {
				opts = append(opts, hooks.Options()...)
			}
			if hooks.NewSupervisor != nil {
				factory = hooks.NewSupervisor
			}
		}
		var sup *audiod.Supervisor
		b.vol.Follow(func(level int) func(int) {
			var follow func(int)
			sup, follow = factory(spec, level, b.log, opts...)
			return follow
		})
		b.wg.Go(func() { sup.Run(b.ctx) })
		b.io, b.hasAudio = sup.IO(nil), true
	})
	return b.io, b.hasAudio
}

// systemVolume follows the host's system volume where the level is the system's (android's STREAM_MUSIC): at each
// audiod start the level is reconciled with it, never raised (volume.Setter.Reconcile); later, a change the system
// made itself (the remote's keys android handled, another app, the tv's hdmi-cec volume) is the level in force,
// stored and reported like any other. the system already plays at it, so following it raises nothing (rule 7). a
// mute alone moves no level: the screen shows it
func (b *Board) systemVolume(level int, muted, initial bool) {
	was := b.muted.Swap(muted)
	if initial {
		b.vol.Reconcile(level)
		return
	}
	before := b.vol.Level()
	if after, err := b.vol.Set(level, volume.OriginSystem); err == nil && after == before && was != muted {
		b.screen.ShowVolume(after, muted, true)
	}
}

func (b *Board) runsAudiod() bool {
	return (b.o.AudiodBinary != "" || b.o.AudiodExternal) && b.o.Configure != nil
}

// airplayScratch is audiod's scratch for libairplay (its device id file): the run dir beside the sockets' on the
// boards that keep them on disk, else the data dir's
func (b *Board) airplayScratch() string {
	if b.o.DataDir == "" {
		return ""
	}
	return filepath.Join(b.o.DataDir, "run", "airplay")
}

// screencast takes media.screencast's config: libairplay in audiod mirrors to the screen while it is enabled, and
// the tap carries the mirror frames only then
func (b *Board) screencast(sc screen.Screencast) {
	b.airplay.Configure(airplay.Config{
		Enabled: sc.Enabled, Name: sc.Name, Mirroring: sc.Enabled, MaxWidth: sc.MaxWidth(), MaxHeight: sc.MaxHeight,
		H265: sc.H265, RequirePin: sc.Pin,
	})
	b.useTap(sc.Enabled)
}

func (b *Board) useTap(mirror bool) {
	if b.o.TapPath != "" {
		b.media.UseTap(&audiodv1.TapConfig{Path: b.o.TapPath, Pcm: true, Bands: true, Mirror: mirror})
	}
}

// Close stops what the board started.
func (b *Board) Close() error {
	b.setupOnce.Do(func() {})
	b.audioOnce.Do(func() {})
	if b.cancel != nil {
		b.cancel()
	}
	b.wg.Wait()
	return nil
}

// Screen is the board's screen plane.
func (b *Board) Screen() *screen.Server {
	b.setup()
	return b.screen
}

func (b *Board) Input() *input.Input {
	b.setup()
	return b.input
}

func (b *Board) Volume() *volume.Setter {
	b.setup()
	return b.vol
}

func (b *Board) Sessions() *session.Recent {
	b.setup()
	return b.sessions
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

// Media is the board's media plane, for a board-specific output's transport keys.
func (b *Board) Media() *media.Media {
	b.setup()
	return b.media
}

// Senders is the board's media-sender volume policy, for another receiver on the same audiod.
func (b *Board) Senders() *soloist.Senders {
	b.setup()
	return b.senders
}

func (b *Board) Telemetry() board.TelemetryProfile { return b.o.Telemetry }

// LinkUp, LinkMessage and LinkDown are the screen's relay (board.LinkPeer).
func (b *Board) LinkUp(send func(*cadencev2.Envelope) error, info *cadencev2.DeviceInfo) {
	b.Screen().LinkUp(send, info)
}

// SetDeviceName is the device's name, which AirPlay advertises while media.screencast sets none and soloist while
// media.soloist's config sets none (board.DeviceNamer); a board without audiod has no receiver.
func (b *Board) SetDeviceName(name string) {
	b.setup()
	if b.airplay != nil {
		b.airplay.SetDeviceName(name)
	}
	if b.soloist != nil {
		b.soloist.SetDeviceName(name)
	}
}

// SetServerURL names the origin the link dials, for the screen's image fetches (board.ServerOrigin).
func (b *Board) SetServerURL(origin string) { b.Screen().SetServerURL(origin) }

func (b *Board) LinkMessage(env *cadencev2.Envelope) bool { return b.Screen().LinkMessage(env) }

func (b *Board) LinkDown() { b.Screen().LinkDown() }

// ServerDuck lowers the mix (board.ServerDucker); the screen's relay takes the link's ducks itself
func (b *Board) ServerDuck(d *cadencev2.Duck) {
	b.setup()
	b.media.ServerDuck(d)
}

var (
	_ board.Board         = (*Board)(nil)
	_ board.Inputs        = (*Board)(nil)
	_ board.Volume        = (*Board)(nil)
	_ board.MediaReporter = (*Board)(nil)
	_ board.MediaPlayback = (*Board)(nil)
	_ board.LinkPeer      = (*Board)(nil)
	_ board.DeviceNamer   = (*Board)(nil)
	_ board.ServerDucker  = (*Board)(nil)
	_ board.Telemetry     = (*Board)(nil)
	_ io.Closer           = (*Board)(nil)
)

// merged is one Buttons over the screen's keys and the board's remotes
func merged(ctx context.Context, sources ...input.Buttons) input.Buttons {
	out := make(chan input.ButtonEvent, 16)
	for _, src := range sources {
		go func() {
			ch := src.Events()
			for {
				select {
				case <-ctx.Done():
					return
				case e, ok := <-ch:
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
		}()
	}
	return chanButtons(out)
}

type chanButtons chan input.ButtonEvent

func (c chanButtons) Events() <-chan input.ButtonEvent { return c }
