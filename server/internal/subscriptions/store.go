package subscriptions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// Retention is how long an update stays, as a transcript (D8).
const Retention = 14 * 24 * time.Hour

var (
	ErrNotFound = errors.New("subscriptions: not found")
	// ErrInstance is a topic whose instance no provider instance of its
	// module has.
	ErrInstance = errors.New("subscriptions: unknown instance")
	ErrInvalid  = errors.New("subscriptions: invalid")
)

// Store keeps the subscriptions, the updates that reached each device and
// which questions were told where.
type Store struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool, q: db.New(pool)} }

// wrap keeps row content out of errors: postgres detail lines quote the row.
func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("subscriptions: %s: %w", op, ErrNotFound)
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		if len(pg.Code) == 5 && (pg.Code[:2] == "23" || pg.Code == "22P02") {
			return fmt.Errorf("subscriptions: %s: %w", op, ErrInvalid)
		}
		return fmt.Errorf("subscriptions: %s: db error %s", op, pg.Code)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("subscriptions: %s failed", op)
}

func pgUUID(s string) pgtype.UUID {
	u, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: u, Valid: true}
}

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

func toSub(r db.Subscription) Subscription {
	s := Subscription{
		ID: uuidString(r.ID), Topic: r.Topic, Events: r.Events, Delivery: r.Delivery, Device: uuidString(r.DeviceID),
		Until: r.Until, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt.Time.UTC(),
	}
	if s.Events == nil {
		s.Events = []string{}
	}
	s.topic, _ = ParseTopic(r.Topic)
	return s
}

// Create stores a subscription, replacing the device's one of the same
// topic, under the settings lock a provider delete takes: the topic's
// instance must be a provider instance of its module (ErrInstance), so a
// delete either sees the subscription and is refused, or comes first.
func (s *Store) Create(ctx context.Context, sub Subscription) (Subscription, error) {
	t, err := ParseTopic(sub.Topic)
	if err != nil {
		return Subscription{}, err
	}
	var out Subscription
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := settings.Lock(ctx, q); err != nil {
			return wrap("lock", err)
		}
		mod, err := q.ProviderInstanceModule(ctx, t.Instance)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && mod != t.Module {
			return ErrInstance
		}
		if err != nil {
			return wrap("instance", err)
		}
		r, err := q.UpsertSubscription(ctx, db.UpsertSubscriptionParams{
			DeviceID: pgUUID(sub.Device), Topic: t.String(), Module: t.Module, Instance: t.Instance,
			Events: sub.Events, Delivery: sub.Delivery, Until: sub.Until, CreatedBy: sub.CreatedBy,
		})
		if err != nil {
			return wrap("create", err)
		}
		out = toSub(r)
		return nil
	})
	return out, err
}

// List is a device's subscriptions, every device's for "", newest first.
func (s *Store) List(ctx context.Context, device string) ([]Subscription, error) {
	var dev pgtype.UUID
	if device != "" {
		if dev = pgUUID(device); !dev.Valid {
			return []Subscription{}, nil
		}
	}
	rows, err := s.q.ListSubscriptions(ctx, dev)
	if err != nil {
		return nil, wrap("list", err)
	}
	out := make([]Subscription, len(rows))
	for i, r := range rows {
		out[i] = toSub(r)
	}
	return out, nil
}

// ofInstance is the subscriptions naming a module's instance.
func (s *Store) ofInstance(ctx context.Context, module, instance string) ([]Subscription, error) {
	rows, err := s.q.ListInstanceSubscriptions(ctx, db.ListInstanceSubscriptionsParams{Module: module, Instance: instance})
	if err != nil {
		return nil, wrap("list", err)
	}
	out := make([]Subscription, len(rows))
	for i, r := range rows {
		out[i] = toSub(r)
	}
	return out, nil
}

type instanceKey struct{ module, instance string }

func (s *Store) instances(ctx context.Context) ([]instanceKey, error) {
	rows, err := s.q.ListSubscribedInstances(ctx)
	if err != nil {
		return nil, wrap("instances", err)
	}
	out := make([]instanceKey, len(rows))
	for i, r := range rows {
		out[i] = instanceKey{r.Module, r.Instance}
	}
	return out, nil
}

