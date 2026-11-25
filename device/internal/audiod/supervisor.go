package audiod

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/tracing"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// a code audiod sends, as the supervisor logs it
var codeChars = regexp.MustCompile(`^[a-z0-9_]{1,31}$`)

// crashes inside the window that put the supervisor in safe mode
const safeModeCrashes = 3

// audiod's kProbeLimit (media/audiod/vendor-probe.h): each of its two vendor probe children, the engines' and then the
// command model's, may run this long before the vendor status comes; a test keeps the two in step
const vendorProbeLimit = 5 * time.Second

// the engines' start after both probes: it opens what the first probe opened, so it gets one probe's time
const vendorStartAllowance = vendorProbeLimit

// ErrUnavailable is returned by the supervised IO while audiod is down or its
// volume has not been sent yet.
var ErrUnavailable = errors.New("audiod: unavailable")

// Spec says how to run audiod.
type Spec struct {
	Binary string
	// Args follow the --socket <Socket> the supervisor always passes.
	Args []string
	// Env nil means a minimal environment (PATH only), never cadenced's own.
	Env []string
	// Socket is created by audiod; its directory is created 0700.
	Socket    string
	Configure *audiodv1.Configure
	// External says audiod is hosted outside cadenced (android: the app runs audiod's engine in its own process, S2):
	// the supervisor only dials Socket, never spawns or signals anything, and Binary, Args and Env are unused. A
	// connection that ends, or a dial that times out, counts as a crash and is retried with the same backoff
	External bool
}

// Timings are the supervisor's durations; zero fields keep their defaults.
type Timings struct {
	DialEvery    time.Duration // 50 ms
	DialTimeout  time.Duration // 5 s
	ReadyTimeout time.Duration // 5 s
	PingEvery    time.Duration // 1 s
	PongTimeout  time.Duration // 3 s
	BackoffMin   time.Duration // 1 s
	BackoffMax   time.Duration // 60 s
	BackoffReset time.Duration // 60 s: a run this long starts the backoff over
	CrashWindow  time.Duration // 5 min
	StopGrace    time.Duration // 2 s from SIGTERM to SIGKILL
	LogDropEvery time.Duration // 1 min between audiod_log_dropped reports
	FirstPacket  time.Duration // 2 s for a session's first mic packet
	// 15 s for it while audiod still probes and starts the vendor engines, which hold a session back until they report:
	// both probes at their limit, then the start
	FirstPacketVendor time.Duration
}

var defaultTimings = Timings{
	DialEvery: 50 * time.Millisecond, DialTimeout: 5 * time.Second, ReadyTimeout: 5 * time.Second,
	PingEvery: time.Second, PongTimeout: 3 * time.Second,
	BackoffMin: time.Second, BackoffMax: time.Minute, BackoffReset: time.Minute,
	CrashWindow: 5 * time.Minute, StopGrace: 2 * time.Second, LogDropEvery: time.Minute,
	FirstPacket: 2 * time.Second, FirstPacketVendor: 2*vendorProbeLimit + vendorStartAllowance,
}

type SupervisorOption func(*Supervisor)

// MediaHandler is cadenced's side of audiod's media plane. MediaReady runs
// after each Ready, once the VolumeSet and the DuckSet went out, with send
// bound to that connection; MediaEnvelope gets each MediaState, CommandResult,
// VizFrame, VolumeApplied, ExternalAction, RouteControl, MirrorState, AirplayPin and HlsCast on it and must never block; MediaGone follows each MediaReady
// once the connection ended.
type MediaHandler interface {
	MediaReady(send func(*audiodv1.Envelope) error)
	MediaEnvelope(env *audiodv1.Envelope)
	MediaGone()
}

func WithMedia(h MediaHandler) SupervisorOption { return func(s *Supervisor) { s.media = h } }

// SoloistHandler is cadenced's side of soloist under audiod. SoloistReady runs
// after each Ready once the VolumeSet, the DuckSet and MediaReady's settings
// went out, with send bound to that connection; SoloistEnvelope gets each
// SoloistStatus, SoloistProbeResult and SenderVolume on it and must never
// block; SoloistGone follows each SoloistReady once the connection ended,
// before MediaGone.
type SoloistHandler interface {
	SoloistReady(send func(*audiodv1.Envelope) error)
	SoloistEnvelope(env *audiodv1.Envelope)
	SoloistGone()
}

