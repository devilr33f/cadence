// Package group is cadenced's side of multiroom playback: the media.group capability. The server computes each
// device's membership and pushes it with the group key as its secret; this package hands it to audiod as
// GroupConfig, again after every audiod start, and publishes audiod's GroupStatus as the capability's state. The key
// and the leader's address never reach a log, an error or a state (rules 6 and 8).
package group

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// Group keeps media.group's config and key for audiod, which forgets them with its client, and the status audiod
// reports.
type Group struct {
	log   *slog.Logger
	cap   *provider
	state chan json.RawMessage

	// held while a config goes out, so audiod gets them in order
	sendMu sync.Mutex

	mu      sync.Mutex
	member  string
	cfg     capschema.MediaGroup
	output  capschema.MediaGroupOutput
	key     []byte
	send    func(*audiodv1.Envelope) error // the live connection's, nil while audiod is down
	sent    *audiodv1.GroupConfig          // what the live connection last got
	status  *audiodv1.GroupStatus
	tracked *audiodv1.GroupTracked
	shown   []byte
	// the state and error last logged
	shownState, shownError string
	shownLocal             bool
	// the media session is a source of the device's own (LocalSource), or a direct route playing
	local, direct bool
	// the calibrate commands waiting for audiod's answer, by request id
	calibrations map[string]chan *audiodv1.GroupCalibrated
}

// New starts out of any group. A nil log discards.
func New(log *slog.Logger) *Group {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	g := &Group{log: log, state: make(chan json.RawMessage, 1)}
	g.cap = &provider{g: g}
	g.mu.Lock()
	g.publishLocked()
	g.mu.Unlock()
	return g
}

// Capability is media.group.
func (g *Group) Capability() capability.Provider { return g.cap }

// SetDeviceID is the device's enrolled id, the member id audiod names itself by in the group's stream.
func (g *Group) SetDeviceID(id string) {
	g.sendMu.Lock()
	defer g.sendMu.Unlock()
	g.mu.Lock()
	g.member = id
	g.mu.Unlock()
	g.sendIfChanged()
}

// GroupReady sends a new audiod connection the config and uses the connection.
func (g *Group) GroupReady(send func(*audiodv1.Envelope) error) {
	g.sendMu.Lock()
	defer g.sendMu.Unlock()
	g.mu.Lock()
	g.send, g.sent, g.status = send, nil, nil
	g.tracked = nil
	g.publishLocked()
	g.mu.Unlock()
	g.sendIfChanged()
}

// GroupEnvelope takes a GroupStatus or a GroupCalibrated from audiod; it never blocks.
func (g *Group) GroupEnvelope(env *audiodv1.Envelope) {
	if c := env.GetGroupCalibrated(); c != nil {
		g.calibrated(c)
		return
	}
	if tracked := env.GetGroupTracked(); tracked != nil {
		g.mu.Lock()
		g.tracked = tracked
		g.publishLocked()
		g.mu.Unlock()
		return
	}
	st, ok := env.GetBody().(*audiodv1.Envelope_GroupStatus)
	if !ok {
		return
	}
	g.mu.Lock()
	g.status = st.GroupStatus
	g.publishLocked()
	g.mu.Unlock()
}

// GroupGone follows the end of the connection GroupReady took.
func (g *Group) GroupGone() {
	g.mu.Lock()
	g.send, g.sent, g.status = nil, nil, nil
	g.tracked = nil
	g.publishLocked()
	g.mu.Unlock()
}

func (g *Group) configure(cfg capschema.MediaGroup, key []byte) {
	g.sendMu.Lock()
	defer g.sendMu.Unlock()
	g.mu.Lock()
	if cfg.GroupID != g.cfg.GroupID || cfg.Role != g.cfg.Role || cfg.Enabled != g.cfg.Enabled {
		// a status from before is another membership's
		g.status = nil
	}
	g.cfg, g.key = cfg, key
	g.tracked = nil
	g.publishLocked()
	g.mu.Unlock()
	g.sendIfChanged()
}

