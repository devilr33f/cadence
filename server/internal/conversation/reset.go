package conversation

import (
	"context"
	"crypto/rand"
	"errors"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// TriggerReset marks the deterministic reset turn. Its command and
// confirmation stay in the history API, but are neither prompt context nor
// learning input (the current boundary already excludes them too).
const TriggerReset = "reset"

// ResetTurn is the optional turn stored for a voice reset.
type ResetTurn struct {
	ID, Text, Reply string
}

// Start is the id the device's current conversation starts after, 0 before
// its first reset. An unknown device is ErrNotFound.
func (s *Store) Start(ctx context.Context, deviceID string) (int64, error) {
	dev, err := parseDevice(deviceID)
	if err != nil {
		return 0, ErrNotFound
	}
	id, err := s.q.ConversationStart(ctx, dev)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, readFailed(ctx)
	}
	return id, nil
}

// Reset moves the context start to the device's newest committed message,
// without deleting a turn. It returns that boundary.
func (s *Store) Reset(ctx context.Context, deviceID string) (int64, error) {
	dev, err := parseDevice(deviceID)
	if err != nil {
		return 0, ErrNotFound
	}
	id, err := s.q.ResetConversation(ctx, dev)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, readFailed(ctx)
	}
	return id, nil
}

// Through returns the messages after afterID through uptoID, oldest first;
// past limit, the newest limit. Unlike After it has no settle-time cutoff, so a
// reset can close and learn from the segment that just ended.
func (s *Store) Through(ctx context.Context, deviceID string, afterID, uptoID int64, limit int) ([]Stored, error) {
	if limit <= 0 || uptoID <= afterID {
		return nil, nil
	}
	dev, err := parseDevice(deviceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ConversationMessagesThrough(ctx, db.ConversationMessagesThroughParams{
		DeviceID: dev, AfterID: afterID, UptoID: uptoID, MaxRows: int32(min(limit, math.MaxInt32)),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Stored, len(rows))
	for i, r := range rows {
		m, err := decode(r.Content)
		if err != nil {
			return nil, errors.New("conversation: decode message")
		}
		out[i] = Stored{ID: r.ID, Trigger: r.Trigger.String, Message: m}
	}
	return out, nil
}

// Confirmation is the short reply every reset path gives.
func Confirmation(language string) string {
	base, _, _ := strings.Cut(strings.ToLower(language), "-")
	if base == "ru" {
		return "Начинаем заново."
	}
	return "Starting over."
}

// CompactRequests is the compactor's manual surface. Both calls return at once;
// false means the device's previous job still runs.
type CompactRequests interface {
	CompactClosed(deviceID string, uptoID int64) bool
	RequestCompaction(deviceID string) bool
}

// Resetter coordinates the durable boundary and the closed segment's existing
// compaction/learning path. Notify is quick and carries no transcript to logs.
type Resetter struct {
	store     *Store
	compactor CompactRequests
	notify    func(deviceID, sessionID string)
}

func NewResetter(store *Store, compactor CompactRequests) *Resetter {
	return &Resetter{store: store, compactor: compactor}
}

// OnReset sets the room fan-out, called after a reset lands. Nil disables it.
func (r *Resetter) OnReset(notify func(deviceID, sessionID string)) { r.notify = notify }

// Reset stores an optional reset turn, then puts it and every earlier turn
// behind the boundary. The closed segment excludes the reset turn itself.
func (r *Resetter) Reset(ctx context.Context, deviceID string, turn *ResetTurn) (int64, error) {
	if turn != nil {
		if turn.ID == "" {
			turn.ID = "reset_" + rand.Text()
		}
		msgs := make([]providers.Message, 0, 2)
		if turn.Text != "" {
			msgs = append(msgs, providers.Message{Role: roleUser, Content: turn.Text})
		}
		if turn.Reply != "" {
			msgs = append(msgs, providers.Message{Role: roleAssistant, Content: turn.Reply})
		}
		if err := r.store.Append(ctx, deviceID, turn.ID, TurnMeta{Trigger: TriggerReset}, msgs); err != nil {
			return 0, err
		}
	}
	boundary, err := r.store.Reset(ctx, deviceID)
	if err != nil {
		return 0, err
	}
	if r.compactor != nil && boundary > 0 {
		r.compactor.CompactClosed(deviceID, boundary)
	}
	if r.notify != nil {
		session := ""
		if turn != nil {
			session = turn.ID
		}
		r.notify(deviceID, session)
	}
	return boundary, nil
}

// CompactNow starts a manual compaction of the current segment. An unknown
// device is ErrNotFound; false means one already runs.
func (r *Resetter) CompactNow(ctx context.Context, deviceID string) (bool, error) {
	if _, err := r.store.Start(ctx, deviceID); err != nil {
		return false, err
	}
	if r.compactor == nil {
		return false, nil
	}
	return r.compactor.RequestCompaction(deviceID), nil
}