func WithSoloist(h SoloistHandler) SupervisorOption { return func(s *Supervisor) { s.soloist = h } }

// AirplayHandler is cadenced's side of the AirPlay receiver under audiod.
// AirplayReady runs after each Ready, once everything SoloistReady sent went
// out, with send bound to that connection; AirplayEnvelope gets each
// AirplayStatus on it and must never block; AirplayGone follows each
// AirplayReady once the connection ended, before SoloistGone.
type AirplayHandler interface {
	AirplayReady(send func(*audiodv1.Envelope) error)
	AirplayEnvelope(env *audiodv1.Envelope)
	AirplayGone()
}

// WithAirplay needs WithSoloist: the AirPlay sender's volume (SenderVolume)
// and the levels the receiver follows (SenderVolumeSet) go through the soloist
// handler, whose policy is sender-generic. NewSupervisor refuses AirPlay
// without it.
func WithAirplay(h AirplayHandler) SupervisorOption { return func(s *Supervisor) { s.airplay = h } }

// GroupHandler is cadenced's side of multiroom playback under audiod (media.group). GroupReady runs after each Ready,
// once everything AirplayReady sent went out, with send bound to that connection; GroupEnvelope gets each GroupStatus
// on it and must never block; GroupGone follows each GroupReady once the connection ended, before AirplayGone.
type GroupHandler interface {
	GroupReady(send func(*audiodv1.Envelope) error)
	GroupEnvelope(env *audiodv1.Envelope)
	GroupGone()
}

func WithGroup(h GroupHandler) SupervisorOption { return func(s *Supervisor) { s.group = h } }

// BluetoothHandler is cadenced's side of a phone's stream into audiod (capability bluetooth). BluetoothReady runs
// after each Ready, once everything GroupReady sent went out, with send and sendFD bound to that connection (sendFD
// sends a frame with one fd beside it, which the caller still owns); BluetoothEnvelope gets each BluetoothControl on
// it and must never block; BluetoothGone follows each BluetoothReady once the connection ended, before GroupGone.
type BluetoothHandler interface {
	BluetoothReady(send func(*audiodv1.Envelope) error, sendFD func(*audiodv1.Envelope, int) error)
	BluetoothEnvelope(env *audiodv1.Envelope)
	BluetoothGone()
}

func WithBluetooth(h BluetoothHandler) SupervisorOption { return func(s *Supervisor) { s.bt = h } }

// InputHandler is cadenced's side of audiod's input events. InputReady runs after each Ready, once the VolumeSet and
// the DuckSet went out and before any capture can start, with send bound to that connection, so the mute switch
// reaches audiod first; it runs under the supervisor's lock and must never call it. InputEnvelope gets each
// WakeEvent, CommandWord and VendorStatus and must never block; InputGone follows each InputReady once the
// connection ended. MicPcm (the custom wake word's microphone) reaches InputEnvelope too.
type InputHandler interface {
	InputReady(send func(*audiodv1.Envelope) error)
	InputEnvelope(env *audiodv1.Envelope)
	InputGone()
}

func WithInput(h InputHandler) SupervisorOption { return func(s *Supervisor) { s.input = h } }

// WithSystemVolume hands the host's system volume to f, where the output's level is the system's
// (OutputConfig.system_volume, android's STREAM_MUSIC): once per connection from its Ready with initial set, before the
// first VolumeSet goes out, so f may lower the level that VolumeSet carries (it runs outside the supervisor's lock and
// may call SetVolume); then for each change the system made itself (SystemVolume), on the session goroutine. f must
// not block.
func WithSystemVolume(f func(level int, muted, initial bool)) SupervisorOption {
	return func(s *Supervisor) { s.system = f }
}

// Counts hears each start of audiod after it died, for the metrics; it must never block.
type Counts interface {
	AudiodRestarted()
}

