package tasks

import (
	"context"
	"math"
	"time"

	"github.com/google/uuid"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

const (
	// MaxNotice caps what a notice says, in characters.
	MaxNotice = 1000
	// NoticeRetention is how long a spoken notice stays: as long as a
	// transcript (D8); one still waiting stays with its task.
	NoticeRetention = 14 * 24 * time.Hour
)

// Notice is what an event turn said about one event of a task, waiting to be
// spoken on its device. Its text is user data: the DB and the admin API
// only, never a log. Token names the question the turn read, if any: once the
// notice is spoken, the device's user was told it. Stale is a notice whose
// task changed since (a question answered meanwhile): it is no news any more.
type Notice struct {
	TaskID   uuid.UUID
	ShortID  string
	EventSeq int
	DeviceID uuid.UUID
	Token    string
	Text     string
	Stale    bool
}

// reportable events start an event turn (M5b-D6); each leaves its task
// unreported until it is told.
func reportable(kind string) bool {
	return kind == "waiting_input" || kind == "done" || kind == "failed"
}

// seq32 is an event seq as the store keeps it (int); one out of its range
// names no event.
func seq32(seq int) int32 {
	if seq < 0 || seq > math.MaxInt32 {
		return -1
	}
	return int32(seq)
}

// AddNotice stores what the turn for event n.EventSeq of task n.TaskID said
// on n.DeviceID, in place of an older event's notice still waiting; one of a
// newer event, or this event's own, stays as it is.
func (s *Store) AddNotice(ctx context.Context, n Notice) error {
	return s.inTx(ctx, func(qs *db.Queries) error {
		id, ev := pgUUID(n.TaskID), seq32(n.EventSeq)
		if err := qs.DropOlderTaskNotices(ctx, db.DropOlderTaskNoticesParams{TaskID: id, EventSeq: ev}); err != nil {
			return wrap("notice", err)
		}
		return wrap("notice", qs.AddTaskNotice(ctx, db.AddTaskNoticeParams{
			TaskID: id, EventSeq: ev, DeviceID: pgUUID(n.DeviceID), Token: n.Token, Text: n.Text,
		}))
	})
}

// NextNotice is the device's oldest undelivered notice, or ErrNotFound.
func (s *Store) NextNotice(ctx context.Context, device uuid.UUID) (Notice, error) {
	r, err := s.q.NextTaskNotice(ctx, pgUUID(device))
	if err != nil {
		return Notice{}, wrap("next notice", err)
	}
	return Notice{
		TaskID: r.TaskID.Bytes, ShortID: r.ShortID, EventSeq: int(r.EventSeq), DeviceID: r.DeviceID.Bytes,
		Token: r.Token, Text: r.Text, Stale: r.Stale,
	}, nil
}

// Delivered records that a notice was spoken, so it is never spoken again,
// marks its event told and its question told on its device, in one
// transaction: a notice the 14-day prune takes was always told, so its event
// never runs a second turn.
func (s *Store) Delivered(ctx context.Context, n Notice) error {
	return s.inTx(ctx, func(qs *db.Queries) error {
		if err := qs.MarkTaskNoticeDelivered(ctx, db.MarkTaskNoticeDeliveredParams{
			TaskID: pgUUID(n.TaskID), EventSeq: seq32(n.EventSeq),
		}); err != nil {
			return wrap("notice delivered", err)
		}
		if err := markReportedAt(ctx, qs, n.ShortID, n.EventSeq); err != nil {
			return err
		}
		return addTold(ctx, qs, n.TaskID, n.Token, n.DeviceID, "")
	})
}

// DropNotice deletes an undelivered notice.
func (s *Store) DropNotice(ctx context.Context, taskID uuid.UUID, seq int) error {
	return wrap("drop notice", s.q.DeleteTaskNotice(ctx, db.DeleteTaskNoticeParams{TaskID: pgUUID(taskID), EventSeq: seq32(seq)}))
}

// Undelivered counts the notices waiting on a device (zero device: all).
func (s *Store) Undelivered(ctx context.Context, device uuid.UUID) (int, error) {
	n, err := s.q.CountUndeliveredTaskNotices(ctx, pgUUID(device))
	return int(n), wrap("undelivered", err)
}

// NoticeDevices lists the devices with notices waiting.
func (s *Store) NoticeDevices(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.q.ListTaskNoticeDevices(ctx)
	if err != nil {
		return nil, wrap("notice devices", err)
	}
	out := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		out[i] = r.Bytes
	}
	return out, nil
}

// Undelivered counts the task updates waiting on a device (zero device:
// all), for the device's "news" light later (M6).
func (m *Manager) Undelivered(ctx context.Context, device uuid.UUID) (int, error) {
	return m.d.Store.Undelivered(ctx, device)
}
