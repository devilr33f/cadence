package devices

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/jackc/pgx/v5"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// Ensure makes cfg the capability's desired config and value its secret name ("" removes it), as one generation
// pushed when the device is online, and only when either differs from what is stored: a config the server computes
// (a multiroom group's) is set again on every recompute, and an unchanged one must add no generation. changed says
// whether anything was stored. The config is validated as SetConfig validates it.
func (s *Service) Ensure(ctx context.Context, deviceID, capability string, cfg json.RawMessage, name, value string) (bool, error) {
	id, ok := parseID(deviceID)
	if !ok {
		return false, ErrUnknownCapability
	}
	if len(value) > maxSecretBytes {
		return false, ErrSecretInvalid
	}
	dev := id.String()
	defer s.lockPush(dev)()
	cctx, cancel := s.linkCtx(ctx)
	row, err := s.q.GetCapability(cctx, db.GetCapabilityParams{DeviceID: id, Capability: capability})
	cancel()
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrUnknownCapability
	}
	if err != nil {
		return false, err
	}
	if err := checkConfig(row.Descriptor, cfg); err != nil {
		return false, err
	}
	if !declares(row.Descriptor, name) {
		return false, ErrUnknownSecret
	}
	have, err := s.loadSecrets(ctx, id, capability, "")
	if err != nil {
		return false, err
	}
	cur, had := have[name]
	sameSecret := (value == "" && !had) || (had && subtle.ConstantTimeCompare([]byte(cur), []byte(value)) == 1)
	if sameSecret && sameJSON(row.Desired, cfg) {
		return false, nil
	}
	next := map[string]string{}
	for k, v := range have {
		next[k] = v
	}
	delete(next, name)
	if value != "" {
		next[name] = value
	}
	if ok, err := fits(capability, cfg, next); err != nil {
		return false, err
	} else if !ok {
		return false, ErrConfigTooLarge
	}
	if !sameSecret {
		if err := s.storeSecret(ctx, dev, capability, name, value); err != nil {
			return false, err
		}
	}
	cctx, cancel = s.linkCtx(ctx)
	gen, err := s.q.SetDesiredConfig(cctx, db.SetDesiredConfigParams{DeviceID: id, Capability: capability, Desired: cfg})
	cancel()
	if err != nil {
		return false, err
	}
	if s.hub.Online(dev) {
		s.push(ctx, dev, id, capability, gen, cfg)
	}
	return true, nil
}

// storeSecret seals and stores value under name, or deletes it for ""
func (s *Service) storeSecret(ctx context.Context, dev, capability, name, value string) error {
	id, _ := parseID(dev)
	cctx, cancel := s.linkCtx(ctx)
	defer cancel()
	action := "delete"
	if value == "" {
		if err := s.q.DeleteCapabilitySecret(cctx, db.DeleteCapabilitySecretParams{DeviceID: id, Capability: capability, Name: name}); err != nil {
			return err
		}
	} else {
		if s.box == nil {
			return errNoBox
		}
		sealed, err := s.box.Seal([]byte(value), secretAAD(dev, capability, name))
		if err != nil {
			return err
		}
		if err := s.q.UpsertCapabilitySecret(cctx, db.UpsertCapabilitySecretParams{
			DeviceID: id, Capability: capability, Name: name, Sealed: sealed,
		}); err != nil {
			return err
		}
		action = "set"
	}
	s.log.Info("capability_secret", "device_id", dev, "capability", capability, "name", name, "action", action)
	return nil
}

// sameJSON compares two documents by value: jsonb renders its own spacing and key order
func sameJSON(a, b []byte) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}