func WithCounts(c Counts) SupervisorOption { return func(s *Supervisor) { s.counts = c } }

// The codes of a ToneState that is not sounding: audiod's answers to a
// StartTone (gated, busy, invalid) and to a StopTone (stopped), a ring the gate
// shut under (gated), and the supervisor's own gone.
const (
	// no volume held: playback is gated
	ToneGated = "gated"
	// another id sounds
	ToneBusy = "busy"
	// an empty id, or a kind other than an alarm's or a timer's
	ToneInvalid = "invalid"
	// StopTone took it back
	ToneStopped = "stopped"
	// the connection it was started on ended before audiod reported it not
	// sounding; never on the wire
	ToneGone = "gone"
)

// WithToneState hands each ToneState audiod sends, the answer to a StartTone
// and a ring's end, to f, which must never block. A tone started on a
// connection that ends before audiod reported it not sounding gets a
// ToneState{Code: ToneGone} from the supervisor itself, so a ring knows to
// start it again on the next audiod.
func WithToneState(f func(*audiodv1.ToneState)) SupervisorOption {
	return func(s *Supervisor) { s.toneState = f }
}

func WithTimings(t Timings) SupervisorOption {
	return func(s *Supervisor) {
		for _, f := range []struct{ dst, src *time.Duration }{
			{&s.t.DialEvery, &t.DialEvery}, {&s.t.DialTimeout, &t.DialTimeout}, {&s.t.ReadyTimeout, &t.ReadyTimeout},
			{&s.t.PingEvery, &t.PingEvery}, {&s.t.PongTimeout, &t.PongTimeout},
			{&s.t.BackoffMin, &t.BackoffMin}, {&s.t.BackoffMax, &t.BackoffMax}, {&s.t.BackoffReset, &t.BackoffReset},
			{&s.t.CrashWindow, &t.CrashWindow}, {&s.t.StopGrace, &t.StopGrace}, {&s.t.LogDropEvery, &t.LogDropEvery},
			{&s.t.FirstPacket, &t.FirstPacket}, {&s.t.FirstPacketVendor, &t.FirstPacketVendor},
		} {
			if *f.src > 0 {
				*f.dst = *f.src
			}
		}
	}
}

// Supervisor runs audiod, restarts it when it crashes, and serves the session
// controller's audio through whichever connection is up.
type Supervisor struct {
	spec    Spec
	log     *slog.Logger
	t       Timings
	sio     *supervisedIO
	media   MediaHandler
	soloist SoloistHandler
	airplay AirplayHandler
	group   GroupHandler
	bt      BluetoothHandler
	input   InputHandler
	logs    LogsHandler
	counts  Counts
	system  func(level int, muted, initial bool)
	// where audiod's own lines are logged locally: the supervisor's log, or the local log alone given WithLogs
	lineLog *slog.Logger
	// called on the session goroutine alone, so states arrive in order
	toneState func(*audiodv1.ToneState)
	safe      atomic.Bool

	mu     sync.Mutex
	level  int
	ducked bool
	// the Configure the next connection gets: the spec's until Reconfigure
	configure *audiodv1.Configure
	// Reconfigure asks the live connection to end, so a new one carries its Configure
	reconnect chan struct{}
	// the live connection, set only once its VolumeSet went out
	cur *audioIO
	// the tones started on cur that audiod has not reported not sounding
	tones   map[string]struct{}
	crashes []time.Time

	dropMu       sync.Mutex
	dropped      int
	dropReported time.Time
}

// NewSupervisor needs the persisted output level (0-100): audiod never runs
// without it (rule 7). An AirPlay handler without a soloist handler is
// dropped, logged: its sender's volume would reach nothing.
func NewSupervisor(spec Spec, level int, log *slog.Logger, opts ...SupervisorOption) *Supervisor {
	s := &Supervisor{spec: spec, log: log, lineLog: log, t: defaultTimings, level: level, configure: spec.Configure,
		reconnect: make(chan struct{}, 1)}
	s.sio = &supervisedIO{s: s}
	for _, o := range opts {
		o(s)
	}
	if s.airplay != nil && s.soloist == nil {
		log.Error("audiod_airplay_refused", "reason", "no_soloist")
		s.airplay = nil
	}
	return s
}

