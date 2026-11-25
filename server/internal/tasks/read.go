package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// StoredEvent is one row of a task's history. For an answered event the
// answer holds who decided what; its words are gone after WordsRetention.
type StoredEvent struct {
	Seq    int
	Kind   string
	At     time.Time
	Answer json.RawMessage
}

// StoredNotice is a notice kept for a task: user data, never logged.
type StoredNotice struct {
	EventSeq    int
	Text        string
	CreatedAt   time.Time
	DeliveredAt *time.Time
}

// Events lists a task's events, oldest first.
func (s *Store) Events(ctx context.Context, taskID uuid.UUID) ([]StoredEvent, error) {
	rows, err := s.q.ListTaskEvents(ctx, pgUUID(taskID))
	if err != nil {
		return nil, wrap("events", err)
	}
	out := make([]StoredEvent, len(rows))
	for i, r := range rows {
		out[i] = StoredEvent{Seq: int(r.Seq), Kind: r.Kind, At: r.At.Time, Answer: r.Answer}
	}
	return out, nil
}

// NoticesOf lists the notices of a task, oldest event first.
func (s *Store) NoticesOf(ctx context.Context, taskID uuid.UUID) ([]StoredNotice, error) {
	rows, err := s.q.ListNoticesOfTask(ctx, pgUUID(taskID))
	if err != nil {
		return nil, wrap("notices", err)
	}
	out := make([]StoredNotice, len(rows))
	for i, r := range rows {
		out[i] = StoredNotice{EventSeq: int(r.EventSeq), Text: r.Text, CreatedAt: r.CreatedAt.Time}
		if r.DeliveredAt.Valid {
			v := r.DeliveredAt.Time
			out[i].DeliveredAt = &v
		}
	}
	return out, nil
}

// History is a task with its events and notices.
type History struct {
	Task    Task
	Events  []StoredEvent
	Notices []StoredNotice
}

// History reads a task with its events and notices.
func (m *Manager) History(ctx context.Context, shortID string) (History, error) {
	t, err := m.d.Store.Get(ctx, shortID)
	if err != nil {
		return History{}, err
	}
	evs, err := m.d.Store.Events(ctx, t.ID)
	if err != nil {
		return History{}, err
	}
	ns, err := m.d.Store.NoticesOf(ctx, t.ID)
	if err != nil {
		return History{}, err
	}
	return History{Task: t, Events: evs, Notices: ns}, nil
}

// errStale reports whether err is a stale answer, by the manager's check or
// the kind's own code.
func errStale(err error) bool {
	var ke *KindError
	return errors.Is(err, ErrStale) || (errors.As(err, &ke) && ke.Code == "hermes_stale")
}
