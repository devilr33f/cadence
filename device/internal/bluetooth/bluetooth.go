// Package bluetooth is cadenced's side of Bluetooth receive (capability bluetooth): BlueZ's adapter, our pairing
// agent and window, the paired devices, and bluealsa's pcms, whose audio fd goes to audiod beside a BluetoothStream.
// No audio crosses cadenced. Names and addresses never reach a log: the short id, codes and numbers alone.
package bluetooth

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

const (
	defaultRetry = 5 * time.Second
	controlQueue = 8
)

// Senders is the media sender policy (soloist's): the phone's volume moves the level through it, and every level it
// sets or keeps goes back to the phone.
type Senders interface {
	SenderVolume(sender audiodv1.Sender, level uint32)
	FollowLevel(f func())
}

// Options are the board's.
type Options struct {
	// Dial connects to bluez and bluealsa; Run calls it again after the backend is lost
	Dial func(ctx context.Context) (Backend, error)
	// nil: the phone's volume moves nothing and is never answered
	Senders Senders
	// the level in force, false while the device has none
	Level    func() (int, bool)
	Sessions capability.Sessions
	// how long Run waits before dialing again; 0 is 5 s
	RetryEvery time.Duration
	Now        func() time.Time
	// nil: no bluetooth.output
	Output *OutputOptions
}

// Bluetooth runs the adapter and the phones' streams; its capability is bluetooth.
type Bluetooth struct {
	opts  Options
	log   *slog.Logger
	cap   *provider
	state chan json.RawMessage
	kick  chan struct{}
	ctls  chan *audiodv1.BluetoothControl

	// held while frames go out to audiod, so they arrive in order
	sendMu sync.Mutex
	// held while pairable is set: a window's open and lock, and an outgoing pair's bondable turn, so none clobbers
	// another; taken before mu, never across a pairing itself
	adapterMu sync.Mutex
	// attempts are the reconnects in flight, which serve waits for
	attempts sync.WaitGroup

	mu  sync.Mutex
	cfg Config
	// changes with the config and the device's name; the adapter has them while appliedGen equals it
	cfgGen, appliedGen int
	device             string
	backend            Backend
	snap               Snapshot
	code               string
	// the adapter was present at the last reconcile, and the agent is registered on its bluez
	adapterUp bool
	agentUp   bool
	window    *window
	// outgoing pairings that hold the adapter pairable (bondable) outside a window
	bonding int
	// device paths our agent let pair in a window, to be trusted once paired
	approved map[string]bool
	// audiod's connection: its sends and a counter that changes with each one
	send    func(*audiodv1.Envelope) error
	sendFD  func(*audiodv1.Envelope, int) error
	conn    int
	streams map[string]*stream // by pcm path
	volumes map[string]*pcmVolume
	// the level changed or a phone's move was refused since the phones were told
	levelDirty bool
	// pcm paths whose open failed, logged once until one succeeds
	failed    map[string]bool
	shown     string
	shownCode string
	// nil without bluetooth.output
	out *output
}

type window struct {
	deadline time.Time
	// set once bluez said discoverable: a change to false after it is bluez's own timeout
	sawDiscoverable bool
}

type stream struct {
	id, device string
	rate       uint32
	channels   uint8
	conn       int
	ctrl       *os.File
	player     *audiodv1.BluetoothPlayer
}

type pcmVolume struct {
	seen    uint8
	written uint8
	wrote   bool
}

// New starts with the default config: on, receiving, a 120 s window. A nil log discards.
func New(opts Options, log *slog.Logger) *Bluetooth {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.RetryEvery <= 0 {
		opts.RetryEvery = defaultRetry
	}
	if opts.Level == nil {
		opts.Level = func() (int, bool) { return 0, false }
	}
	b := &Bluetooth{
		opts: opts, log: log, state: make(chan json.RawMessage, 1), kick: make(chan struct{}, 1),
		ctls: make(chan *audiodv1.BluetoothControl, controlQueue), cfg: DefaultConfig(),
		approved: map[string]bool{}, streams: map[string]*stream{}, volumes: map[string]*pcmVolume{},
		failed: map[string]bool{},
	}
	b.cap = &provider{b: b}
	if opts.Output != nil {
		b.out = newOutput(*opts.Output)
	}
	if opts.Senders != nil {
		opts.Senders.FollowLevel(b.LevelChanged)
	}
	b.mu.Lock()
	b.publishLocked()
	b.mu.Unlock()
	return b
}