// IO is the session controller's audio; it stays the same across restarts.
func (s *Supervisor) IO(triggers <-chan audio.Trigger) audio.IO {
	return audio.IO{Triggers: triggers, Mic: s.sio, Speaker: s.sio}
}

// SafeMode is true for the rest of the process after 3 crashes inside the
// crash window; audiod then restarts with the vendor engines off.
func (s *Supervisor) SafeMode() bool { return s.safe.Load() }

// Reconfigure replaces the board's Configure (a board whose output follows something, as a bluetooth speaker). audiod
// keeps running: the live connection ends and a new one carries cfg, which every handler is ready on again as after a
// restart (audiod lets everything a connection set go with it, and shuts its playback gate until the next VolumeSet,
// rule 7). With no connection up, the next one carries it.
func (s *Supervisor) Reconfigure(cfg *audiodv1.Configure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configure = cfg
	select {
	case s.reconnect <- struct{}{}:
	default:
	}
}

// SetVolume sends level at once when audiod is up, else as the first message
// after its next Ready; the latest level wins.
func (s *Supervisor) SetVolume(level int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.level = level
	if s.cur != nil {
		s.sendVolume(s.cur.c, level)
	}
}

// out of range maps to 101, which audiod refuses with its gate shut
func wireLevel(level int) uint32 {
	if level < 0 || level > 100 {
		return 101
	}
	return uint32(level)
}

// callers hold mu
func (s *Supervisor) sendVolume(c *Client, level int) {
	_ = c.Send(&audiodv1.Envelope{Body: &audiodv1.Envelope_VolumeSet{VolumeSet: &audiodv1.VolumeSet{Level: wireLevel(level)}}})
}

// duck sends DuckSet at once when audiod is up; audiod forgets it with its
// client, so the latest value follows the VolumeSet after every Ready.
func (s *Supervisor) duck(active bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ducked = active
	if s.cur != nil {
		sendDuck(s.cur.c, active)
	}
}

// tone plays a one-shot earcon (wake, done or error); a tone missed while audiod is down is not replayed
func (s *Supervisor) tone(kind audiodv1.ToneKind) { s.sendTone(&audiodv1.Tone{Kind: kind}) }

// prompt plays the spoken prompt id in lang once, or the error earcon where audiod lacks it; "" plays the earcon
func (s *Supervisor) prompt(id, lang string) {
	s.sendTone(&audiodv1.Tone{Kind: audiodv1.ToneKind_TONE_KIND_ERROR, PromptId: id, Lang: lang})
}

func (s *Supervisor) sendTone(t *audiodv1.Tone) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil {
		_ = s.cur.c.Send(&audiodv1.Envelope{Body: &audiodv1.Envelope_Tone{Tone: t}})
	}
}

func sendDuck(c *Client, active bool) {
	_ = c.Send(&audiodv1.Envelope{Body: &audiodv1.Envelope_DuckSet{DuckSet: &audiodv1.DuckSet{Active: active}}})
}

// StartTone loops an alarm's or a timer's tone on audiod until StopTone;
// a ToneState answers it. ErrUnavailable while audiod is down.
func (s *Supervisor) StartTone(id string, kind audiodv1.ToneKind) error {
	return s.StartRing(&audiodv1.ToneStart{Id: id, Kind: kind})
}

// StartRing loops a ring of segments (an alarm's music, a reminder's chime and speech) as StartTone loops a tone; its
// url's Authorization goes to audiod alone, never into a log.
func (s *Supervisor) StartRing(start *audiodv1.ToneStart) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return ErrUnavailable
	}
	err := s.cur.c.Send(&audiodv1.Envelope{Body: &audiodv1.Envelope_ToneStart{ToneStart: start}})
	if err == nil {
		s.tones[start.GetId()] = struct{}{}
	}
	return err
}

// StopTone ends the tone StartTone began; a ToneState says once it stopped.
// ErrUnavailable while audiod is down.
func (s *Supervisor) StopTone(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return ErrUnavailable
	}
	return s.cur.c.Send(&audiodv1.Envelope{Body: &audiodv1.Envelope_ToneStop{ToneStop: &audiodv1.ToneStop{Id: id}}})
}

