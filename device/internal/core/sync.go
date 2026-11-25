package core

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/alarms"
	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/discovery"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/link"
	"git.shino.boo/devilreef/cadence/device/internal/ops"
	"git.shino.boo/devilreef/cadence/device/internal/session"
	"git.shino.boo/devilreef/cadence/device/internal/syncstore"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
)

const (
	protocolVersion = 3
	welcomeWait     = 10 * time.Second
)

var errNoWelcome = errors.New("sync: first frame is not a welcome")

// Sync keeps the board's capability config and state in step with the server.
// It is the link's handler: one link at a time.
type Sync struct {
	board   board.Board
	store   *syncstore.Store
	version string
	log     *slog.Logger
	// the board's capabilities, then core's own, extra
	caps    []capability.Provider
	extra   []capability.Provider
	reports []*cadencev2.CapabilityReport // the parts in order, each with the whole report's hash
	byID    map[string]capability.Provider
	// per capability, its command schemas by name
	commands       map[string]map[string]string
	commandTimeout time.Duration
	// the capabilities that watch the link (network.wifi), and their requests to dial again at once
	linkWatchers []capability.LinkWatcher
	redial       chan struct{}
	// nil without lights
	ind *indicator
	// the board's audio, set by StartAudio before any link
	aio      audio.IO
	hasAudio bool
	// the board's ringer, set by StartAudio before any link; nil for none
	ringer *alarms.Ringer
	// the board's input and level, set by StartAudio before any link; nil for none
	input *input.Input
	vol   *volume.Setter
	// what the sessions do, for the command words; every link's controller keeps it
	activity     *session.Activity
	recheckEvery time.Duration

	mu sync.Mutex
	// wordSend sends the spotter's home phrases to the server, nil with no link
	wordSend func(*cadencev2.Envelope) error
	latest   map[string]json.RawMessage   // per capability, kept across links
	media    *cadencev2.MediaSessionState // the latest, kept across links
	// the device's name as the server last sent it, or as the store kept it
	name      string
	nameKnown bool
	stop      context.CancelFunc
	// the triggers of the link's controller, nil while no link is up
	triggers chan audio.Trigger
	// set by Connected, read by Message, cleared by Disconnected: one goroutine
	ctl   *session.Controller
	slots chan struct{} // the link's commands in flight
	wg    sync.WaitGroup

	// where every link's controller counts its sessions, nil for nowhere
	counts session.Counts

	// this device's id, which a session grant must name as its target; "" refuses every grant
	deviceID string
	// the device's lan addresses, read at every hello
	lanAddresses func() []string
	grants       *grants

	// what the log scrubber was told of the secrets, in force and kept
	secrets knownSecrets

	// tells the owner a request failed, on the board's speaker and lights; never nil
	errors *session.ErrorSounds
	// where the ringer streams an alarm's music and a reminder's speech from; nil for none
	ringSounds alarms.Sounds
	// the daemon's own update executor, nil when no server url was given (core's tests)
	ops *ops.Executor

	// which capabilities have their hardware now (board.Availabilities); nil for always. availChanged is its one
	// subscription, read by the link's forwarder
	avail        *capability.Availability
	availChanged <-chan struct{}
	// sendMu orders each state delta with the availability it carries; sent is what the link's last delta of each
	// capability said, under sendMu
	sendMu sync.Mutex
	sent   map[string]bool
}

// SyncOption adds what core composes around the board.
type SyncOption func(*Sync)

// WithCounts counts every session begun, on any link, in c.
func WithCounts(c session.Counts) SyncOption { return func(s *Sync) { s.counts = c } }

// WithDeviceID is the device's enrolled id, which the server's session grants name as their target.
func WithDeviceID(id string) SyncOption { return func(s *Sync) { s.deviceID = id } }

// withRingSounds has the ringer stream the server's ring sounds from serverURL with the device token.
func withRingSounds(serverURL, token string) SyncOption {
	return func(s *Sync) { s.ringSounds = alarms.ServerSounds(serverURL, token) }
}

// withOps lets the daemon run the server's update plans for itself: it fetches plan and files from
// serverURL with the device token, exec steps go through the board's shell. A session or a ring
// refuses the run (rule 7).
//
//nolint:unused // not composed by runLinked yet, so ops_start is never answered
func withOps(serverURL, token string, roots *x509.CertPool) SyncOption {
	return func(s *Sync) {
		c, err := otlp.NewClient(serverURL, token, roots)
		if err != nil {
			return
		}
		shell := "/bin/sh"
		if sh, ok := s.board.(board.Sheller); ok {
			shell = sh.Shell()
		}
		s.ops = ops.New(ops.Deps{Get: c, Shell: shell, Log: s.log, Busy: s.opsBusy})
	}
}

