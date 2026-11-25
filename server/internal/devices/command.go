package devices

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"google.golang.org/protobuf/encoding/protojson"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

var ErrUnknownCommand = errors.New("unknown command")

// Canonical returns the device's id as the hub keys it, or ErrNotFound.
func (s *Service) Canonical(ctx context.Context, deviceID string) (string, error) {
	id, err := s.device(ctx, deviceID)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// device parses deviceID and checks that the device exists.
func (s *Service) device(ctx context.Context, deviceID string) (pgtype.UUID, error) {
	id, ok := parseID(deviceID)
	if !ok {
		return id, ErrNotFound
	}
	cctx, cancel := s.linkCtx(ctx)
	_, err := s.q.DeviceByID(cctx, id)
	cancel()
	if errors.Is(err, pgx.ErrNoRows) {
		return id, ErrNotFound
	}
	return id, err
}

// Command returns the device's id as the hub keys it and the args schema of a
// command its report lists.
func (s *Service) Command(ctx context.Context, deviceID, capability, name string) (string, *jsonschema.Schema, error) {
	id, err := s.device(ctx, deviceID)
	if err != nil {
		return "", nil, err
	}
	cctx, cancel := s.linkCtx(ctx)
	row, err := s.q.GetCapability(cctx, db.GetCapabilityParams{DeviceID: id, Capability: capability})
	cancel()
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrUnknownCapability
	}
	if err != nil {
		return "", nil, err
	}
	var d cadencev2.Descriptor
	if err := protojson.Unmarshal(row.Descriptor, &d); err != nil {
		return "", nil, err
	}
	src, ok := d.GetCommands()[name]
	if !ok {
		return "", nil, ErrUnknownCommand
	}
	sch, err := compileSchema(src)
	if err != nil {
		return "", nil, err
	}
	return id.String(), sch, nil
}