// SetTransientLevel sets audiod's output to level for an alarm's ramp
// without making it the level in force: SetVolume's level stays the one
// every audiod start gets, and audiod keeps its gate. audiod refuses it while
// no tone StartTone began sounds, and never sets it again. ErrUnavailable
// while audiod is down; it never reaches a later audiod.
func (s *Supervisor) SetTransientLevel(level int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return ErrUnavailable
	}
	set := &audiodv1.VolumeSet{Level: wireLevel(level), Transient: true}
	return s.cur.c.Send(&audiodv1.Envelope{Body: &audiodv1.Envelope_VolumeSet{VolumeSet: set}})
}

// volumeApplied logs a level that did not hold. audiod sets a plain level its hardware control refused again itself,
// gate shut, and answers ok once it holds; a transient one (a ramp step) it never sets again, and its refusal leaves
// the level in force as it was, so it is no failed volume
func (s *Supervisor) volumeApplied(va *audiodv1.VolumeApplied) {
	if va.GetOk() {
		return
	}
	msg := "audiod_volume_failed"
	if va.GetTransient() {
		msg = "audiod_ramp_refused"
	}
	s.log.Warn(msg, "volume", int64(va.GetLevel()), "reason", codeToken(va.GetCode()))
}

// codeToken passes audiod's code on, and nothing else it might send
func codeToken(code string) string {
	if codeChars.MatchString(code) {
		return code
	}
	return "unknown"
}

// toneReported forgets a tone audiod no longer sounds and hands the state on
func (s *Supervisor) toneReported(st *audiodv1.ToneState) {
	if !st.GetSounding() {
		s.mu.Lock()
		delete(s.tones, st.GetId())
		s.mu.Unlock()
	}
	if s.toneState != nil {
		s.toneState(st)
	}
}