// sendIfChanged sends the live connection the config when it differs from what it last got. callers hold sendMu
func (g *Group) sendIfChanged() {
	g.mu.Lock()
	send := g.send
	c := groupConfig(g.cfg, g.member, g.key, g.output)
	if send == nil || g.sent != nil && proto.Equal(g.sent, c) {
		g.mu.Unlock()
		return
	}
	g.sent = c
	g.mu.Unlock()
	if send(&audiodv1.Envelope{Body: &audiodv1.Envelope_GroupConfig{GroupConfig: c}}) != nil {
		g.log.Warn("group_config_unsent")
		return
	}
	g.log.Info("group_config_sent", "enabled", c.GetEnabled(), "group", c.GetGroupId(), "role", roleName(c.GetRole()),
		"members", len(c.GetMembers()))
}

// callers hold mu
func (g *Group) publishLocked() {
	st := g.stateLocked()
	b, err := json.Marshal(st)
	if err != nil || bytes.Equal(b, g.shown) {
		return
	}
	if st.State != g.shownState || st.Error != g.shownError || st.LocalSource != g.shownLocal {
		g.log.Info("group_state", "state", st.State, "code", st.Error, "group", st.GroupID, "role", st.Role,
			"local_source", st.LocalSource)
	}
	g.shown, g.shownState, g.shownError, g.shownLocal = b, st.State, st.Error, st.LocalSource
	offer(g.state, json.RawMessage(b))
}

// stateLocked is off while the device is in no group, connecting until audiod reports the membership, then audiod's
// own; a member's localSource with either. callers hold mu
func (g *Group) stateLocked() capschema.MediaGroupStatus {
	var out capschema.MediaGroupStatus
	switch {
	case !g.cfg.Enabled:
		out = capschema.MediaGroupStatus{State: stateOff}
	case g.status == nil || g.status.GetGroupId() != g.cfg.GroupID:
		out = capschema.MediaGroupStatus{GroupID: g.cfg.GroupID, Role: g.cfg.Role, State: stateConnecting, LocalSource: g.local}
	default:
		out = fromStatus(g.status)
		out.LocalSource = g.local
	}
	out.Output = cloneOutput(g.output)
	out.Tracking = trackingState(g.tracked, g.cfg)
	return out
}

// provider is media.group: the device's membership, with the group key as its one secret.
type provider struct{ g *Group }

func (*provider) Descriptor() capability.Descriptor {
	return capability.Descriptor{
		ID: capschema.MediaGroupID, Version: 1, ConfigSchema: capschema.MediaGroupConfig, StateSchema: capschema.MediaGroupState,
		Secrets:  []capability.SecretSpec{{Name: capschema.MediaGroupKeySecret, Description: "multiroom group key, set by the server"}},
		Commands: map[string]string{capschema.GroupCalibrateCommand: capschema.GroupCalibrateArgs},
	}
}

func (p *provider) Apply(ctx context.Context, gen int64, cfg json.RawMessage) error {
	return p.ApplySecrets(ctx, gen, cfg, nil)
}

// ApplySecrets takes the membership and the key that came with it; a config or a key it cannot run is refused, and
// the one in force stays.
func (p *provider) ApplySecrets(_ context.Context, _ int64, raw json.RawMessage, secrets map[string]string) error {
	cfg, ok := parseConfig(raw)
	if !ok {
		return &capability.ApplyError{Code: codeInvalid}
	}
	var key []byte
	if cfg.Enabled {
		var problem string
		if key, problem = parseKey(secrets); problem != "" {
			return &capability.ApplyError{Code: problem}
		}
	}
	p.g.configure(cfg, key)
	return nil
}

func (p *provider) Command(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if name != capschema.GroupCalibrateCommand {
		return nil, capability.ErrUnsupported
	}
	return p.g.calibrate(ctx, args)
}

// CommandWait makes the provider a capability.CommandWaiter: a calibration runs past core's usual wait.
func (p *provider) CommandWait(name string) time.Duration {
	if name == capschema.GroupCalibrateCommand {
		return calibrateWait
	}
	return 0
}

func (p *provider) State() <-chan json.RawMessage { return p.g.state }

// SetDeviceID makes the provider a capability.DeviceIdentified: core names the device before any config applies.
func (p *provider) SetDeviceID(id string) { p.g.SetDeviceID(id) }

// offer puts v in a channel of one that keeps the latest value; it never blocks.
func offer[T any](ch chan T, v T) {
	for {
		select {
		case ch <- v:
			return
		default:
		}
		select {
		case <-ch:
		default:
		}
	}
}
