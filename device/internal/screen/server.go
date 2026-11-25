// Package screen is cadenced's side of the screen plane (cadence-screen, screen/v1): it accepts the screen on its
// socket, reports it as the capabilities display.screen, media.video, media.mpv, input.remote and media.screencast,
// relays the
// screen's rpc and context to the server and the server's assistant turns, cards and ducks to the screen, registers
// the screen's player with audiod's arbitration, and hands the screen's volume and assistant keys to the board's
// input (rule 7 unchanged: only a press moves the level).
package screen

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/media"
	"git.shino.boo/devilreef/cadence/device/internal/tracing"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
)

const (
	// the screen says Hello within this, or it is dropped
	helloWait = 5 * time.Second
	// a command waits this long for the screen's CommandResult, inside the 10 s a command gets
	commandWait = 8 * time.Second
	// frames waiting for the screen's writer; past it a frame is dropped, never waited for
	outbox = 64
)

// Options say how the board runs its screen plane.
type Options struct {
	// the socket, an abstract name when it starts with @
	Path string
	// the board's media plane: the screen's player is one of its external sessions, and its gate is the screen's
	// PlaybackGate (rule 7). nil: none, and the gate stays shut
	Media *media.Media
	// the screen's volume keys (and its assistant key, with a microphone of its own) act on the board's input; a
	// board that reads its remotes itself (evdev) leaves it off, so no press counts twice
	ForwardKeys bool
	// where audiod serves the screen's tap; empty for none
	TapPath string
	// what the artifact shipped (its MANIFEST), which capabilities are advertised by
	Components    Components
	DaemonVersion string
	// the screen's fallback code typed on the claim screen (design §22) and a new server address from its settings;
	// nil: logged and dropped, as an enrolled cadenced does
	EnrollCode    func(code string)
	ServerAddress func(url string)
	// Screencast hears each media.screencast config that applied: the board's airplay receiver takes it. nil: the
	// screen alone takes it
	Screencast func(Screencast)
	// CoverDir is where the covers of the sessions audiod renders are written for the screen to read (NowPlaying); empty:
	// none shown
	CoverDir string
	// DirectGate: the board runs no audiod, so nobody else confirms the level; the gate opens once the screen's
	// player answered ok to the level in force (DirectLevel) on its connection (rule 7)
	DirectGate bool
	// nil discards
	Log *slog.Logger
}

// Server is the screen plane's one connection at a time; a newer screen replaces the one before.
type Server struct {
	o   Options
	log *slog.Logger

	buttons chan input.ButtonEvent
	nextID  atomic.Uint64

	mu      sync.Mutex
	conn    *peer
	hello   *screenv1.Hello
	status  *screenv1.ScreenStatus
	info    *cadencev2.DeviceInfo
	uplink  func(*cadencev2.Envelope) error
	pending map[string]chan string
	// the configs the server set for the screen's capabilities, sent again to every screen
	configs map[string]string
	// the screen's playing sessions, by id, so a disconnect ends them
	sessions map[string]bool
	// the last mirroring and pin audiod reported
	mirror *screenv1.MirrorStart
	// the claim cadenced holds open while unenrolled (ShowClaim); nil once enrolled
	claim *screenv1.ClaimState
	// the microphone the screen's owner picked (MicChoice); nil: none picked
	mic *screenv1.MicChoice
	// the screen's assistant key is down (a DOWN without its UP yet), so its LONG is no second press
	assistantHeld bool
	// the origin cadenced links to, for the screen's image fetches (LinkStatus.server_url)
	serverURL string
	// the server's own fields of the sessions it owns (the link's media_session_state server -> device), by session
	// id, sent again to a screen that (re)connects; memory-only (D11)
	served map[string]*cadencev2.MediaSessionState
	// the airplay video casts the screen plays, by session id
	hls map[string]*hlsCast
	// the level in force as ShowVolume last told it, sent again (show false) to a screen that connects
	volume *screenv1.VolumeState
	// the household's alarms as the server last pushed them, sent again to a screen that connects, so a ring that
	// began before it shows; memory-only, cleared when the link drops
	alarms *cadencev2.AlarmsSummary

	// DirectGate: the level in force, and whether the attached screen's player holds it (the gate)
	level     int
	hasLevel  bool
	levelHeld bool

	// the player's latest figures, for system.metrics
	stats playerStats

	display    *display
	video      *video
	mpv        *mpv
	remote     *remote
	screencast *screencast
	nowPlaying *nowPlaying
}

type peer struct {
	c    frameConn
	out  chan *screenv1.Envelope
	done chan struct{}
	once sync.Once
}

func (p *peer) close() {
	p.once.Do(func() {
		close(p.done)
		_ = p.c.Close()
	})
}

