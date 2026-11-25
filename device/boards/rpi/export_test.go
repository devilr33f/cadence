package rpi

import (
	"context"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// Configure is the output route against the first HDMI card.
var Configure = func(address string) *audiodv1.Configure {
	return routeConfigure("hdmi:CARD=vc4hdmi0", address)
}

// WithRoot reads the device under root; the screen's unit is left alone (no systemctl in tests).
func WithRoot(root string) Option {
	return func(o *options) { o.root, o.screenControl = root, noScreenControl{} }
}

// WithStack fakes the bluetooth stack: the output check's answer, and the backend.
func WithStack(reason string, dial func(ctx context.Context) (bluetooth.Backend, error)) Option {
	return func(o *options) {
		o.qualify = func(context.Context) string { return reason }
		o.qualifyWait = time.Millisecond
		o.dial = dial
	}
}

// UseSupervisor has the speaker's changes and the levels reach r as they would audiod's supervisor.
func (b *Board) UseSupervisor(r interface {
	Reconfigure(*audiodv1.Configure)
	SetVolume(int)
}) {
	b.setupBluetooth()
	b.bt.outMu.Lock()
	defer b.bt.outMu.Unlock()
	b.Board.Volume().Follow(func(level int) func(int) {
		b.bt.level, b.bt.levelKnown = level, true
		r.SetVolume(b.gainLocked(level))
		return b.applyGain(r)
	})
	b.bt.sup = r
}

// PlayerStatus is the media session as the speaker sees it.
var PlayerStatus = playerStatus
