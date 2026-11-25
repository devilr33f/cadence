package devices

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// Delete removes the device and everything keyed to it in one transaction:
// its settings overrides, and through the store's cascades its token (on its
// row), enrol codes, capabilities with their sealed secrets, device-scope
// memories, conversation turns with their messages and compactions, task
// notices and told marks, and Home Assistant usage. Its log lines and metric
// points stay until their retention. A transaction Postgres aborts to break a
// deadlock or a serialization conflict (a learning run's insert, the
// conversation prune) runs once more. Once committed, its live link closes
// with 1008, the hub refuses it from then on and the live feed hears
// device_deleted; enrolling the same hardware again makes a new device.
func (s *Service) Delete(ctx context.Context, deviceID string) error {
	return s.delete(ctx, deviceID, false)
}

// ErrNotPending refuses a pending-only delete of a device that is enrolled.
var ErrNotPending = errors.New("device not pending")

// DeletePending is Delete for a device that is still pending. An enrolled one
// is ErrNotPending and keeps everything, also one whose enrolment commits
// while the delete waits for its row: the check is the delete's own statement.
func (s *Service) DeletePending(ctx context.Context, deviceID string) error {
	return s.delete(ctx, deviceID, true)
}

func (s *Service) delete(ctx context.Context, deviceID string, pendingOnly bool) error {
	id, ok := parseID(deviceID)
	if !ok {
		return ErrNotFound
	}
	dev := id.String()
	err := s.deleteOnce(ctx, id, dev, pendingOnly)
	if code, again := aborted(err); again {
		s.log.Warn("device_delete_retried", "code", code)
		err = s.deleteOnce(ctx, id, dev, pendingOnly)
	}
	if err != nil {
		return err
	}
	s.hub.Remove(dev)
	s.forget(dev)
	s.log.Info("device_deleted", "device_id", dev)
	s.notify()
	s.deleted(dev)
	return nil
}

func (s *Service) deleteOnce(ctx context.Context, id pgtype.UUID, dev string, pendingOnly bool) error {
	return pgx.BeginFunc(ctx, s.store, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		del := q.DeleteDevice
		if pendingOnly {
			del = q.DeletePendingDevice
		}
		// the device row first: a settings patch holds it (FOR KEY SHARE)
		// until it commits, so the delete waits for it and then removes what
		// it stored, and a later patch finds no device
		n, err := del(ctx, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return notDeleted(ctx, q, id, pendingOnly)
		}
		// a device's settings scope is its canonical id
		return q.DeleteSettings(ctx, dev)
	})
}

// notDeleted is why a delete took no row: no such device, or, for a
// pending-only one, a device that is enrolled.
func notDeleted(ctx context.Context, q *db.Queries, id pgtype.UUID, pendingOnly bool) error {
	if !pendingOnly {
		return ErrNotFound
	}
	_, err := q.DeviceByID(ctx, id)
	switch {
	case err == nil:
		return ErrNotPending
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	}
	return err
}

// aborted names the code of a transaction Postgres rolled back to break a
// deadlock (40P01) or a serialization conflict (40001): nothing of it stays,
// so it may run again.
func aborted(err error) (string, bool) {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "40P01" || pg.Code == "40001") {
		return pg.Code, true
	}
	return "", false
}

// forget drops what the service keeps of a deleted device in memory.
func (s *Service) forget(dev string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.assemblies, dev)
	delete(s.pushLocks, dev)
}
