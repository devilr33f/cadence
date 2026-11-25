package soloist

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sync"

	"git.shino.boo/devilreef/cadence/device/internal/volume"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// policy is rule 7 for media senders (reference §5): a sender's deliberate
// slider move sets the level as a command would, unless the device has no
// level of its own or a ring sounds, and every change of the level goes back
// to soloist and airplay, the mover's own included: audiod records it as an echo, and
// its sender guard learns the speaker's level from it alone. audiod has
// already dropped echoes and first values; bluetooth, whose volume cadenced
// hears itself, drops its own and hears the level back through its follower.
type policy struct {
	vol     *volume.Setter
	ringing func() bool
	// back tells the senders the level in force; it may block, so only run calls it
	back func()
	log  *slog.Logger

	mu      sync.Mutex
	pending *request // the latest request not handled yet
	dirty   bool     // the level changed since soloist was told
	kick    chan struct{}
	// more senders told the level in force, after back
	followers []func()
}

type request struct {
	sender audiodv1.Sender
	level  uint32
}

func newPolicy(vol *volume.Setter, ringing func() bool, back func(), log *slog.Logger) *policy {
	p := &policy{vol: vol, ringing: ringing, back: back, log: log, kick: make(chan struct{}, 1)}
	vol.Listen(p.changed)
	return p
}

// follow has f told the level in force with back, from run
func (p *policy) follow(f func()) {
	p.mu.Lock()
	p.followers = append(p.followers, f)
	p.mu.Unlock()
}

// tell runs back and every follower
func (p *policy) tell() {
	p.back()
	p.mu.Lock()
	fs := p.followers
	p.mu.Unlock()
	for _, f := range fs {
		f()
	}
}

// request takes a sender's slider move; it never blocks, and a newer one
// replaces one not handled yet
func (p *policy) request(sender audiodv1.Sender, level uint32) {
	p.mu.Lock()
	p.pending = &request{sender: sender, level: level}
	p.mu.Unlock()
	p.wake()
}

// changed runs under the setter's lock, so it only marks the level to go back;
// run sends it after whatever it is doing, a request's own change included
func (p *policy) changed(int, volume.Origin) {
	p.mu.Lock()
	p.dirty = true
	p.mu.Unlock()
	p.wake()
}

func (p *policy) wake() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

func (p *policy) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.kick:
		}
		p.mu.Lock()
		r, dirty := p.pending, p.dirty
		p.pending, p.dirty = nil, false
		p.mu.Unlock()
		if dirty {
			p.tell()
		}
		if r != nil {
			p.apply(*r)
		}
	}
}

func (p *policy) apply(r request) {
	sender := senderName(r.sender)
	if sender == "unknown" {
		p.log.Warn("media_volume_refused", "sender", sender, "reason", "unsupported")
		return
	}
	if p.ringing != nil && p.ringing() {
		p.refuse(sender, "ring")
		return
	}
	got, err := p.vol.SetKnown(int(min(r.level, math.MaxInt32)), volume.OriginSender)
	switch {
	case errors.Is(err, volume.ErrUnknown):
		p.refuse(sender, "unknown")
	case err != nil:
		p.refuse(sender, "store")
	default:
		p.log.Info("media_volume", "sender", sender, "volume", got)
	}
}

// refuse: the slider moved all the same, so it goes back to the level in
// force, when the device has one
func (p *policy) refuse(sender, reason string) {
	p.log.Warn("media_volume_refused", "sender", sender, "reason", reason)
	p.tell()
}

func senderName(s audiodv1.Sender) string {
	switch s {
	case audiodv1.Sender_SENDER_SOLOIST:
		return "soloist"
	case audiodv1.Sender_SENDER_AIRPLAY:
		return "airplay"
	case audiodv1.Sender_SENDER_BLUETOOTH:
		return "bluetooth"
	default:
		return "unknown"
	}
}