// opsBusy answers why the daemon must not restart now.
//
//nolint:unused // withOps' busy check, unused while withOps is
func (s *Sync) opsBusy() (string, bool) {
	if s.activity != nil && s.activity.Snapshot().Session != "" {
		return "session", true
	}
	if s.ringer != nil && s.ringer.Ringing() {
		return "ring", true
	}
	return "", false
}

// WithLANAddresses reads the addresses each Hello carries from f in place of the interfaces.
func WithLANAddresses(f func() []string) SyncOption { return func(s *Sync) { s.lanAddresses = f } }

// withLights reports the owner's switches beside the board's capabilities and shows the sessions on the lights; nil
// adds nothing.
func withLights(l *lights) SyncOption {
	return func(s *Sync) {
		if l != nil {
			s.extra, s.ind = append(s.extra, l.capabilities()...), l.ind
		}
	}
}

// withCapabilities reports ps beside the board's capabilities.
func withCapabilities(ps ...capability.Provider) SyncOption {
	return func(s *Sync) { s.extra = append(s.extra, ps...) }
}

// NewSync expects unique capability ids. It fails with a
// *capability.TooLargeError when a descriptor fits no report frame.
func NewSync(b board.Board, store *syncstore.Store, version string, log *slog.Logger, opts ...SyncOption) (*Sync, error) {
	s := &Sync{
		board: b, store: store, version: version, log: log, commandTimeout: commandWait, latest: map[string]json.RawMessage{},
		activity: &session.Activity{}, recheckEvery: recheckEvery, grants: newGrants(), lanAddresses: discovery.LANAddresses,
	}
	s.errors = session.NewErrorSounds(log)
	for _, o := range opts {
		o(s)
	}
	if s.ind != nil {
		s.errors.UseLights(s.ind)
	}
	s.caps = slices.Concat(b.Capabilities(), s.extra)
	if ab, ok := b.(board.Availabilities); ok {
		s.avail = ab.Availability()
		s.availChanged = s.avail.Changed()
	}
	reports, err := capability.Reports(s.caps)
	if err != nil {
		return nil, err
	}
	s.reports = reports
	s.byID = make(map[string]capability.Provider, len(s.caps))
	s.commands = make(map[string]map[string]string, len(s.caps))
	s.redial = make(chan struct{}, 1)
	for _, p := range s.caps {
		d := p.Descriptor()
		s.byID[d.ID], s.commands[d.ID] = p, d.Commands
		if di, ok := p.(capability.DeviceIdentified); ok && s.deviceID != "" {
			di.SetDeviceID(s.deviceID)
		}
		if lw, ok := p.(capability.LinkWatcher); ok {
			lw.SetRedial(s.requestRedial)
			s.linkWatchers = append(s.linkWatchers, lw)
		}
	}
	return s, nil
}

// Redials says when a capability asked the link to dial again at once (a wi-fi switch changed the network under it).
func (s *Sync) Redials() <-chan struct{} { return s.redial }

func (s *Sync) requestRedial() {
	select {
	case s.redial <- struct{}{}:
	default:
	}
}

// linkChanged tells the capabilities that watch the link.
func (s *Sync) linkChanged(up bool) {
	for _, lw := range s.linkWatchers {
		lw.LinkChanged(up)
	}
}

// Restore re-applies every stored config at boot, so the last applied one is
// in force before, and without, a link.
func (s *Sync) Restore(ctx context.Context) {
	s.restoreDeviceName()
	for _, p := range s.caps {
		id := p.Descriptor().ID
		e, ok, err := s.store.LoadEntry(id)
		if err != nil {
			s.log.Error("config_restore_failed", "capability", id, "code", "store_unreadable")
			continue
		}
		if !ok {
			continue
		}
		if err := s.applyTo(ctx, p, e.Generation, e.Config, declared(p, e.Secrets)); err != nil {
			s.log.Error("config_restore_failed", "capability", id, "generation", e.Generation, "code", applyCode(err))
			continue
		}
		s.log.Info("config_restored", "capability", id, "generation", e.Generation)
	}
}