func New(o Options) *Server {
	s := &Server{
		o: o, log: o.Log, buttons: make(chan input.ButtonEvent, 16), pending: map[string]chan string{},
		configs: map[string]string{}, sessions: map[string]bool{}, served: map[string]*cadencev2.MediaSessionState{},
		hls: map[string]*hlsCast{},
	}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s.display = newDisplay(s)
	s.video = newVideo(s)
	s.mpv = newMpv(s)
	s.remote = newRemote(s)
	s.screencast = newScreencast(s)
	s.nowPlaying = &nowPlaying{dir: o.CoverDir}
	if o.Media != nil {
		o.Media.UseLocal(s.localSession)
		o.Media.UseExternalPlayer(s)
		o.Media.UseMirrorListener(s)
		o.Media.OnGate(func(open bool) { s.send(gateEnvelope(open)) })
	}
	return s
}

// Capabilities are the screen's, those the artifact shipped.
func (s *Server) Capabilities() []capability.Provider {
	var ps []capability.Provider
	if s.o.Components.Has(ComponentScreen) {
		ps = append(ps, s.display, s.video, s.mpv, s.remote)
	}
	if s.o.Components.Has(ComponentAirplay) {
		ps = append(ps, s.screencast)
	}
	return ps
}

// Events are the screen's volume and assistant keys, as the board's buttons: the input's Buttons port.
func (s *Server) Events() <-chan input.ButtonEvent { return s.buttons }

// Run serves the socket until ctx ends.
func (s *Server) Run(ctx context.Context) error {
	if s.o.Path != "" && s.o.Path[0] != '@' {
		// a socket left at the path is a dead cadenced's
		if fi, err := os.Lstat(s.o.Path); err == nil && fi.Mode()&os.ModeSocket != 0 {
			_ = os.Remove(s.o.Path)
		}
	}
	l, err := listen(s.o.Path)
	if err != nil {
		s.log.Error("screen_listen_failed")
		return err
	}
	if s.o.Path[0] != '@' {
		_ = os.Chmod(s.o.Path, 0o600)
	}
	s.log.Info("screen_listening")
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				s.drop(nil)
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			continue
		}
		wg.Go(func() { s.serve(ctx, newFrameConn(c)) })
	}
}

func (s *Server) serve(ctx context.Context, c frameConn) {
	p := &peer{c: c, out: make(chan *screenv1.Envelope, outbox), done: make(chan struct{})}
	defer p.close()
	hello, ok := s.readHello(p)
	if !ok {
		s.log.Warn("screen_refused", "reason", "no_hello")
		return
	}
	go s.write(p)
	s.attach(p, hello)
	defer s.drop(p)
	go func() {
		select {
		case <-ctx.Done():
			p.close()
		case <-p.done:
		}
	}()
	for {
		b, err := p.c.readFrame()
		if err != nil {
			return
		}
		env := &screenv1.Envelope{}
		if proto.Unmarshal(b, env) != nil {
			s.log.Warn("screen_ipc_invalid", "code", "garbage")
			return
		}
		s.handle(env)
	}
}

func (s *Server) readHello(p *peer) (*screenv1.Hello, bool) {
	got := make(chan *screenv1.Hello, 1)
	go func() {
		b, err := p.c.readFrame()
		env := &screenv1.Envelope{}
		if err != nil || proto.Unmarshal(b, env) != nil {
			got <- nil
			return
		}
		got <- env.GetHello()
	}()
	select {
	case h := <-got:
		return h, h != nil
	case <-time.After(helloWait):
		return nil, false
	}
}

func (s *Server) write(p *peer) {
	for {
		select {
		case <-p.done:
			return
		case env := <-p.out:
			b, err := proto.Marshal(env)
			if err == nil {
				err = p.c.writeFrame(b)
			}
			if errors.Is(err, errFrameTooLarge) {
				s.log.Warn("screen_frame_dropped", "reason", "too_large")
				continue
			}
			if err != nil {
				p.close()
				return
			}
		}
	}
}

