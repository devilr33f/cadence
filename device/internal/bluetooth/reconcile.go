package bluetooth

import (
	"context"
	"slices"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/receivername"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// reconcile reads everything once and brings the adapter, the window, trust, the streams, the players and the
// phones' volume in line with it; only Run's goroutine calls it, and backend calls run outside mu
func (b *Bluetooth) reconcile(ctx context.Context, be Backend) {
	snap, err := be.Snapshot(ctx)
	if err != nil {
		if ctx.Err() == nil {
			b.setCode("bus_failed")
		}
		return
	}
	b.mu.Lock()
	b.snap = snap
	b.mu.Unlock()
	b.adapter(ctx, be, snap)
	b.trust(ctx, be, snap)
	b.streamsFrom(ctx, be, snap)
	b.players(snap)
	b.volume(ctx, be, snap)
	b.route(snap)
	b.speakerVolume(ctx, be, snap)
	b.player(ctx, be, snap)
	b.reconnect(ctx, be, snap)
	b.mu.Lock()
	switch {
	case snap.Adapter == nil:
		b.code = "no_adapter"
	case !snap.Bluealsa:
		b.code = "bluealsa_missing"
	default:
		b.code = ""
	}
	b.publishLocked()
	b.mu.Unlock()
}

// adapter registers the agent on each bluez that has the adapter, applies the config, ends a window bluez or the
// clock ended, and locks pairable and discoverable outside one
func (b *Bluetooth) adapter(ctx context.Context, be Backend, snap Snapshot) {
	a := snap.Adapter
	b.mu.Lock()
	if a == nil {
		if b.adapterUp {
			b.log.Warn("bluetooth_adapter_gone")
		}
		b.adapterUp, b.agentUp = false, false
		b.cfgGen++
		if b.window != nil {
			b.window = nil
			b.log.Info("bluetooth_window_closed", "reason", "lost")
		}
		b.mu.Unlock()
		return
	}
	b.adapterUp = true
	cfg, gen, agentUp := b.cfg, b.cfgGen, b.agentUp
	applied := b.appliedGen == gen
	alias := receivername.Pick(cfg.Name, b.device, maxName)
	reason := ""
	if w := b.window; w != nil {
		switch {
		case !cfg.Enabled || !cfg.Receive:
			reason = "disabled"
		case !b.opts.Now().Before(w.deadline):
			reason = "timeout"
		case w.sawDiscoverable && !a.Discoverable:
			reason = "bluez"
		case a.Discoverable:
			w.sawDiscoverable = true
		}
		if reason != "" {
			b.window = nil
			b.log.Info("bluetooth_window_closed", "reason", reason)
		}
	}
	open := b.window != nil
	bonding := b.bonding > 0
	b.mu.Unlock()

	if !agentUp {
		if err := be.RegisterAgent(ctx, b); err != nil {
			b.log.Warn("bluetooth_agent_failed")
		} else {
			b.mu.Lock()
			b.agentUp = true
			b.mu.Unlock()
			b.log.Info("bluetooth_agent_registered")
		}
	}
	if !applied {
		ok := true
		if a.Powered != cfg.Enabled {
			ok = be.SetPowered(ctx, cfg.Enabled) == nil && ok
			b.log.Info("bluetooth_powered", "on", cfg.Enabled)
		}
		if cfg.Enabled && a.Alias != alias {
			ok = be.SetAlias(ctx, alias) == nil && ok
		}
		if !ok {
			b.log.Warn("bluetooth_apply_failed")
		}
		if ok {
			b.mu.Lock()
			// a config or name that came meanwhile is applied at the next reconcile
			b.appliedGen = gen
			b.mu.Unlock()
		}
	}
	// an outgoing pair holds pairable on (bondable) until it ends, and locks it itself then
	if !open && (a.Pairable && !bonding || a.Discoverable || reason != "") {
		b.lock(ctx, be)
	}
}

// lock: pairable and discoverable off; an unpowered adapter refuses discoverable, and nobody sees it anyway
func (b *Bluetooth) lock(ctx context.Context, be Backend) {
	b.adapterMu.Lock()
	defer b.adapterMu.Unlock()
	b.lockHeld(ctx, be)
}

// lockHeld is lock for callers that hold adapterMu: a window opened meanwhile stays open, and pairable stays on
// while an outgoing pair needs it
func (b *Bluetooth) lockHeld(ctx context.Context, be Backend) {
	b.mu.Lock()
	open, bonding := b.window != nil, b.bonding > 0
	b.mu.Unlock()
	if open {
		return
	}
	var err error
	if !bonding {
		err = be.SetPairable(ctx, false)
	}
	_ = be.SetDiscoverable(ctx, false)
	if err != nil {
		b.log.Warn("bluetooth_lock_failed")
		return
	}
	b.log.Info("bluetooth_locked")
}

// trust: a device our agent let pair in a window is trusted once bluez says it is paired
func (b *Bluetooth) trust(ctx context.Context, be Backend, snap Snapshot) {
	b.mu.Lock()
	var todo []Device
	for _, d := range snap.Devices {
		if b.approved[d.Path] && d.Paired {
			delete(b.approved, d.Path)
			if !d.Trusted {
				todo = append(todo, d)
			}
		}
	}
	b.mu.Unlock()
	for _, d := range todo {
		id := ShortID(snap.Adapter.Address, d.Address)
		if err := be.SetTrusted(ctx, d.Path, true); err != nil {
			b.log.Warn("bluetooth_trust_failed", "id", id)
			continue
		}
		b.log.Info("bluetooth_paired", "id", id, "kind", kind(d.Icon, d.Class))
	}
}

// players mirror each open stream's phone player to audiod, on change
func (b *Bluetooth) players(snap Snapshot) {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	b.mu.Lock()
	send := b.send
	var out []*audiodv1.Envelope
	for _, st := range b.streams {
		p := playerFor(snap, st)
		if st.player != nil && proto.Equal(st.player, p) {
			continue
		}
		st.player = p
		out = append(out, &audiodv1.Envelope{Body: &audiodv1.Envelope_BluetoothPlayer{BluetoothPlayer: p}})
	}
	b.mu.Unlock()
	if send == nil {
		return
	}
	for _, e := range out {
		_ = send(e)
	}
}

// playerFor is the phone's player as audiod takes it; with none, the stream plays while its pcm flows
func playerFor(snap Snapshot, st *stream) *audiodv1.BluetoothPlayer {
	i := slices.IndexFunc(snap.Players, func(p Player) bool { return p.Device == st.device })
	if i < 0 {
		return &audiodv1.BluetoothPlayer{StreamId: st.id, Playing: true}
	}
	p := snap.Players[i]
	out := &audiodv1.BluetoothPlayer{StreamId: st.id, Playing: p.Status == "playing",
		DurationMs: uint64(p.DurationMs), PositionMs: uint64(p.PositionMs)}
	if p.Title != "" || p.Artist != "" || p.Album != "" {
		out.Metadata = &audiodv1.MediaMetadata{Title: p.Title, Artist: p.Artist, Album: p.Album}
	}
	return out
}

func deviceAt(snap Snapshot, path string) (Device, bool) {
	i := slices.IndexFunc(snap.Devices, func(d Device) bool { return d.Path == path })
	if i < 0 {
		return Device{}, false
	}
	return snap.Devices[i], true
}