func (s *Sync) Connected(ctx context.Context, sess link.Session) error {
	err := sess.Send(&cadencev2.Envelope{Body: &cadencev2.Envelope_Hello{Hello: &cadencev2.Hello{
		Protocol: protocolVersion, DaemonVersion: s.version, Board: s.board.ID(), CapabilitiesHash: s.reports[0].GetHash(),
		LanAddresses: s.lanAddresses(),
	}}})
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, welcomeWait)
	env, err := sess.Recv(wctx)
	cancel()
	if err != nil {
		return err
	}
	w := env.GetWelcome()
	if w == nil {
		return errNoWelcome
	}
	if w.GetSendFullReport() {
		for _, r := range s.reports {
			if err := sess.Send(&cadencev2.Envelope{Body: &cadencev2.Envelope_CapabilityReport{CapabilityReport: r}}); err != nil {
				return err
			}
		}
	}
	s.deviceInfo(w.GetDeviceInfo())
	ctx, cancel = context.WithCancel(ctx)
	s.mu.Lock()
	s.stop = cancel
	s.mu.Unlock()
	if lp, ok := s.board.(board.LinkPeer); ok {
		lp.LinkUp(sess.Send, w.GetDeviceInfo())
	}
	s.linkChanged(true)
	if ml, ok := s.board.(board.MediaLinker); ok {
		ml.MediaLinked()
	}
	s.slots = make(chan struct{}, commandsInFlight)
	if s.ops != nil {
		s.ops.SetSend(sess.Send)
	}
	s.mu.Lock()
	s.wordSend = sess.Send
	s.mu.Unlock()
	s.forward(ctx, sess)
	s.forwardMedia(ctx, sess)
	s.startSession(ctx, sess)
	return nil
}

func (s *Sync) Message(ctx context.Context, sess link.Session, env *cadencev2.Envelope) error {
	if s.ctl != nil && s.ctl.Handle(env) {
		return nil
	}
	if mo := env.GetMicOpen(); mo != nil {
		s.micOpen(mo)
		return nil
	}
	// a rename reaches the receivers, and a screen's relay too
	info := env.GetDeviceInfo()
	s.deviceInfo(info)
	if lp, ok := s.board.(board.LinkPeer); ok && lp.LinkMessage(env) {
		return nil
	}
	if info != nil {
		return nil
	}
	if d := env.GetDuck(); d != nil {
		if sd, ok := s.board.(board.ServerDucker); ok {
			sd.ServerDuck(d)
		}
		return nil
	}
	if os := env.GetOpsStart(); os != nil {
		if s.ops != nil {
			s.ops.Start(ctx, os.GetRunId())
		}
		return nil
	}
	if cc := env.GetCapabilityCommand(); cc != nil {
		return s.command(ctx, sess, cc)
	}
	if cs := env.GetConfigSet(); cs != nil {
		if a := s.apply(ctx, cs); a != nil {
			return sess.Send(&cadencev2.Envelope{Body: &cadencev2.Envelope_ConfigApplied{ConfigApplied: a}})
		}
	}
	return nil
}

func (s *Sync) Disconnected() {
	s.mu.Lock()
	s.wordSend = nil
	s.mu.Unlock()
	s.mu.Lock()
	stop := s.stop
	s.stop, s.triggers = nil, nil
	s.mu.Unlock()
	if stop != nil {
		stop()
		if lp, ok := s.board.(board.LinkPeer); ok {
			lp.LinkDown()
		}
		s.linkChanged(false)
	}
	s.wg.Wait()
	s.ctl, s.slots = nil, nil
}

// apply answers a ConfigSet: a generation up to the stored one with the stored
// config is acknowledged as it is; any other is applied and stored on success,
// since the server starts generations over after a reset or a re-enrollment.
// It returns nil, no answer, when the store failed.
func (s *Sync) apply(ctx context.Context, cs *cadencev2.ConfigSet) *cadencev2.ConfigApplied {
	id, gen := cs.GetCapability(), cs.GetGeneration()
	ack := func(code string) *cadencev2.ConfigApplied {
		return &cadencev2.ConfigApplied{Capability: id, Generation: gen, ErrorCode: code}
	}
	p, ok := s.byID[id]
	if !ok {
		s.log.Warn("config_apply_failed", "capability", id, "generation", gen, "code", "unknown_capability")
		return ack("unknown_capability")
	}
	cfg := json.RawMessage(cs.GetJson())
	secrets := declared(p, cs.GetSecrets())
	// an unreadable entry is applied over; the next good apply replaces it
	if e, ok, err := s.store.LoadEntry(id); err == nil && ok && gen <= e.Generation && sameJSON(cfg, e.Config) && maps.Equal(secrets, e.Secrets) {
		return ack("")
	}
	if err := s.applyTo(ctx, p, gen, cfg, secrets); err != nil {
		code := applyCode(err)
		s.log.Warn("config_apply_failed", "capability", id, "generation", gen, "code", code)
		return ack(code)
	}
	if err := s.store.SaveEntry(id, syncstore.Entry{Generation: gen, Config: cfg, Secrets: secrets}); err != nil {
		// any answer marks gen applied on the server, though a restart would bring
		// back the older stored config; unanswered, it stays pending and comes
		// again on the next link, where the store gets another try
		s.log.Error("config_apply_failed", "capability", id, "generation", gen, "code", "store_failed")
		return nil
	}
	s.log.Info("config_applied", "capability", id, "generation", gen)
	return ack("")
}