// attach makes p the screen: Ready, then what it must know before it shows anything
func (s *Server) attach(p *peer, hello *screenv1.Hello) {
	s.mu.Lock()
	old := s.conn
	s.conn, s.hello, s.status, s.levelHeld = p, proto.CloneOf(hello), nil, false
	configs := make(map[string]string, len(s.configs))
	for id, cfg := range s.configs {
		configs[id] = cfg
	}
	mirror := s.mirror
	claim := s.claim
	vol := s.volume
	alarms := s.alarms
	s.mic = nil
	served := make([]*cadencev2.MediaSessionState, 0, len(s.served))
	for _, st := range s.served {
		served = append(served, st)
	}
	s.mu.Unlock()
	if old != nil {
		old.close()
	}
	s.log.Info("screen_connected", "platform", hello.GetPlatform().String(), "width", hello.GetDisplay().GetWidth(),
		"height", hello.GetDisplay().GetHeight(), "local_mic", hello.GetLocalMic())
	s.send(s.ready())
	s.send(s.linkStatus())
	s.send(gateEnvelope(s.gateOpen()))
	s.attachLevel(p)
	if vol != nil {
		s.send(volumeEnvelope(vol, false))
	}
	for id, cfg := range configs {
		s.send(&screenv1.Envelope{Body: &screenv1.Envelope_CapabilityConfig{CapabilityConfig: &screenv1.CapabilityConfig{
			Capability: id, Json: cfg,
		}}})
	}
	if mirror != nil {
		s.send(&screenv1.Envelope{Body: &screenv1.Envelope_MirrorStart{MirrorStart: mirror}})
	}
	if claim != nil {
		s.send(&screenv1.Envelope{Body: &screenv1.Envelope_ClaimState{ClaimState: claim}})
	}
	for _, st := range served {
		s.send(&screenv1.Envelope{Body: &screenv1.Envelope_MediaSessionState{MediaSessionState: st}})
	}
	if alarms != nil {
		s.send(&screenv1.Envelope{Body: &screenv1.Envelope_AlarmsSummary{AlarmsSummary: alarms}})
	}
	if env := s.nowPlaying.attached(); env != nil {
		s.send(env)
	}
	s.display.publish()
	s.mpv.attached()
}

// ShowClaim is the claim an unenrolled cadenced holds open: the screen shows its code and qr (link state
// unenrolled), now and to every screen that connects later.
func (s *Server) ShowClaim(c *screenv1.ClaimState) {
	c = proto.CloneOf(c)
	s.mu.Lock()
	s.claim = c
	s.mu.Unlock()
	s.send(s.linkStatus())
	s.send(&screenv1.Envelope{Body: &screenv1.Envelope_ClaimState{ClaimState: c}})
}

// drop forgets p (nil: whoever is attached): its commands are answered unavailable and its sessions end
func (s *Server) drop(p *peer) {
	s.mu.Lock()
	if p != nil && s.conn != p {
		s.mu.Unlock()
		return
	}
	attached := s.conn != nil
	if s.conn != nil {
		s.conn.close()
	}
	s.conn, s.hello, s.status, s.mic, s.assistantHeld, s.levelHeld = nil, nil, nil, nil, false, false
	pending := s.pending
	s.pending = map[string]chan string{}
	sessions := s.sessions
	s.sessions = map[string]bool{}
	casts := make([]string, 0, len(s.hls))
	for id := range s.hls {
		casts = append(casts, id)
	}
	s.mu.Unlock()
	for _, id := range casts {
		s.endCast(id, 0, 0, "failed")
	}
	for _, ch := range pending {
		ch <- codeUnavailable
	}
	if s.o.Media != nil {
		for id := range sessions {
			s.o.Media.RemoveExternal(id)
		}
	}
	s.video.clear()
	s.display.publish()
	s.mpv.detached()
	if attached {
		// the screen's leases on live cards end with it; it acquires again once back (pkg/rpc CardsRelease)
		s.up(&cadencev2.Envelope{Body: &cadencev2.Envelope_RpcRequest{RpcRequest: &cadencev2.RpcRequest{
			Id: "cadenced-release", Method: rpc.CardsRelease, Json: `{"all":true}`,
		}}})
	}
	tracing.ReleasePrefix(rpcSpanPrefix, "screen_gone")
	s.log.Info("screen_disconnected")
}

// send queues env for the screen, if one is attached; it never blocks
func (s *Server) send(env *screenv1.Envelope) bool {
	s.mu.Lock()
	p := s.conn
	s.mu.Unlock()
	if p == nil {
		return false
	}
	select {
	case p.out <- env:
		return true
	case <-p.done:
		return false
	default:
		s.log.Warn("screen_frame_dropped", "reason", "full")
		return false
	}
}

func (s *Server) gateOpen() bool {
	if s.o.DirectGate {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.levelHeld
	}
	return s.o.Media != nil && s.o.Media.GateOpen()
}

func gateEnvelope(open bool) *screenv1.Envelope {
	return &screenv1.Envelope{Body: &screenv1.Envelope_PlaybackGate{PlaybackGate: &screenv1.PlaybackGate{Open: open}}}
}