func (s *Supervisor) current() *audioIO {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Run keeps audiod running until ctx ends, then stops it.
func (s *Supervisor) Run(ctx context.Context) {
	defer s.reportDrops(true)
	delay := s.t.BackoffMin
	for attempt := 1; ; attempt++ {
		if attempt > 1 && s.counts != nil {
			s.counts.AudiodRestarted()
		}
		ran, crashed := s.runOnce(ctx, attempt)
		if !crashed {
			return
		}
		s.recordCrash()
		if ran >= s.t.BackoffReset {
			delay = s.t.BackoffMin
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		delay = min(delay*2, s.t.BackoffMax)
	}
}

func (s *Supervisor) recordCrash() {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.crashes[:0]
	for _, c := range s.crashes {
		if now.Sub(c) < s.t.CrashWindow {
			kept = append(kept, c)
		}
	}
	s.crashes = append(kept, now)
	if len(s.crashes) >= safeModeCrashes && !s.safe.Swap(true) {
		s.log.Warn("audiod_safe_mode", "crashes", len(s.crashes))
	}
}

// runOnce runs audiod once; crashed is false when ctx ended and audiod was
// stopped on purpose.
func (s *Supervisor) runOnce(ctx context.Context, attempt int) (ran time.Duration, crashed bool) {
	if s.spec.External {
		return s.runExternal(ctx, attempt)
	}
	s.log.Info("audiod_start", "attempt", attempt)
	if err := os.MkdirAll(filepath.Dir(s.spec.Socket), 0o700); err != nil {
		s.log.Warn("audiod_spawn_failed", "reason", "socket_dir")
		return 0, true
	}
	p, err := s.spawn()
	if err != nil {
		s.log.Warn("audiod_spawn_failed", "reason", "exec")
		return 0, true
	}
	started := time.Now()
	stop := s.sessions(ctx, p)
	if stop {
		p.stop(s.t.StopGrace)
	} else {
		p.kill()
	}
	ran = time.Since(started)
	code := p.reap()
	s.reportDrops(false)
	if stop {
		s.log.Info("audiod_stopped", "code", code, "ran_ms", ran.Milliseconds())
		return ran, false
	}
	s.log.Warn("audiod_exit", "code", code, "ran_ms", ran.Milliseconds())
	return ran, true
}

// runExternal serves an audiod someone else runs, once: it dials, serves until the connection ends, and never
// stops audiod itself, which outlives cadenced
func (s *Supervisor) runExternal(ctx context.Context, attempt int) (ran time.Duration, crashed bool) {
	s.log.Info("audiod_dial", "attempt", attempt)
	p := externalProc()
	started := time.Now()
	stop := s.sessions(ctx, p)
	ran = time.Since(started)
	s.reportDrops(false)
	if stop {
		s.log.Info("audiod_detached", "ran_ms", ran.Milliseconds())
		return ran, false
	}
	s.log.Warn("audiod_lost", "ran_ms", ran.Milliseconds())
	return ran, true
}

// how a session ended
type ending int

const (
	endCrash ending = iota
	// ctx ended
	endStop
	// Reconfigure: connect again to the same audiod
	endReconnect
)

// sessions serves a started audiod over one connection after another, a new one at each Reconfigure, until it dies;
// it reports whether ctx ended.
func (s *Supervisor) sessions(ctx context.Context, p *proc) bool {
	end := s.session(ctx, p, false)
	for end == endReconnect {
		s.log.Info("audiod_reconfigure")
		end = s.session(ctx, p, true)
	}
	return end == endStop
}

// session connects to a started audiod and serves it until it dies, ctx ends or a Reconfigure asks for a new
// connection; again says the last one ended for that.
func (s *Supervisor) session(ctx context.Context, p *proc, again bool) ending {
	failed := func() ending {
		if ctx.Err() != nil {
			return endStop
		}
		return endCrash
	}
	c, ready, sent := s.connect(ctx, p, again)
	if c == nil {
		return failed()
	}
	defer c.Close()
	version := ready.GetVersion()
	// the system's level, where the output's level is the system volume: reconciled before the first VolumeSet, so
	// what publish sends is already the level in force (lower or equal to both, rule 7). outside the lock: the
	// handler's setter applies through SetVolume
	if sv := ready.GetSystemVolume(); sv != nil && s.system != nil {
		s.system(int(sv.GetLevel()), sv.GetMuted(), true)
	}
	fwd := make(chan *audiodv1.Envelope, recvBuf)
	defer close(fwd)
	s.publish(c, fwd, sent)
	defer s.unpublish()
	if s.input != nil {
		defer s.input.InputGone()
	}
	s.log.Info("audiod_ready", "version", version)
	if s.logs != nil {
		s.logs.LogsReady(c.Send)
		defer s.logs.LogsGone()
	} else {
		// no log pipeline (no server url, a bad ca file): nothing would take the children's lines
		_ = c.Send(&audiodv1.Envelope{Body: &audiodv1.Envelope_LogsSet{LogsSet: &audiodv1.LogsSet{ThirdParty: false}}})
	}
	if s.media != nil {
		s.media.MediaReady(c.Send)
		defer s.media.MediaGone()
	}
	if s.soloist != nil {
		s.soloist.SoloistReady(c.Send)
		defer s.soloist.SoloistGone()
	}
	if s.airplay != nil {
		s.airplay.AirplayReady(c.Send)
		defer s.airplay.AirplayGone()
	}
	if s.group != nil {
		s.group.GroupReady(c.Send)
		defer s.group.GroupGone()
	}
	if s.bt != nil {
		s.bt.BluetoothReady(c.Send, c.SendWithFD)
		defer s.bt.BluetoothGone()
	}
	return s.serve(ctx, p, c, fwd)
}

// connect dials audiod and sends it the Configure; again, audiod may still hold the connection a Reconfigure ended,
// and refuses a newcomer meanwhile, so a connection closed before Ready is tried again within the dial timeout
func (s *Supervisor) connect(ctx context.Context, p *proc, again bool) (*Client, *audiodv1.Ready, *audiodv1.Configure) {
	deadline := time.Now().Add(s.t.DialTimeout)
	for {
		c := s.dial(ctx, p)
		if c == nil {
			return nil, nil, nil
		}
		ready, sent, ok := s.handshake(ctx, p, c)
		if ok {
			return c, ready, sent
		}
		c.Close()
		if !again || ctx.Err() != nil || time.Now().After(deadline) {
			return nil, nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, nil, nil
		case <-p.exited:
			return nil, nil, nil
		case <-time.After(s.t.DialEvery):
		}
	}
}

// promptsDir is the install's share/prompts beside audiod's bin, "" for an audiod cadenced does not run: a board
// whose tree differs sets Configure.prompts_dir itself
func promptsDir(spec Spec) string {
	if spec.External || spec.Binary == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(filepath.Dir(spec.Binary)), "share", "prompts")
}

// dial retries until audiod listens; audiod unlinks a stale socket itself
func (s *Supervisor) dial(ctx context.Context, p *proc) *Client {
	deadline := time.NewTimer(s.t.DialTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(s.t.DialEvery)
	defer tick.Stop()
	for {
		if c, err := Dial(ctx, s.spec.Socket, WithLogger(s.log)); err == nil {
			return c
		}
		select {
		case <-ctx.Done():
			return nil
		case <-p.exited:
			return nil
		case <-deadline.C:
			s.log.Warn("audiod_unready", "stage", "dial")
			return nil
		case <-tick.C:
		}
	}
}

func (s *Supervisor) handshake(ctx context.Context, p *proc, c *Client) (*audiodv1.Ready, *audiodv1.Configure, bool) {
	cfg := &audiodv1.Configure{}
	s.mu.Lock()
	// this connection carries the latest Configure: a Reconfigure before now needs no other
	select {
	case <-s.reconnect:
	default:
	}
	if s.configure != nil {
		cfg = proto.Clone(s.configure).(*audiodv1.Configure)
	}
	s.mu.Unlock()
	if cfg.GetPromptsDir() == "" {
		cfg.PromptsDir = promptsDir(s.spec)
	}
	// safe mode restarts with the vendor engines and the keyword engine off: button wake alone, so the output path and
	// alarm tones stay up
	if capture := cfg.GetCapture(); capture != nil && s.safe.Load() {
		if e := capture.GetEngines(); e != nil {
			e.Enabled = false
		}
		capture.Keyword = nil
	}
	s.mu.Lock()
	// informational for audiod, which applies only VolumeSet
	cfg.Volume = &audiodv1.VolumeConfig{Level: wireLevel(s.level)}
	s.mu.Unlock()
	if err := c.Send(&audiodv1.Envelope{Body: &audiodv1.Envelope_Configure{Configure: cfg}}); err != nil {
		return nil, nil, false
	}
	t := time.NewTimer(s.t.ReadyTimeout)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, nil, false
		case <-p.exited:
			return nil, nil, false
		case <-t.C:
			s.log.Warn("audiod_unready", "stage", "ready")
			return nil, nil, false
		case env, ok := <-c.Recv():
			if !ok {
				return nil, nil, false
			}
			switch b := env.GetBody().(type) {
			case *audiodv1.Envelope_Ready:
				return b.Ready, cfg, true
			case *audiodv1.Envelope_LogLine:
				s.logLine(b.LogLine)
			}
		}
	}
}