// Get is one subscription.
func (s *Store) Get(ctx context.Context, id string) (Subscription, error) {
	u := pgUUID(id)
	if !u.Valid {
		return Subscription{}, ErrNotFound
	}
	r, err := s.q.GetSubscription(ctx, u)
	if err != nil {
		return Subscription{}, wrap("get", err)
	}
	return toSub(r), nil
}

// Delete removes a subscription by id, of that device alone unless device is
// "": ErrNotFound when there is none.
func (s *Store) Delete(ctx context.Context, id, device string) error {
	u := pgUUID(id)
	var dev pgtype.UUID
	if device != "" {
		dev = pgUUID(device)
	}
	if !u.Valid || device != "" && !dev.Valid {
		return ErrNotFound
	}
	n, err := s.q.DeleteSubscription(ctx, db.DeleteSubscriptionParams{ID: u, DeviceID: dev})
	if err != nil {
		return wrap("delete", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteTopic removes the device's subscription of a topic.
func (s *Store) DeleteTopic(ctx context.Context, device, topic string) error {
	t, err := ParseTopic(topic)
	if err != nil {
		return ErrNotFound
	}
	n, err := s.q.DeleteSubscriptionTopic(ctx, db.DeleteSubscriptionTopicParams{DeviceID: pgUUID(device), Topic: t.String()})
	if err != nil {
		return wrap("delete", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// InboxItem is one update as it reached one device. Its texts are user content.
type InboxItem struct {
	ID             int64     `json:"id"`
	Device         string    `json:"device"`
	SubscriptionID string    `json:"subscriptionId,omitempty"`
	Module         string    `json:"module"`
	Instance       string    `json:"instance"`
	Topic          string    `json:"topic"`
	Event          string    `json:"event"`
	Kind           string    `json:"kind,omitempty"`
	Title          string    `json:"title,omitempty"`
	Text           string    `json:"text,omitempty"`
	RefID          string    `json:"refId,omitempty"`
	Question       *Question `json:"question,omitempty"`
	Delivery       string    `json:"delivery" enum:"speaker,agent"`
	State          string    `json:"state" enum:"pending,delivered,inbox"`
	HeldBusy       bool      `json:"heldBusy,omitempty"`
	At             time.Time `json:"at"`
	Seen           bool      `json:"seen"`
	Open           bool      `json:"open,omitempty" doc:"a question nobody answered and the module did not withdraw"`
}

const (
	statePending   = "pending"
	stateDelivered = "delivered"
	stateInbox     = "inbox"
)

func toItem(r db.SubscriptionItem) InboxItem {
	it := InboxItem{
		ID: r.ID, Device: uuidString(r.DeviceID), SubscriptionID: uuidString(r.SubscriptionID), Module: r.Module,
		Instance: r.Instance, Topic: r.Topic, Event: r.Event, Kind: r.Kind, Title: r.Title, Text: r.Text, RefID: r.RefID,
		Delivery: r.Delivery, State: r.State, HeldBusy: r.HeldBusy, At: r.At.Time.UTC(), Seen: r.SeenAt.Valid,
	}
	if len(r.Question) > 0 {
		var q Question
		if json.Unmarshal(r.Question, &q) == nil {
			it.Question = &q
			it.Open = !r.AnsweredAt.Valid && !r.WithdrawnAt.Valid
		}
	}
	return it
}

func toItems(rows []db.SubscriptionItem) []InboxItem {
	out := make([]InboxItem, len(rows))
	for i, r := range rows {
		out[i] = toItem(r)
	}
	return out
}

func (it InboxItem) ref() Ref { return Ref{Instance: it.Instance, ID: it.RefID} }

// add stores an update for a device, withdrawing the ref's older open
// questions when it asks a new one or withdraws them; a next_stop
// subscription that took a stopped event goes in the same transaction.
func (s *Store) add(ctx context.Context, it InboxItem, endSub bool) (int64, error) {
	var id int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		var question []byte
		token := ""
		if it.Question != nil {
			question, _ = json.Marshal(it.Question)
			token = it.Question.Token
		}
		var err error
		id, err = q.AddSubscriptionItem(ctx, db.AddSubscriptionItemParams{
			DeviceID: pgUUID(it.Device), SubscriptionID: pgUUID(it.SubscriptionID), Module: it.Module,
			Instance: it.Instance, Topic: it.Topic, Event: it.Event, Kind: it.Kind, Title: it.Title, Text: it.Text,
			RefID: it.RefID, Question: question, Token: token, Delivery: it.Delivery, State: it.State,
			At: pgtype.Timestamptz{Time: it.At, Valid: true},
		})
		if err != nil {
			return wrap("add", err)
		}
		if endSub && it.SubscriptionID != "" {
			if _, err := q.DeleteSubscription(ctx, db.DeleteSubscriptionParams{ID: pgUUID(it.SubscriptionID)}); err != nil {
				return wrap("end", err)
			}
		}
		return nil
	})
	return id, err
}

// withdraw ends a ref's open questions, but the one keep names.
func (s *Store) withdraw(ctx context.Context, module string, ref Ref, keep string) error {
	_, err := s.q.WithdrawSubscriptionQuestions(ctx, db.WithdrawSubscriptionQuestionsParams{
		Module: module, Instance: ref.Instance, RefID: ref.ID, Keep: keep,
	})
	return wrap("withdraw", err)
}

func (s *Store) pending(ctx context.Context, device string) ([]InboxItem, error) {
	rows, err := s.q.PendingSubscriptionItems(ctx, pgUUID(device))
	if err != nil {
		return nil, wrap("pending", err)
	}
	return toItems(rows), nil
}

func (s *Store) pendingDevices(ctx context.Context) ([]string, error) {
	rows, err := s.q.PendingSubscriptionDevices(ctx)
	if err != nil {
		return nil, wrap("pending devices", err)
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = uuidString(r)
	}
	return out, nil
}

// delivered marks items delivered and, when told, their questions told on
// the device, together.
func (s *Store) delivered(ctx context.Context, device string, items []InboxItem, told bool) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		ids := make([]int64, len(items))
		for i, it := range items {
			ids[i] = it.ID
		}
		if err := q.MarkSubscriptionItemsDelivered(ctx, ids); err != nil {
			return wrap("delivered", err)
		}
		if !told {
			return nil
		}
		for _, it := range items {
			if err := addTold(ctx, q, it, device, ""); err != nil {
				return err
			}
		}
		return nil
	})
}

func addTold(ctx context.Context, q *db.Queries, it InboxItem, device, turn string) error {
	if it.Question == nil || it.Question.Token == "" || !it.Open {
		return nil
	}
	return wrap("told", q.AddSubscriptionTold(ctx, db.AddSubscriptionToldParams{
		Module: it.Module, Instance: it.Instance, RefID: it.RefID, Token: it.Question.Token,
		DeviceID: pgUUID(device), TurnID: turn,
	}))
}

func (s *Store) heldBusy(ctx context.Context, device string) error {
	return wrap("held", s.q.MarkSubscriptionItemsHeldBusy(ctx, pgUUID(device)))
}

func (s *Store) busyToInbox(ctx context.Context, device string) (int64, error) {
	n, err := s.q.MoveBusyHeldToInbox(ctx, pgUUID(device))
	return n, wrap("inbox", err)
}

// Unread is what the device's next woken turn reads: delivered or inbox
// updates no woken turn saw, and every open question.
func (s *Store) Unread(ctx context.Context, device string) ([]InboxItem, error) {
	rows, err := s.q.UnreadSubscriptionItems(ctx, pgUUID(device))
	if err != nil {
		return nil, wrap("unread", err)
	}
	return toItems(rows), nil
}

// Inbox is the device's inbox as the API shows it: pending updates, unread
// ones and open questions, oldest first, at most limit.
func (s *Store) Inbox(ctx context.Context, device string, limit int) ([]InboxItem, error) {
	dev := pgUUID(device)
	if !dev.Valid {
		return nil, ErrNotFound
	}
	rows, err := s.q.DeviceSubscriptionItems(ctx, db.DeviceSubscriptionItemsParams{DeviceID: dev, MaxRows: int32(min(max(limit, 1), maxInboxPage))})
	if err != nil {
		return nil, wrap("inbox", err)
	}
	return toItems(rows), nil
}

// seen marks the items a turn read and, with told set to the device, their
// open questions told on it by that turn.
func (s *Store) seen(ctx context.Context, turn string, items []InboxItem, told string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		ids := make([]int64, len(items))
		for i, it := range items {
			ids[i] = it.ID
		}
		if err := q.MarkSubscriptionItemsSeen(ctx, db.MarkSubscriptionItemsSeenParams{TurnID: turn, Ids: ids}); err != nil {
			return wrap("seen", err)
		}
		if told == "" {
			return nil
		}
		for _, it := range items {
			if err := addTold(ctx, q, it, told, turn); err != nil {
				return err
			}
		}
		return nil
	})
}

