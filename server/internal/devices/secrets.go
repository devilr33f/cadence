package devices

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/encoding/protojson"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

const maxSecretBytes = 4096

var (
	ErrUnknownSecret = errors.New("unknown secret")
	ErrSecretInvalid = errors.New("secret invalid")
	errNoBox         = errors.New("devices: no secrets box")
	errBadID         = errors.New("devices: bad device id")
)

// the aad binds a sealed value to its device, capability and name
func secretAAD(deviceID, capability, name string) []byte {
	return []byte("device-secret:" + deviceID + "\x00" + capability + "\x00" + name)
}

// loadSecrets opens the stored secrets the capability's current descriptor
// declares, but not skip's: rows of names it does not declare (a device rolled
// back to an older build) stay inert, never opened and never sent.
func (s *Service) loadSecrets(ctx context.Context, id pgtype.UUID, capability, skip string) (map[string]string, error) {
	cctx, cancel := s.linkCtx(ctx)
	rows, err := s.q.ListCapabilitySecrets(cctx, db.ListCapabilitySecretsParams{DeviceID: id, Capability: capability})
	cancel()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	cctx, cancel = s.linkCtx(ctx)
	row, err := s.q.GetCapability(cctx, db.GetCapabilityParams{DeviceID: id, Capability: capability})
	cancel()
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if s.box == nil {
		return nil, errNoBox
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		if r.Name == skip || !declares(row.Descriptor, r.Name) {
			continue
		}
		plain, err := s.box.Open(r.Sealed, secretAAD(id.String(), capability, r.Name))
		if err != nil {
			return nil, err
		}
		out[r.Name] = string(plain)
	}
	return out, nil
}

// fits reports whether a ConfigSet for the capability would fit in one frame.
func fits(capability string, cfg []byte, secrets map[string]string) (bool, error) {
	var compact bytes.Buffer
	if len(bytes.TrimSpace(cfg)) == 0 {
		cfg = []byte("{}")
	}
	if err := json.Compact(&compact, cfg); err != nil {
		return false, err
	}
	probe := &cadencev2.Envelope{Body: &cadencev2.Envelope_ConfigSet{
		ConfigSet: &cadencev2.ConfigSet{Capability: capability, Generation: math.MaxInt64, Json: compact.String(), Secrets: secrets},
	}}
	b, err := wire.Encode(probe)
	if errors.Is(err, wire.ErrFrameTooLarge) || err == nil && len(b) > MaxConfigFrameBytes {
		return false, nil
	}
	return err == nil, err
}

// MaxConfigFrameBytes bounds a capability's config with its secrets as one ConfigSet: it reaches every device,
// the speakers too, and a config never needs more; the link itself takes far larger frames (wire.MaxFrameBytes)
const MaxConfigFrameBytes = 16384

func declares(descriptor []byte, name string) bool {
	var d cadencev2.Descriptor
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(descriptor, &d); err != nil {
		return false
	}
	for _, sp := range d.GetSecrets() {
		if sp.GetName() == name {
			return true
		}
	}
	return false
}

// secretTarget checks the capability and the declared name, returning the
// device's canonical id.
func (s *Service) secretTarget(ctx context.Context, deviceID, capability, name string) (pgtype.UUID, error) {
	id, ok := parseID(deviceID)
	if !ok {
		return id, ErrUnknownCapability
	}
	cctx, cancel := s.linkCtx(ctx)
	row, err := s.q.GetCapability(cctx, db.GetCapabilityParams{DeviceID: id, Capability: capability})
	cancel()
	if errors.Is(err, pgx.ErrNoRows) {
		return id, ErrUnknownCapability
	}
	if err != nil {
		return id, err
	}
	if !declares(row.Descriptor, name) {
		return id, ErrUnknownSecret
	}
	return id, nil
}

