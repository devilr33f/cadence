-- name: DropOlderTaskNotices :exec
DELETE FROM task_notices WHERE task_id = @task_id AND delivered_at IS NULL AND event_seq < @event_seq;

-- name: AddTaskNotice :exec
-- an undelivered notice of a newer event, or this event's own, stays
INSERT INTO task_notices (task_id, event_seq, device_id, token, text)
VALUES (@task_id, @event_seq, @device_id, @token, @text)
ON CONFLICT DO NOTHING;

-- name: NextTaskNotice :one
-- the device's oldest undelivered notice; stale once its task has a newer
-- event, but one of a progress event (an event turn's own answer) only once a
-- newer event is more than progress
SELECT n.task_id, n.event_seq, n.device_id, n.token, n.text, t.short_id,
    EXISTS (
        SELECT 1 FROM task_events e
        WHERE e.task_id = n.task_id AND e.seq > n.event_seq
          AND (e.kind <> 'progress' OR o.kind IS DISTINCT FROM 'progress')
    ) AS stale
FROM task_notices n
JOIN tasks t ON t.id = n.task_id
LEFT JOIN task_events o ON o.task_id = n.task_id AND o.seq = n.event_seq
WHERE n.device_id = @device_id AND n.delivered_at IS NULL
ORDER BY n.created_at, n.event_seq
LIMIT 1;

-- name: MarkTaskNoticeDelivered :exec
UPDATE task_notices SET delivered_at = now()
WHERE task_id = @task_id AND event_seq = @event_seq AND delivered_at IS NULL;

-- name: DeleteTaskNotice :exec
DELETE FROM task_notices WHERE task_id = @task_id AND event_seq = @event_seq AND delivered_at IS NULL;

-- name: CountUndeliveredTaskNotices :one
SELECT count(*) FROM task_notices
WHERE delivered_at IS NULL AND (sqlc.narg(device_id)::uuid IS NULL OR device_id = sqlc.narg(device_id));

-- name: ListTaskNoticeDevices :many
SELECT DISTINCT device_id FROM task_notices WHERE delivered_at IS NULL ORDER BY device_id;

-- name: PruneTaskNotices :execrows
DELETE FROM task_notices WHERE delivered_at < @older_than;

-- name: ListNoticesOfTask :many
SELECT event_seq, text, created_at, delivered_at FROM task_notices WHERE task_id = @task_id ORDER BY event_seq;
