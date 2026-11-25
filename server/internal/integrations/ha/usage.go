package ha

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

const codeStore = "store"

// Scored is an entity id with its usage score decayed to now.
type Scored struct {
	EntityID string
	Score    float64
}

// Usage ranks the entities the home tools changed (M5c-D8): per device and
// entity a score that halves every 14 days and grows by one on each
// successful control. It keeps entity ids only; a device's rows go with it.
type Usage struct {
	q   *db.Queries
	log *slog.Logger
}

// NewUsage stores on q; a nil log is slog's default.
func NewUsage(q *db.Queries, log *slog.Logger) *Usage {
	if log == nil {
		log = slog.Default()
	}
	return &Usage{q: q, log: log}
}

// Record counts a use of entityID on deviceID. It is a ranking hint, so it
// never fails its caller: a failure logs its code only, never the ids (a
// postgres error or a panic value can quote them).
func (u *Usage) Record(ctx context.Context, deviceID, entityID string) {
	defer func() {
		if recover() != nil {
			u.log.Error("ha_usage_failed", "code", "panic")
		}
	}()
	if code := u.record(ctx, deviceID, entityID); code != "" {
		u.log.Warn("ha_usage_failed", "code", code)
	}
}

func (u *Usage) record(ctx context.Context, deviceID, entityID string) string {
	dev, err := uuid.Parse(deviceID)
	switch {
	case err != nil:
		return "bad_device"
	case !validEntityID(entityID):
		return "bad_entity"
	}
	err = u.q.RecordHAUsage(ctx, db.RecordHAUsageParams{DeviceID: pgtype.UUID{Bytes: dev, Valid: true}, EntityID: entityID})
	switch {
	case err == nil:
		return ""
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return CodeTimeout
	case ctx.Err() != nil:
		return "canceled"
	}
	return codeStore
}

// Top is the device's n most used entities, ties by entity id.
func (u *Usage) Top(ctx context.Context, device uuid.UUID, n int) ([]Scored, error) {
	if n <= 0 {
		return nil, nil
	}
	rows, err := u.q.TopHAUsage(ctx, db.TopHAUsageParams{DeviceID: pgtype.UUID{Bytes: device, Valid: true}, N: rowCap(n)})
	if err != nil {
		return nil, fail(codeStore)
	}
	out := make([]Scored, len(rows))
	for i, r := range rows {
		out[i] = Scored{EntityID: r.EntityID, Score: r.Decayed}
	}
	return out, nil
}

// Household is the n entities the household uses most, the devices' scores
// summed, ties by entity id.
func (u *Usage) Household(ctx context.Context, n int) ([]Scored, error) {
	if n <= 0 {
		return nil, nil
	}
	rows, err := u.q.HouseholdHAUsage(ctx, rowCap(n))
	if err != nil {
		return nil, fail(codeStore)
	}
	out := make([]Scored, len(rows))
	for i, r := range rows {
		out[i] = Scored{EntityID: r.EntityID, Score: r.Decayed}
	}
	return out, nil
}

// rowCap caps n at the most entities the home keeps.
func rowCap(n int) int32 { return int32(max(0, min(n, maxEntities))) } //nolint:gosec // clamped
