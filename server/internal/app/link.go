package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/enroll"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
	"git.shino.boo/devilreef/cadence/server/internal/media"
)

// linkDeps are what a device link reaches; caps and voices are interfaces so
// the wiring can be tested without a database or providers.
type linkDeps struct {
	caps interface {
		Hello(ctx context.Context, deviceID string, h *cadencev2.Hello) *cadencev2.Welcome
		Handle(ctx context.Context, deviceID string, env *cadencev2.Envelope) bool
		Closed(deviceID string)
	}
	cmds     *commands.Service
	sessions *media.Store
	voices   interface {
		Linked(ctx context.Context, c gateway.Conn)
		Handle(ctx context.Context, c gateway.Conn, env *cadencev2.Envelope) bool
		Disconnected(deviceID string)
	}
	// seen records the link and answers the device's name and model
	seen func(ctx context.Context, deviceID string) (name, model string, err error)
	// zones, when set, keeps each linked device's alarms zone, from when its capabilities are known
	zones interface {
		Linked(deviceID string)
		SyncLater(ctx context.Context, deviceID string)
		Closed(deviceID string)
	}
	// connected, when set, hears that a device linked
	connected func(deviceID string)
	// presence, when set, publishes each link's open and close (M8-D2), from
	// the name and model the last-seen write read before the hub took the link
	presence interface {
		Remember(deviceID, name, model string)
		Linked(deviceID string)
		Unlinked(deviceID string)
	}
	// screens, when set, keeps screen contexts, runs rpc calls and watches
	// the voice service's frames for the room fan-out (stream N)
	screens *screenParts
	// info, when set, is the device's name and room for its welcome
	info func(ctx context.Context, deviceID string) *cadencev2.DeviceInfo
	// groups, when set, keeps each linked device's lan addresses and recomputes the multiroom configs as links open,
	// reports arrive and links close
	groups interface {
		Linked(deviceID string, addresses []string)
		Poke()
		Closed(deviceID string)
	}
	// ops, when set, hears each hello's daemon version (a run's success is the relink that names
	// its target) and the daemon's own update-run frames
	ops interface {
		NoteHello(ctx context.Context, deviceID string, protocol uint32, version string)
		Handle(deviceID string, env *cadencev2.Envelope) bool
	}
	version string
	log     *slog.Logger
}

// voiceConn is c as the voice service gets it: its frames shown to the room
// fan-out
func (d linkDeps) voiceConn(c gateway.Conn) gateway.Conn {
	if d.screens == nil {
		return c
	}
	return tapped{Conn: c, tap: d.screens.rooms.Outbound}
}

// deviceLink routes each device link's messages to the services.
func deviceLink(d linkDeps) gateway.Handlers {
	return gateway.Handlers{
		OnHello: func(ctx context.Context, c gateway.Conn, h *cadencev2.Hello) *cadencev2.Welcome {
			d.cmds.Linked(ctx, c)
			d.voices.Linked(ctx, d.voiceConn(c))
			// a replaced link gets no OnClose: a device back without a session must not show the old one
			d.sessions.Closed(c.DeviceID())
			w := d.caps.Hello(ctx, c.DeviceID(), h)
			w.ServerVersion = d.version
			if d.info != nil {
				w.DeviceInfo = d.info(ctx, c.DeviceID())
			}
			if d.zones != nil {
				d.zones.Linked(c.DeviceID())
				// a device asked for its full report has its capabilities known once the report is in
				if !w.SendFullReport {
					d.zones.SyncLater(ctx, c.DeviceID())
				}
			}
			if d.connected != nil {
				d.connected(c.DeviceID())
			}
			if d.presence != nil {
				d.presence.Linked(c.DeviceID())
			}
			if d.screens != nil {
				// a screen of a room whose turn runs joins it
				d.screens.rooms.Linked(c.DeviceID())
			}
			if d.groups != nil {
				d.groups.Linked(c.DeviceID(), h.GetLanAddresses())
			}
			if d.ops != nil {
				d.ops.NoteHello(ctx, c.DeviceID(), h.GetProtocol(), h.GetDaemonVersion())
			}
			return w
		},
		OnMessage: func(ctx context.Context, c gateway.Conn, env *cadencev2.Envelope) {
			if d.ops != nil && d.ops.Handle(c.DeviceID(), env) {
				return
			}
			if d.screens != nil && (d.screens.rooms.Inbound(c.DeviceID(), env) || d.screens.router.Handle(c.DeviceID(), env)) {
				return
			}
			if d.caps.Handle(ctx, c.DeviceID(), env) {
				if d.zones != nil && wholeReport(env.GetCapabilityReport()) {
					d.zones.SyncLater(ctx, c.DeviceID())
				}
				// a new report may add or drop media.group
				if d.groups != nil && wholeReport(env.GetCapabilityReport()) {
					d.groups.Poke()
				}
				return
			}
			if d.cmds.Handle(c, env) || d.sessions.Handle(c.DeviceID(), env) {
				return
			}
			d.voices.Handle(ctx, d.voiceConn(c), env)
		},
		OnClose: func(id string) {
			if d.zones != nil {
				d.zones.Closed(id)
			}
			d.caps.Closed(id)
			d.cmds.Closed(id)
			d.sessions.Closed(id)
			d.voices.Disconnected(id)
			if d.screens != nil {
				d.screens.router.Closed(id)
				d.screens.rooms.Closed(id)
				// the link dropped: its leases end before its cards go; it acquires again once back
				d.screens.leases.ReleaseAll(id)
				d.screens.cards.Forget(id)
			}
			if d.presence != nil {
				d.presence.Unlinked(id)
			}
			if d.groups != nil {
				d.groups.Closed(id)
			}
		},
		Seen: func(ctx context.Context, id string) {
			ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			name, model, err := d.seen(ctx, id)
			switch {
			case errors.Is(err, enroll.ErrUnknownDevice):
				// deleted meanwhile: the hub refuses its link
			case err != nil:
				d.log.Error("last_seen_failed", "device_id", id)
			case d.presence != nil:
				d.presence.Remember(id, name, model)
			}
		},
	}
}

// wholeReport reports a capability report's single frame or its last part: the one that stores it
func wholeReport(r *cadencev2.CapabilityReport) bool {
	return r != nil && (r.GetParts() <= 1 || r.GetPart() == r.GetParts()-1)
}
