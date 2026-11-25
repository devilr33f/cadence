package rpi

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
	"git.shino.boo/devilreef/cadence/device/internal/bluetooth/bluez"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

const (
	// trixie's system bus and the onboard adapter
	systemBus = "/run/dbus/system_bus_socket"
	adapter   = "hci0"
	// the qualification waits this long for bluez and bluealsa, which systemd may still be starting
	qualifyTries = 5
	qualifyEvery = time.Second
)

type bluetoothOptions struct {
	// the output check; nil is bluez.QualifyOutput on the board's bus
	qualify func(ctx context.Context) string
	// between the check's tries; 0 is qualifyEvery
	qualifyWait time.Duration
	// the backend; nil dials bluez on the board's bus
	dial func(ctx context.Context) (bluetooth.Backend, error)
}

// boardBluetooth is the pi's receiver and routable speaker beside the screen's own capabilities.
type boardBluetooth struct {
	once    sync.Once
	manager *bluetooth.Bluetooth
	caps    []capability.Provider
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	// the speaker audiod plays to, and audiod's supervisor once it runs
	outMu   sync.Mutex
	speaker string
	sup     reconfigurer
	// the speaker's own volume carries the level (audiod's gain at full), and the level audiod last had
	hard       bool
	level      int
	levelKnown bool
}

func (b *Board) setupBluetooth() {
	b.bt.once.Do(func() {
		// the shared screen board owns the level, sessions, media and sender policy; bluetooth is another consumer
		b.Board.Capabilities()
		b.bt.ctx, b.bt.cancel = context.WithCancel(context.Background())
		opts := bluetooth.Options{
			Dial: b.Bluetooth().Dial, Senders: b.Senders(), Level: b.Volume().Current,
			Sessions: b.Sessions(),
		}
		if reason := b.qualifyBluetoothOutput(); reason == "" {
			opts.Output = &bluetooth.OutputOptions{
				Route: b.route, Lower: b.lower, KnownFile: filepath.Join(b.dataDir, "bluetooth", "speakers"),
				Hardware: b.hardware, SpeakerLevel: b.speakerLevel, Control: b.control,
			}
		} else {
			b.log.Warn("bluetooth_output_unavailable", "reason", reason)
		}
		b.bt.manager = bluetooth.New(opts, b.log)
		b.Board.Volume().Listen(func(int, volume.Origin) { b.bt.manager.LevelChanged() })
		b.Board.Media().WatchSession(func(s *cadencev2.MediaSessionState) { b.bt.manager.SetPlaying(playerStatus(s)) })
		b.bt.caps = []capability.Provider{b.bt.manager.Capability()}
		if out := b.bt.manager.OutputCapability(); out != nil {
			b.bt.caps = append(b.bt.caps, out)
		}
		b.bt.wg.Go(func() { b.bt.manager.Run(b.bt.ctx) })
	})
}

// qualifyBluetoothOutput reports bluetooth.output only when bluez is on the bus and bluealsa has a2dp-source.
func (b *Board) qualifyBluetoothOutput() string {
	check := b.o.qualify
	if check == nil {
		bus := b.Bluetooth().Bus
		check = func(ctx context.Context) string { return bluez.QualifyOutput(ctx, bus) }
	}
	wait := b.o.qualifyWait
	if wait <= 0 {
		wait = qualifyEvery
	}
	var reason string
	for i := range qualifyTries {
		if i > 0 {
			select {
			case <-b.bt.ctx.Done():
				return reason
			case <-time.After(wait):
			}
		}
		ctx, cancel := context.WithTimeout(b.bt.ctx, qualifyEvery)
		reason = check(ctx)
		cancel()
		if reason == "" {
			return ""
		}
	}
	return reason
}

// Bluetooth is the onboard adapter on trixie's system bus. The bus is under the board's root in tests.
func (b *Board) Bluetooth() board.BluetoothProfile {
	p := board.BluetoothProfile{Bus: "unix:path=" + under(b.o.root, systemBus), Adapter: adapter, Dial: b.o.dial}
	if p.Dial == nil {
		p.Dial = func(ctx context.Context) (bluetooth.Backend, error) {
			be, err := bluez.Dial(ctx, p.Bus, p.Adapter)
			if err != nil {
				return nil, err
			}
			return be, nil
		}
	}
	return p
}

func (b *Board) Capabilities() []capability.Provider {
	caps := b.Board.Capabilities()
	b.setupBluetooth()
	b.setupHDMI()
	return append(caps, b.bt.caps...)
}

func (b *Board) AudioIO() (audio.IO, bool) {
	b.setupBluetooth()
	b.setupHDMI()
	return b.Board.AudioIO()
}

// SetDeviceName names the adapter too, while bluetooth's config sets no alias.
func (b *Board) SetDeviceName(name string) {
	b.setupBluetooth()
	if b.bt.manager != nil {
		b.bt.manager.SetDeviceName(name)
	}
	b.Board.SetDeviceName(name)
}

func (b *Board) Close() error {
	b.closeHDMI()
	b.bt.once.Do(func() {})
	if b.bt.cancel != nil {
		b.bt.cancel()
	}
	err := b.Board.Close()
	b.bt.wg.Wait()
	return err
}

var (
	_ board.Bluetooth      = (*Board)(nil)
	_ board.Availabilities = (*Board)(nil)
)