// publish sends the latest level as the first message after Ready, so audiod
// opens its playback gate before any stream can start, then the duck and the
// mute switch, and only then lets the IO use the connection, so no capture
// starts before the switch is known there.
func (s *Supervisor) publish(c *Client, fwd <-chan *audiodv1.Envelope, sent *audiodv1.Configure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sendVolume(c, s.level)
	sendDuck(c, s.ducked)
	if s.input != nil {
		s.input.InputReady(c.Send)
	}
	a := newIO(c, fwd, s.t.FirstPacket, s.t.FirstPacketVendor)
	// audiod answers a configured alsa capture with a VendorStatus once its engines are settled
	a.vendorPending.Store(sent.GetCapture().GetBackend() == audiodv1.Backend_BACKEND_ALSA)
	s.cur = a
	s.tones = map[string]struct{}{}
}

// unpublish ends the connection's use; each tone it may still have sounded is
// reported gone
func (s *Supervisor) unpublish() {
	s.mu.Lock()
	s.cur = nil
	gone := slices.Sorted(maps.Keys(s.tones))
	s.tones = nil
	s.mu.Unlock()
	if s.toneState == nil {
		return
	}
	for _, id := range gone {
		s.toneState(&audiodv1.ToneState{Id: id, Code: ToneGone})
	}
}