// ofTopic is a device's latest updates of a topic that reached it, oldest
// first, at most limit.
func (s *Store) ofTopic(ctx context.Context, device, topic string, limit int) ([]InboxItem, error) {
	dev := pgUUID(device)
	if !dev.Valid {
		return nil, ErrNotFound
	}
	rows, err := s.q.TopicSubscriptionItems(ctx, db.TopicSubscriptionItemsParams{DeviceID: dev, Topic: topic, MaxRows: int32(min(max(limit, 1), maxInspect))})
	if err != nil {
		return nil, wrap("topic", err)
	}
	items := toItems(rows)
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	return items, nil
}

// openQuestion is the newest open question of a token, on the device unless
// device is "".
func (s *Store) openQuestion(ctx context.Context, token, device string) (InboxItem, error) {
	var dev pgtype.UUID
	if device != "" {
		dev = pgUUID(device)
	}
	rows, err := s.q.OpenSubscriptionQuestions(ctx, db.OpenSubscriptionQuestionsParams{Token: token, DeviceID: dev})
	if err != nil {
		return InboxItem{}, wrap("question", err)
	}
	if len(rows) == 0 {
		return InboxItem{}, ErrNotFound
	}
	return toItem(rows[0]), nil
}

func (s *Store) answered(ctx context.Context, it InboxItem) error {
	_, err := s.q.AnswerSubscriptionQuestion(ctx, db.AnswerSubscriptionQuestionParams{
		Module: it.Module, Instance: it.Instance, RefID: it.RefID, Token: it.Question.Token,
	})
	return wrap("answered", err)
}