// Capability is bluetooth.
func (b *Bluetooth) Capability() capability.Provider { return b.cap }

// SetDeviceName is the device's name, the adapter's alias while the config sets none; it never blocks.
func (b *Bluetooth) SetDeviceName(name string) {
	b.mu.Lock()
	if b.device != name {
		b.device = name
		b.cfgGen++
	}
	b.mu.Unlock()
	b.wake()
}

func (b *Bluetooth) wake() {
	select {
	case b.kick <- struct{}{}:
	default:
	}
}

// Run dials the backend, and again whenever it is lost, and keeps the adapter and the streams as the config says
// until ctx ends.
func (b *Bluetooth) Run(ctx context.Context) {
	done := make(chan struct{})
	defer func() { <-done }()
	go func() {
		defer close(done)
		b.presses(ctx)
	}()
	for ctx.Err() == nil {
		be, err := b.opts.Dial(ctx)
		if err == nil {
			b.serve(ctx, be)
			_ = be.Close()
			b.lost()
			// no bus, no speaker
			b.route(Snapshot{})
		} else if ctx.Err() == nil {
			b.setCode("bus_failed")
		}
		t := time.NewTimer(b.opts.RetryEvery)
		select {
		case <-ctx.Done():
		case <-t.C:
		}
		t.Stop()
	}
}

func (b *Bluetooth) serve(parent context.Context, be Backend) {
	ctx, cancel := context.WithCancel(parent)
	defer func() {
		cancel()
		b.attempts.Wait()
	}()
	b.mu.Lock()
	b.backend, b.agentUp, b.adapterUp = be, false, false
	b.cfgGen++
	if b.out != nil {
		b.out.rc.reset()
		b.out.playerUp = false
	}
	b.mu.Unlock()
	changes := be.Changes()
	for {
		b.reconcile(ctx, be)
		var expire <-chan time.Time
		var timer *time.Timer
		b.mu.Lock()
		if w := b.window; w != nil {
			timer = time.NewTimer(max(w.deadline.Sub(b.opts.Now()), 0))
			expire = timer.C
		}
		b.mu.Unlock()
		stop := func() {
			if timer != nil {
				timer.Stop()
			}
		}
		select {
		case <-ctx.Done():
			stop()
			return
		case _, ok := <-changes:
			if !ok {
				stop()
				b.log.Warn("bluetooth_backend_lost")
				return
			}
		case <-b.kick:
		case <-expire:
		case c := <-b.ctls:
			b.control(ctx, be, c)
		}
		stop()
	}
}

// lost follows the backend's end: no adapter, no window, no stream
func (b *Bluetooth) lost() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.backend, b.adapterUp, b.agentUp, b.window = nil, false, false, nil
	if b.out != nil {
		b.out.rc.reset()
	}
	b.snap = Snapshot{}
	for path, st := range b.streams {
		b.endLocked(path, st, "lost")
	}
	clear(b.volumes)
	b.code = "bus_failed"
	b.publishLocked()
}

func (b *Bluetooth) setCode(code string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.code = code
	b.publishLocked()
}

// Authorize is our agent: a pairing or a service is let through only while a window is open, or a pairing we
// started with that very device (bluez's own config leaves the adapter always pairable, and an outgoing pair holds
// it pairable for a moment, so this is the lock).
func (b *Bluetooth) Authorize(req AgentRequest) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.idLocked(req.Address)
	if req.Kind == RequestPairing && b.cfg.Enabled && b.outgoingLocked(req.Device) {
		// a speaker the owner chose in a woken session, which we pair to
		b.log.Info("bluetooth_pair_accepted", "id", id, "kind", req.Kind, "direction", "outgoing")
		return true
	}
	if w := b.window; w == nil || !b.opts.Now().Before(w.deadline) || !b.cfg.Enabled || !b.cfg.Receive {
		b.log.Info("bluetooth_pair_refused", "id", id, "kind", req.Kind, "reason", "no_window")
		return false
	}
	b.approved[req.Device] = true
	b.log.Info("bluetooth_pair_accepted", "id", id, "kind", req.Kind)
	return true
}

var _ Agent = (*Bluetooth)(nil)

func (b *Bluetooth) idLocked(address string) string {
	if b.snap.Adapter == nil || address == "" {
		return ""
	}
	return ShortID(b.snap.Adapter.Address, address)
}