func (s *Server) ready() *screenv1.Envelope {
	s.mu.Lock()
	info, lang := s.info, s.language()
	s.mu.Unlock()
	return &screenv1.Envelope{Body: &screenv1.Envelope_Ready{Ready: &screenv1.Ready{
		DaemonVersion: s.o.DaemonVersion, DeviceName: info.GetName(), RoomId: info.GetRoomId(),
		RoomName: info.GetRoomName(), Language: lang, TapPath: s.o.TapPath, GateOpen: s.gateOpen(),
	}}}
}

// request sends what build makes for a fresh request id and waits for the screen's CommandResult
func (s *Server) request(ctx context.Context, build func(id string) *screenv1.Envelope) error {
	id := "s-" + strconv.FormatUint(s.nextID.Add(1), 10)
	answer := make(chan string, 1)
	s.mu.Lock()
	attached := s.conn != nil
	if attached {
		s.pending[id] = answer
	}
	s.mu.Unlock()
	if !attached || !s.send(build(id)) {
		s.forget(id)
		return &capability.CommandError{Code: codeUnavailable}
	}
	t := time.NewTimer(commandWait)
	defer t.Stop()
	select {
	case code := <-answer:
		if code == "" {
			return nil
		}
		return &capability.CommandError{Code: commandCode(code)}
	case <-t.C:
		s.forget(id)
		return &capability.CommandError{Code: codeTimeout}
	case <-ctx.Done():
		s.forget(id)
		return ctx.Err()
	}
}

func (s *Server) forget(id string) {
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
}

func (s *Server) answer(r *screenv1.CommandResult) {
	s.mu.Lock()
	ch, ok := s.pending[r.GetRequestId()]
	delete(s.pending, r.GetRequestId())
	s.mu.Unlock()
	if ok {
		ch <- r.GetCode()
	}
}

// handle acts on one frame from the screen, on its reader
func (s *Server) handle(env *screenv1.Envelope) {
	switch b := env.GetBody().(type) {
	case *screenv1.Envelope_CommandResult:
		s.answer(b.CommandResult)
	case *screenv1.Envelope_CapabilityState:
		s.capabilityState(b.CapabilityState)
	case *screenv1.Envelope_ScreenStatus:
		s.mu.Lock()
		s.status = proto.CloneOf(b.ScreenStatus)
		s.mu.Unlock()
		s.display.publish()
	case *screenv1.Envelope_InputEvent:
		s.input(b.InputEvent)
	case *screenv1.Envelope_MediaState:
		s.mediaState(b.MediaState)
	case *screenv1.Envelope_PlayerStats:
		s.stats.take(b.PlayerStats, time.Now())
	case *screenv1.Envelope_RpcRequest:
		s.rpc(b.RpcRequest)
	case *screenv1.Envelope_MirrorStop:
		s.stopMirror(b.MirrorStop)
	case *screenv1.Envelope_NowPlayingCommand:
		s.nowPlayingCommand(b.NowPlayingCommand)
	case *screenv1.Envelope_RpcCancel:
		s.up(&cadencev2.Envelope{Body: &cadencev2.Envelope_RpcCancel{RpcCancel: b.RpcCancel}})
	case *screenv1.Envelope_ScreenContext:
		s.up(&cadencev2.Envelope{Body: &cadencev2.Envelope_ScreenContext{ScreenContext: b.ScreenContext}})
	case *screenv1.Envelope_LogLine:
		// a line's text is the screen's own and never logged here; only that one came (rule 8)
		s.log.Debug("screen_log_line", "level", b.LogLine.GetLevel().String(), "truncated", b.LogLine.GetTruncated())
	case *screenv1.Envelope_EnrollCode:
		// the code is a credential: never logged
		if s.o.EnrollCode == nil {
			s.log.Info("screen_request_unsupported", "type", bodyName(env))
			return
		}
		s.o.EnrollCode(b.EnrollCode.GetCode())
	case *screenv1.Envelope_ServerAddress:
		if s.o.ServerAddress == nil {
			s.log.Info("screen_request_unsupported", "type", bodyName(env))
			return
		}
		s.o.ServerAddress(b.ServerAddress.GetUrl())
	case *screenv1.Envelope_MicChoice:
		s.mu.Lock()
		s.mic = proto.CloneOf(b.MicChoice)
		s.mu.Unlock()
		s.display.publish()
	case *screenv1.Envelope_Hello:
	default:
		s.log.Info("screen_ipc_unhandled", "type", bodyName(env))
	}
}

func bodyName(env *screenv1.Envelope) string {
	if env.ProtoReflect().WhichOneof(env.ProtoReflect().Descriptor().Oneofs().ByName("body")) == nil {
		return "none"
	}
	return string(env.ProtoReflect().WhichOneof(env.ProtoReflect().Descriptor().Oneofs().ByName("body")).Name())
}