// serve keeps audiod healthy and hands the session envelopes to the IO until audiod dies, ctx ends or a Reconfigure
// asks for a new connection.
func (s *Supervisor) serve(ctx context.Context, p *proc, c *Client, fwd chan<- *audiodv1.Envelope) ending {
	ping := time.NewTicker(s.t.PingEvery)
	defer ping.Stop()
	lastPong := time.Now()
	var seq uint64
	for {
		select {
		case <-ctx.Done():
			return endStop
		case <-p.exited:
			return endCrash
		case <-s.reconnect:
			return endReconnect
		case <-ping.C:
			if time.Since(lastPong) > s.t.PongTimeout {
				s.log.Warn("audiod_unhealthy")
				return endCrash
			}
			seq++
			_ = c.Send(&audiodv1.Envelope{Body: &audiodv1.Envelope_Ping{Ping: &audiodv1.HealthPing{Seq: seq}}})
			// a count held back by the rate limit goes out without waiting for another drop
			s.reportDrops(false)
		case env, ok := <-c.Recv():
			if !ok {
				return endCrash
			}
			switch b := env.GetBody().(type) {
			case *audiodv1.Envelope_Pong:
				lastPong = time.Now()
			case *audiodv1.Envelope_VolumeApplied:
				s.volumeApplied(b.VolumeApplied)
				if s.media != nil {
					s.media.MediaEnvelope(env)
				}
			case *audiodv1.Envelope_LogLine:
				s.logLine(b.LogLine)
			case *audiodv1.Envelope_MediaState, *audiodv1.Envelope_CommandResult, *audiodv1.Envelope_VizFrame,
				*audiodv1.Envelope_ExternalAction, *audiodv1.Envelope_RouteControl, *audiodv1.Envelope_MirrorState,
				*audiodv1.Envelope_AirplayPin, *audiodv1.Envelope_HlsCast:
				if s.media != nil {
					s.media.MediaEnvelope(env)
				}
			case *audiodv1.Envelope_SoloistStatus, *audiodv1.Envelope_SoloistProbeResult, *audiodv1.Envelope_SenderVolume:
				if s.soloist != nil {
					s.soloist.SoloistEnvelope(env)
				}
			case *audiodv1.Envelope_AirplayStatus:
				if s.airplay != nil {
					s.airplay.AirplayEnvelope(env)
				}
			case *audiodv1.Envelope_GroupStatus, *audiodv1.Envelope_GroupCalibrated:
				if s.group != nil {
					s.group.GroupEnvelope(env)
				}
			case *audiodv1.Envelope_BluetoothControl:
				if s.bt != nil {
					s.bt.BluetoothEnvelope(env)
				}
			case *audiodv1.Envelope_VendorStatus:
				if a := s.current(); a != nil {
					a.vendorPending.Store(false)
				}
				if s.input != nil {
					s.input.InputEnvelope(env)
				}
			case *audiodv1.Envelope_WakeEvent, *audiodv1.Envelope_CommandWord, *audiodv1.Envelope_MicPcm:
				if s.input != nil {
					s.input.InputEnvelope(env)
				}
			case *audiodv1.Envelope_TraceMark:
				tracing.Mark(b.TraceMark)
			case *audiodv1.Envelope_SystemVolume:
				if s.system != nil {
					s.system(int(b.SystemVolume.GetLevel()), b.SystemVolume.GetMuted(), false)
				}
			case *audiodv1.Envelope_Ready:
			case *audiodv1.Envelope_ToneState:
				s.toneReported(b.ToneState)
			default:
				select {
				case fwd <- env:
				case <-ctx.Done():
					return endStop
				case <-p.exited:
					return endCrash
				}
			}
		}
	}
}
