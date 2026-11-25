package core

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"

	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/config"
	"git.shino.boo/devilreef/cadence/device/internal/discovery"
	"git.shino.boo/devilreef/cadence/device/internal/link"
	"git.shino.boo/devilreef/cadence/device/internal/syncstore"
)

// Run runs the board; an enrolled one syncs with the server, its Sync composed with opts.
func Run(ctx context.Context, cfg config.Config, boards []board.Board, log *slog.Logger, version string, opts ...SyncOption) error {
	b, err := board.Find(boards, cfg.Board)
	if err != nil {
		log.Error("board_refused", "board", cfg.Board, "reason", "unknown")
		return err
	}
	if err := b.Fingerprint(ctx); err != nil {
		log.Error("board_refused", "board", cfg.Board, "reason", "fingerprint")
		if errors.Is(err, board.ErrFingerprint) {
			return err
		}
		return errors.Join(board.ErrFingerprint, err)
	}
	if c, ok := b.(io.Closer); ok {
		defer func() { _ = c.Close() }()
	}
	log.Info("board_ready", "board", b.ID(), "version", version)
	if cfg.ServerURL != "" {
		if err := runLinked(ctx, cfg, b, log, version, opts); err != nil {
			return err
		}
	}
	<-ctx.Done()
	log.Info("shutdown")
	return nil
}

// runLinked syncs the board with the server until ctx is done, drawing on its
// lights and acting on its input from boot. A board with duplicate capability
// ids, or a descriptor too large for any report frame, stays offline: the
// server would refuse its report or never get it whole.
func runLinked(ctx context.Context, cfg config.Config, b board.Board, log *slog.Logger, version string, opts []SyncOption) error {
	l := startLights(ctx, b, cfg.DataDir, log)
	defer l.stop()
	// the device is advertised on its network from boot, with or without a link
	advertise := discovery.Advertise
	if a, ok := b.(board.Advertiser); ok {
		advertise = a.Advertise
	}
	d := newDiscovery(ctx, discovery.Info{DeviceID: cfg.DeviceID, Board: b.ID(), Version: version},
		discovery.SettingsPath(cfg.DataDir), log, advertise)
	defer d.Stop()
	if id, ok := duplicateID(slices.Concat(b.Capabilities(), l.capabilities(), []capability.Provider{d, &sessionCapability{}})); ok {
		log.Error("capability_duplicate", "capability", id)
		return nil
	}
	roots, err := link.RootCAs(cfg.CAFile)
	if err != nil {
		log.Error("link_refused", "reason", "ca_file")
		return err
	}
	store, err := syncstore.Open(syncstore.Dir(cfg.DataDir))
	if err != nil {
		// the cause quotes the data dir
		log.Error("link_refused", "reason", "store")
		return errors.New("link: cannot open the config store")
	}
	s, err := NewSync(b, store, version, log, slices.Concat(opts, []SyncOption{withLights(l), withCapabilities(d), withSessionSettings(), WithDeviceID(cfg.DeviceID),
		withRingSounds(cfg.ServerURL, cfg.DeviceToken)})...)
	var tooLarge *capability.TooLargeError
	if errors.As(err, &tooLarge) {
		log.Error("capability_too_large", "capability", tooLarge.ID)
		return nil
	}
	if err != nil {
		return err
	}
	s.Restore(ctx)
	actx, stop := context.WithCancel(ctx)
	wait := s.StartAudio(actx)
	defer func() { stop(); wait() }()
	if o, ok := b.(board.ServerOrigin); ok {
		o.SetServerURL(cfg.ServerURL)
	}
	dialer := link.Dialer{URL: cfg.ServerURL, Token: cfg.DeviceToken, RootCAs: roots, Redial: s.Redials()}
	if g, ok := b.(board.LinkGate); ok {
		dialer.Blocked = g.LinkBlocked
	}
	return link.Run(ctx, dialer, s, log)
}

// newDiscovery advertises the device on its network through advertise (cadenced's own responder, or the board's);
// core's tests advertise nothing
var newDiscovery = discovery.NewCapabilityWith

func duplicateID(ps []capability.Provider) (string, bool) {
	seen := make(map[string]bool, len(ps))
	for _, p := range ps {
		id := p.Descriptor().ID
		if seen[id] {
			return id, true
		}
		seen[id] = true
	}
	return "", false
}
