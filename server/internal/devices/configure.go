package devices

import (
	"context"
	"encoding/json"
	"errors"
	"maps"

	"github.com/jackc/pgx/v5"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// Configure makes cfg the capability's desired config and sets each of secrets (every name declared, every value
// non-empty), in one transaction under one new generation, then pushes them together when the device is online: the
// device never sees the config without its secrets (network.wifi's ssid without its psk). A generation is added even
// when nothing differs, so the same config and secret pushed again reach the device as a new attempt (a wi-fi switch
// retried after a rollback). Secrets it does not name stay as they are.
func (s *Service) Configure(ctx context.Context, deviceID, capability string, cfg json.RawMessage, secrets map[string]string) (int64, error) {
	id, ok := parseID(deviceID)
	if !ok {
		return 0, ErrUnknownCapability
	}
	cctx, cancel := s.linkCtx(ctx)
	row, err := s.q.GetCapability(cctx, db.GetCapabilityParams{DeviceID: id, Capability: capability})
	cancel()
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrUnknownCapability
	}
	if err != nil {
		return 0, err
	}
	if err := checkConfig(row.Descriptor, cfg); err != nil {
		return 0, err
	}
	for name, value := range secrets {
		if !declares(row.Descriptor, name) {
			return 0, ErrUnknownSecret
		}
		if n := len(value); n == 0 || n > maxSecretBytes {
			return 0, ErrSecretInvalid
		}
	}
	if len(secrets) > 0 && s.box == nil {
		return 0, errNoBox
	}
	dev := id.String()
	defer s.lockPush(dev)()
	have, err := s.loadSecrets(ctx, id, capability, "")
	if err != nil {
		return 0, err
	}
	next := maps.Clone(have)
	if next == nil {
		next = map[string]string{}
	}
	maps.Copy(next, secrets)
	if ok, err := fits(capability, cfg, next); err != nil {
		return 0, err
	} else if !ok {
		return 0, ErrConfigTooLarge
	}
	sealed := make(map[string][]byte, len(secrets))
	for name, value := range secrets {
		b, err := s.box.Seal([]byte(value), secretAAD(dev, capability, name))
		if err != nil {
			return 0, err
		}
		sealed[name] = b
	}
	var gen int64
	cctx, cancel = s.linkCtx(ctx)
	err = pgx.BeginFunc(cctx, s.store, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		for name, b := range sealed {
			if err := q.UpsertCapabilitySecret(cctx, db.UpsertCapabilitySecretParams{
				DeviceID: id, Capability: capability, Name: name, Sealed: b,
			}); err != nil {
				return err
			}
		}
		var err error
		gen, err = q.SetDesiredConfig(cctx, db.SetDesiredConfigParams{DeviceID: id, Capability: capability, Desired: cfg})
		return err
	})
	cancel()
	if err != nil {
		return 0, err
	}
	for name := range sealed {
		s.log.Info("capability_secret", "device_id", dev, "capability", capability, "name", name, "action", "set")
	}
	if s.hub.Online(dev) {
		s.push(ctx, dev, id, capability, gen, cfg)
	}
	return gen, nil
}