// applyTo tells the log scrubber of the secrets before p is handed them. A
// config p refuses leaves the one in force as it was, and its secrets stay
// redacted beside those in force: p may have taken them before it refused. A
// value a config p takes replaces or deletes stays redacted too.
func (s *Sync) applyTo(ctx context.Context, p capability.Provider, gen int64, cfg json.RawMessage, secrets map[string]string) error {
	sa, ok := p.(capability.SecretApplier)
	if !ok {
		return p.Apply(ctx, gen, cfg)
	}
	id := p.Descriptor().ID
	s.secrets.offer(id, secrets)
	err := sa.ApplySecrets(ctx, gen, cfg, secrets)
	s.secrets.settle(id, secrets, err == nil)
	return err
}

// declared keeps the secrets p declares and takes; any other is neither
// handed over nor stored
func declared(p capability.Provider, in map[string]string) map[string]string {
	if _, ok := p.(capability.SecretApplier); !ok {
		return nil
	}
	var out map[string]string
	for _, spec := range p.Descriptor().Secrets {
		if v, ok := in[spec.Name]; ok {
			if out == nil {
				out = map[string]string{}
			}
			out[spec.Name] = v
		}
	}
	return out
}

// sameJSON reports whether a and b hold one JSON value, whatever their spacing,
// key order or escaping: the store's encoder may re-escape what it saves.
func sameJSON(a, b []byte) bool {
	decode := func(raw []byte) (any, bool) {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var v any
		if d.Decode(&v) != nil || d.More() {
			return nil, false
		}
		return v, true
	}
	x, okA := decode(a)
	y, okB := decode(b)
	return okA && okB && reflect.DeepEqual(x, y)
}

// applyCode is the code the server may see: never the error's own text.
func applyCode(err error) string {
	var ae *capability.ApplyError
	if errors.As(err, &ae) && ae.Code != "" {
		return ae.Code
	}
	return "apply_failed"
}

// forward sends each provider's latest state at once, then every new one,
// until the link ends; each delta says whether its capability is available,
// and a change of availability alone sends the capability's latest state
// again (none, an empty json, while it has none).
func (s *Sync) forward(ctx context.Context, sess link.Session) {
	s.sendMu.Lock()
	s.sent = map[string]bool{}
	s.sendMu.Unlock()
	if s.availChanged != nil {
		s.wg.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-s.availChanged:
					s.resendAvailability(sess)
				}
			}
		})
	}
	for _, p := range s.caps {
		id, ch := p.Descriptor().ID, p.State()
		s.wg.Go(func() {
			select {
			case v := <-ch:
				s.remember(id, v)
			default:
			}
			if v, ok := s.last(id); ok || !s.avail.Available(id) {
				s.sendState(sess, id, v)
			}
			for {
				select {
				case <-ctx.Done():
					return
				case v := <-ch:
					s.remember(id, v)
					s.sendState(sess, id, v)
				}
			}
		})
	}
}

func (s *Sync) remember(id string, v json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latest[id] = v
}

func (s *Sync) last(id string) (json.RawMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.latest[id]
	return v, ok
}

// sendState drops a failed send: the state is kept and goes out on the next link.
func (s *Sync) sendState(sess link.Session, id string, v json.RawMessage) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.sendLocked(sess, id, v)
}

// sendLocked is sendState under sendMu: the delta carries the availability in force as it is sent
func (s *Sync) sendLocked(sess link.Session, id string, v json.RawMessage) {
	available := s.avail.Available(id)
	err := sess.Send(&cadencev2.Envelope{Body: &cadencev2.Envelope_StateDelta{StateDelta: &cadencev2.StateDelta{
		Capability: id, Json: string(v), Unavailable: !available,
	}}})
	if errors.Is(err, wire.ErrFrameTooLarge) {
		s.log.Warn("state_dropped", "capability", id, "reason", "too_large")
	}
	if s.sent != nil {
		s.sent[id] = available
	}
}

// resendAvailability sends the latest state of each capability whose availability is not what the link last heard;
// one never sent counts as available. The latest state is read under sendMu, so it is never older than one the
// capability's own forwarder already sent.
func (s *Sync) resendAvailability(sess link.Session) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	for _, p := range s.caps {
		id := p.Descriptor().ID
		now := s.avail.Available(id)
		was, ok := s.sent[id]
		if !ok {
			was = true
		}
		if was == now {
			continue
		}
		v, _ := s.last(id)
		s.sendLocked(sess, id, v)
		s.log.Info("capability_availability", "capability", id, "available", now)
	}
}
