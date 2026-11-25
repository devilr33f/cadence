// Package enroll creates pending devices and exchanges one-time codes for device tokens.
package enroll

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/devicename"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

const codeTTL = 30 * time.Minute

var (
	ErrUnknownModel = errors.New("unknown model")
	ErrRefused      = errors.New("enroll refused")
	ErrNameInvalid  = errors.New("device name invalid")
	ErrNameTaken    = errors.New("device name taken")
	// ErrRoomInvalid is a room name past devicename's rules
	ErrRoomInvalid = errors.New("room invalid")
	// ErrUnknownDevice is a device id no device has
	ErrUnknownDevice = errors.New("unknown device")
)

type Fingerprint struct{ Board string }

type Device struct {
	ID        string
	Name      string
	Model     string
	Status    string
	Room      string
	ExpiresAt time.Time
}

type Service struct {
	q   *db.Queries
	now func() time.Time
	bus events.Publisher
	// groupRefs, when set, names each device's multiroom group in the device list
	groupRefs devices.GroupRefs
}

// ShowGroups has the device list name each device's group; set it before the api serves.
func (s *Service) ShowGroups(refs devices.GroupRefs) { s.groupRefs = refs }

func NewService(q *db.Queries, now func() time.Time) *Service { return &Service{q: q, now: now} }

// PublishTo makes each completed enrolment a device_added event on bus.
func (s *Service) PublishTo(bus events.Publisher) { s.bus = bus }

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// CreateDevice returns the pending device and its code in display form. Its
// name follows a rename's rules (devicename): trimmed, and no other device's.
func (s *Service) CreateDevice(ctx context.Context, name, model string) (Device, string, error) {
	if !knownModel(model) {
		return Device{}, "", ErrUnknownModel
	}
	name, ok := devicename.Clean(name)
	if !ok {
		return Device{}, "", ErrNameInvalid
	}
	display, err := NewCode()
	if err != nil {
		return Device{}, "", err
	}
	norm, _ := NormalizeCode(display)
	row, err := s.q.CreateDevice(ctx, db.CreateDeviceParams{Name: name, Model: model})
	if devicename.Taken(err) {
		return Device{}, "", ErrNameTaken
	}
	if err != nil {
		return Device{}, "", err
	}
	exp := s.now().Add(codeTTL)
	err = s.q.CreateEnrollCode(ctx, db.CreateEnrollCodeParams{
		CodeHash: auth.Hash(norm), DeviceID: row.ID, Model: model, ExpiresAt: ts(exp),
	})
	if err != nil {
		return Device{}, "", err
	}
	return Device{ID: row.ID.String(), Name: row.Name, Model: row.Model, Status: row.Status, ExpiresAt: exp}, display, nil
}

// Enroll exchanges a code for a device token; any failed check is ErrRefused.
func (s *Service) Enroll(ctx context.Context, code string, fp Fingerprint) (string, string, error) {
	norm, ok := NormalizeCode(code)
	if !ok {
		return "", "", ErrRefused
	}
	token, hash, err := auth.NewToken(auth.DevicePrefix)
	if err != nil {
		return "", "", err
	}
	now := s.now()
	row, err := s.q.ClaimEnrollCode(ctx, db.ClaimEnrollCodeParams{
		Now: ts(now), CodeHash: auth.Hash(norm), Model: fp.Board, TokenHash: hash,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrRefused
	}
	if err != nil {
		return "", "", err
	}
	id := row.ID.String()
	if s.bus != nil {
		// a token made just now has opened no link yet
		s.bus.Publish(events.Event{Type: events.DeviceAdded, DeviceID: id, At: now,
			Data: events.DeviceData{Name: row.Name, Model: row.Model}})
	}
	return id, token, nil
}

// AuthDevice returns the id of the enrolled device owning the token.
func (s *Service) AuthDevice(ctx context.Context, token string) (string, error) {
	if !strings.HasPrefix(token, auth.DevicePrefix) {
		return "", auth.ErrUnauthorized
	}
	id, err := s.q.DeviceByTokenHash(ctx, auth.Hash(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return "", auth.ErrUnauthorized
	}
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// Seen records that the device connected now and answers its name and model;
// ErrUnknownDevice for a device deleted meanwhile.
func (s *Service) Seen(ctx context.Context, deviceID string) (name, model string, err error) {
	var id pgtype.UUID
	if err := id.Scan(deviceID); err != nil {
		return "", "", err
	}
	row, err := s.q.UpdateLastSeen(ctx, db.UpdateLastSeenParams{ID: id, SeenAt: ts(s.now())})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrUnknownDevice
	}
	return row.Name, row.Model, err
}

type Listed struct {
	ID         string
	Name       string
	Model      string
	Status     string
	Room       string
	LastSeenAt *time.Time
}

func (s *Service) List(ctx context.Context) ([]Listed, error) {
	rows, err := s.q.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Listed, 0, len(rows))
	for _, r := range rows {
		d := Listed{ID: r.ID.String(), Name: r.Name, Model: r.Model, Status: r.Status, Room: r.Room}
		if r.LastSeenAt.Valid {
			t := r.LastSeenAt.Time
			d.LastSeenAt = &t
		}
		out = append(out, d)
	}
	return out, nil
}

// Room is the device's room, "" for none or an unknown device.
func (s *Service) Room(ctx context.Context, deviceID string) string {
	var id pgtype.UUID
	if err := id.Scan(deviceID); err != nil {
		return ""
	}
	room, err := s.q.DeviceRoom(ctx, id)
	if err != nil {
		return ""
	}
	return room
}
