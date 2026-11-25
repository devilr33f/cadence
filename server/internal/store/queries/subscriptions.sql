-- name: UpsertSubscription :one
-- one subscription per device and topic: a new one replaces it, as the newest
INSERT INTO subscriptions (device_id, topic, module, instance, events, delivery, until, created_by)
VALUES (@device_id, @topic, @module, @instance, @events::text[], @delivery, @until, @created_by)
ON CONFLICT (device_id, topic) DO UPDATE
SET events = excluded.events, delivery = excluded.delivery, until = excluded.until,
    created_by = excluded.created_by, created_at = now()
RETURNING *;

-- name: ListSubscriptions :many
-- a device's subscriptions, or every device's when device_id is null, newest first
SELECT * FROM subscriptions
WHERE sqlc.narg('device_id')::uuid IS NULL OR device_id = sqlc.narg('device_id')::uuid
ORDER BY created_at DESC, id;

-- name: ListInstanceSubscriptions :many
SELECT * FROM subscriptions WHERE module = @module AND instance = @instance ORDER BY created_at DESC, id;

-- name: ListSubscribedInstances :many
SELECT DISTINCT module, instance FROM subscriptions ORDER BY module, instance;

-- name: GetSubscription :one
SELECT * FROM subscriptions WHERE id = @id;

-- name: DeleteSubscription :execrows
DELETE FROM subscriptions
WHERE id = @id AND (sqlc.narg('device_id')::uuid IS NULL OR device_id = sqlc.narg('device_id')::uuid);

-- name: DeleteSubscriptionTopic :execrows
DELETE FROM subscriptions WHERE device_id = @device_id AND topic = @topic;

-- name: SubscriptionNamesInstance :one
SELECT EXISTS (SELECT 1 FROM subscriptions WHERE instance = @instance);

-- name: ProviderInstanceModule :one
SELECT module FROM provider_instances WHERE name = @name;

-- name: AddSubscriptionItem :one
INSERT INTO subscription_items (device_id, subscription_id, module, instance, topic, event, kind, title, text,
                                ref_id, question, token, delivery, state, at)
VALUES (@device_id, sqlc.narg('subscription_id')::uuid, @module, @instance, @topic, @event, @kind, @title, @text,
        @ref_id, sqlc.narg('question')::jsonb, @token, @delivery, @state, @at)
RETURNING id;

-- name: WithdrawSubscriptionQuestions :execrows
-- the open questions of a ref, but the one token keep names
UPDATE subscription_items SET withdrawn_at = now()
WHERE module = @module AND instance = @instance AND ref_id = @ref_id AND question IS NOT NULL
  AND answered_at IS NULL AND withdrawn_at IS NULL AND token <> @keep;

-- name: PendingSubscriptionItems :many
SELECT * FROM subscription_items WHERE device_id = @device_id AND state = 'pending' ORDER BY id;

-- name: PendingSubscriptionDevices :many
SELECT DISTINCT device_id FROM subscription_items WHERE state = 'pending';

-- name: MarkSubscriptionItemsDelivered :exec
UPDATE subscription_items SET state = 'delivered', delivered_at = now()
WHERE id = ANY(@ids::bigint[]) AND state = 'pending';

-- name: MarkSubscriptionItemsHeldBusy :exec
UPDATE subscription_items SET held_busy = true
WHERE device_id = @device_id AND state = 'pending' AND NOT held_busy;

-- name: MoveBusyHeldToInbox :execrows
UPDATE subscription_items SET state = 'inbox'
WHERE device_id = @device_id AND state = 'pending' AND held_busy;

-- name: UnreadSubscriptionItems :many
-- what a device's next woken turn reads: delivered or inbox updates no woken
-- turn saw, and every open question
SELECT * FROM subscription_items
WHERE device_id = @device_id AND state <> 'pending'
  AND (seen_at IS NULL OR (question IS NOT NULL AND answered_at IS NULL AND withdrawn_at IS NULL))
ORDER BY id;

-- name: DeviceSubscriptionItems :many
-- the inbox as the API shows it: pending, unread and open questions
SELECT * FROM subscription_items
WHERE device_id = @device_id
  AND (state = 'pending' OR seen_at IS NULL OR (question IS NOT NULL AND answered_at IS NULL AND withdrawn_at IS NULL))
ORDER BY id
LIMIT @max_rows::int;

-- name: MarkSubscriptionItemsSeen :exec
UPDATE subscription_items SET seen_at = now(), seen_turn = @turn_id
WHERE id = ANY(@ids::bigint[]) AND seen_at IS NULL;

-- name: TopicSubscriptionItems :many
-- a device's latest updates of one topic that reached it, newest first
SELECT * FROM subscription_items
WHERE device_id = @device_id AND topic = @topic AND state <> 'pending'
ORDER BY id DESC
LIMIT @max_rows::int;

-- name: OpenSubscriptionQuestions :many
-- the open questions of a token, newest first; device_id narrows them to a device
SELECT * FROM subscription_items
WHERE token = @token AND question IS NOT NULL AND answered_at IS NULL AND withdrawn_at IS NULL
  AND (sqlc.narg('device_id')::uuid IS NULL OR device_id = sqlc.narg('device_id')::uuid)
ORDER BY id DESC;

-- name: AnswerSubscriptionQuestion :execrows
UPDATE subscription_items SET answered_at = now(), seen_at = coalesce(seen_at, now())
WHERE module = @module AND instance = @instance AND ref_id = @ref_id AND token = @token
  AND question IS NOT NULL AND answered_at IS NULL AND withdrawn_at IS NULL;

-- name: AddSubscriptionTold :exec
-- the first telling of a question on a device stays
INSERT INTO subscription_told (module, instance, ref_id, token, device_id, turn_id)
VALUES (@module, @instance, @ref_id, @token, @device_id, @turn_id)
ON CONFLICT DO NOTHING;

-- name: SubscriptionToldBefore :one
-- the question was told on the device outside the turn turn_id names
SELECT EXISTS (
    SELECT 1 FROM subscription_told
    WHERE module = @module AND instance = @instance AND ref_id = @ref_id AND token = @token
      AND device_id = @device_id AND turn_id <> @turn_id
);

-- name: PruneSubscriptionItems :execrows
DELETE FROM subscription_items WHERE created_at < @older_than::timestamptz;

-- name: PruneSubscriptionTold :execrows
DELETE FROM subscription_told WHERE at < @older_than::timestamptz;
