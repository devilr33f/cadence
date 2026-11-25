package tasks

import (
	"context"

	"github.com/google/uuid"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// addTold records that the question token names was told on a device, by
// turn ("" for a spoken notice); the first telling stays. A question without
// a token, or no device, records nothing.
func addTold(ctx context.Context, qs *db.Queries, taskID uuid.UUID, token string, device uuid.UUID, turn string) error {
	if token == "" || device == uuid.Nil {
		return nil
	}
	return wrap("told", qs.AddTaskTold(ctx, db.AddTaskToldParams{
		TaskID: pgUUID(taskID), Token: token, DeviceID: pgUUID(device), TurnID: turn,
	}))
}

// MarkTold records that a turn on a device read a task's question, by its
// token, so its user may have heard it (Ruling M5b-R9).
func (m *Manager) MarkTold(ctx context.Context, shortID, token string, device uuid.UUID, turn string) error {
	t, err := m.d.Store.Get(ctx, shortID)
	if err != nil {
		return err
	}
	return addTold(ctx, m.d.Store.q, t.ID, token, device, turn)
}

// ToldBefore reports whether a task's question, by its token, was told on a
// device before the turn turn names: its notice spoken there, or another turn
// there read it. A voice approval answers only such a question: the user
// heard it before they spoke (Ruling M5b-R9). Without a turn there is no
// before: false.
func (m *Manager) ToldBefore(ctx context.Context, shortID, token string, device uuid.UUID, turn string) (bool, error) {
	if token == "" || device == uuid.Nil || turn == "" {
		return false, nil
	}
	ok, err := m.d.Store.q.TaskToldBefore(ctx, db.TaskToldBeforeParams{
		ShortID: shortID, Token: token, DeviceID: pgUUID(device), TurnID: turn,
	})
	return ok, wrap("told", err)
}