// toldBefore reports whether the question was told on the device outside
// the turn turn names; without a turn there is no before.
func (s *Store) toldBefore(ctx context.Context, it InboxItem, device, turn string) (bool, error) {
	if it.Question == nil || it.Question.Token == "" || turn == "" {
		return false, nil
	}
	ok, err := s.q.SubscriptionToldBefore(ctx, db.SubscriptionToldBeforeParams{
		Module: it.Module, Instance: it.Instance, RefID: it.RefID, Token: it.Question.Token,
		DeviceID: pgUUID(device), TurnID: turn,
	})
	return ok, wrap("told", err)
}

// Prune deletes the updates and tellings older than olderThan.
func (s *Store) Prune(ctx context.Context, olderThan time.Time) (int64, error) {
	at := pgtype.Timestamptz{Time: olderThan, Valid: true}
	n, err := s.q.PruneSubscriptionItems(ctx, at)
	if err != nil {
		return 0, wrap("prune", err)
	}
	_, err = s.q.PruneSubscriptionTold(ctx, at)
	return n, wrap("prune", err)
}

// RunRetention prunes at start and then every interval until ctx ends; stop
// waits for the loop. A failure logs its operation alone.
func (s *Store) RunRetention(ctx context.Context, log *slog.Logger, every time.Duration) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	prune := func() {
		n, err := s.Prune(ctx, time.Now().Add(-Retention))
		switch {
		case err != nil && ctx.Err() == nil:
			log.Error("subscription_prune_failed")
		case n > 0:
			log.Info("subscription_pruned", "items", n)
		}
	}
	go func() {
		defer close(done)
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			prune()
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}