// SetSecret seals the value, stores it and pushes the capability's config with
// all its secrets under a new generation.
func (s *Service) SetSecret(ctx context.Context, deviceID, capability, name, value string) error {
	id, err := s.secretTarget(ctx, deviceID, capability, name)
	if err != nil {
		return err
	}
	if n := len(value); n == 0 || n > maxSecretBytes {
		return ErrSecretInvalid
	}
	if s.box == nil {
		return errNoBox
	}
	dev := id.String()
	defer s.lockPush(dev)()
	cctx, cancel := s.linkCtx(ctx)
	row, err := s.q.GetCapability(cctx, db.GetCapabilityParams{DeviceID: id, Capability: capability})
	cancel()
	if err != nil {
		return err
	}
	have, err := s.loadSecrets(ctx, id, capability, name)
	if err != nil {
		return err
	}
	next := map[string]string{name: value}
	for k, v := range have {
		next[k] = v
	}
	if ok, err := fits(capability, row.Desired, next); err != nil {
		return err
	} else if !ok {
		return ErrConfigTooLarge
	}
	sealed, err := s.box.Seal([]byte(value), secretAAD(dev, capability, name))
	if err != nil {
		return err
	}
	cctx, cancel = s.linkCtx(ctx)
	err = s.q.UpsertCapabilitySecret(cctx, db.UpsertCapabilitySecretParams{
		DeviceID: id, Capability: capability, Name: name, Sealed: sealed,
	})
	cancel()
	if err != nil {
		return err
	}
	s.log.Info("capability_secret", "device_id", dev, "capability", capability, "name", name, "action", "set")
	return s.bumpAndPush(ctx, dev, id, capability)
}

// DeleteSecret removes the stored value and pushes the config without it.
func (s *Service) DeleteSecret(ctx context.Context, deviceID, capability, name string) error {
	id, err := s.secretTarget(ctx, deviceID, capability, name)
	if err != nil {
		return err
	}
	dev := id.String()
	defer s.lockPush(dev)()
	// bump first: a failed delete then still leaves a push pending, and the delete can be retried
	row, err := s.bump(ctx, id, capability)
	if err != nil {
		return err
	}
	cctx, cancel := s.linkCtx(ctx)
	err = s.q.DeleteCapabilitySecret(cctx, db.DeleteCapabilitySecretParams{DeviceID: id, Capability: capability, Name: name})
	cancel()
	if err != nil {
		return err
	}
	s.log.Info("capability_secret", "device_id", dev, "capability", capability, "name", name, "action", "delete")
	s.pushIfOnline(ctx, dev, id, capability, row)
	return nil
}

// bumpAndPush runs under the push lock: the capability's desired config, or
// {}, goes out again under the next generation when the device is online.
func (s *Service) bumpAndPush(ctx context.Context, dev string, id pgtype.UUID, capability string) error {
	row, err := s.bump(ctx, id, capability)
	if err != nil {
		return err
	}
	s.pushIfOnline(ctx, dev, id, capability, row)
	return nil
}

func (s *Service) bump(ctx context.Context, id pgtype.UUID, capability string) (db.BumpDesiredGenRow, error) {
	cctx, cancel := s.linkCtx(ctx)
	defer cancel()
	return s.q.BumpDesiredGen(cctx, db.BumpDesiredGenParams{DeviceID: id, Capability: capability})
}

func (s *Service) pushIfOnline(ctx context.Context, dev string, id pgtype.UUID, capability string, row db.BumpDesiredGenRow) {
	if s.hub.Online(dev) {
		s.push(ctx, dev, id, capability, row.DesiredGen, row.Desired)
	}
}

// ScrubSecrets opens every value stored for the device, named
// <capability>.<name> as the device names them for its log scrub, rows its
// descriptor no longer declares included: the server scrubs the device's logs
// with them again. A value that does not open is an error, never skipped.
func (s *Service) ScrubSecrets(ctx context.Context, deviceID string) (map[string]string, error) {
	id, ok := parseID(deviceID)
	if !ok {
		return nil, errBadID
	}
	rows, err := s.q.ListDeviceSecrets(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(rows) > 0 && s.box == nil {
		return nil, errNoBox
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		plain, err := s.box.Open(r.Sealed, secretAAD(id.String(), r.Capability, r.Name))
		if err != nil {
			return nil, err
		}
		out[r.Capability+"."+r.Name] = string(plain)
	}
	return out, nil
}
