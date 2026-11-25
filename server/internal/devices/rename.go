package devices

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"git.shino.boo/devilreef/cadence/server/internal/devicename"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

var (
	ErrNameInvalid = errors.New("device name invalid")
	ErrNameTaken   = errors.New("device name taken")
	ErrRoomInvalid = errors.New("device room invalid")
)

// Rename gives the device name as devicename.Clean has it, if no other device
// holds it in any case; its own name, in any case, is no clash. Telemetry
// stamps the new name from then on, and the live feed hears device_renamed.
func (s *Service) Rename(ctx context.Context, deviceID, name string) error {
	id, ok := parseID(deviceID)
	if !ok {
		return ErrNotFound
	}
	name, ok = devicename.Clean(name)
	if !ok {
		return ErrNameInvalid
	}
	model, err := s.q.RenameDevice(ctx, db.RenameDeviceParams{ID: id, Name: name})
	switch {
	case devicename.Taken(err):
		return ErrNameTaken
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return err
	}
	s.notify()
	s.renamed(id.String(), name, model)
	return nil
}

// SetRoom puts the device in a room, named as devicename.CleanRoom has it; "" is none. A speaker and a screen of one
// room are one assistant from the next turn on. The live feed hears device_renamed, so lists read the room again.
func (s *Service) SetRoom(ctx context.Context, deviceID, room string) error {
	id, ok := parseID(deviceID)
	if !ok {
		return ErrNotFound
	}
	room, ok = devicename.CleanRoom(room)
	if !ok {
		return ErrRoomInvalid
	}
	n, err := s.q.SetDeviceRoom(ctx, db.SetDeviceRoomParams{ID: id, Room: room})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	d, err := s.q.DeviceByID(ctx, id)
	if err != nil {
		return nil
	}
	s.notify()
	s.renamed(id.String(), d.Name, d.Model)
	return nil
}
