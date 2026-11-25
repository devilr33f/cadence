-- name: CreateTask :one
INSERT INTO tasks (short_id, kind, title, origin_device_id, status)
VALUES ('t-' || nextval('task_short_id_seq'), @kind, @title, sqlc.narg(origin_device_id), 'queued')
RETURNING *;

-- name: GetTaskByShortID :one
SELECT * FROM tasks WHERE short_id = @short_id;

-- name: GetTaskByRef :one
SELECT * FROM tasks WHERE kind = @kind AND external_ref = @external_ref;

-- name: ListTasks :many
SELECT * FROM tasks
WHERE (@kind::text = '' OR kind = @kind)
  AND (cardinality(@statuses::text[]) = 0 OR status = ANY(@statuses::text[]))
  AND (sqlc.narg(origin_device_id)::uuid IS NULL OR origin_device_id = sqlc.narg(origin_device_id))
ORDER BY created_at DESC, substr(short_id, 3)::bigint DESC
LIMIT @max_rows::int;

-- name: ListOpenTasks :many
SELECT * FROM tasks WHERE status IN ('queued', 'running', 'waiting_input') ORDER BY created_at, substr(short_id, 3)::bigint;

-- name: UpdateTask :one
UPDATE tasks SET
    status = @status,
    progress = @progress,
    question = sqlc.narg(question),
    result = sqlc.narg(result),
    error_code = sqlc.narg(error_code),
    external_ref = coalesce(sqlc.narg(external_ref), external_ref),
    finished_at = CASE WHEN @finished::bool THEN coalesce(finished_at, now()) ELSE NULL END,
    reported_at = CASE WHEN @unreport::bool THEN NULL ELSE reported_at END,
    updated_at = now()
WHERE id = @id
  AND (sqlc.narg(only_from)::text IS NULL OR status = sqlc.narg(only_from))
RETURNING *;

-- name: AddTaskEvent :one
INSERT INTO task_events (task_id, seq, kind, answer)
SELECT @task_id, coalesce(max(seq), 0) + 1, @kind, sqlc.narg(answer)::jsonb FROM task_events WHERE task_id = @task_id
RETURNING seq;

-- name: PruneTasks :execrows
DELETE FROM tasks WHERE finished_at < @older_than;

-- name: ListUnreportedTasks :many
SELECT * FROM tasks
WHERE finished_at IS NOT NULL AND reported_at IS NULL
  AND (sqlc.narg(origin_device_id)::uuid IS NULL OR origin_device_id = sqlc.narg(origin_device_id))
ORDER BY finished_at, substr(short_id, 3)::bigint;

-- name: MarkTaskReported :execrows
UPDATE tasks SET reported_at = now() WHERE short_id = @short_id AND reported_at IS NULL;

-- name: ScrubTaskAnswerWords :execrows
UPDATE task_events SET answer = answer - 'words'
WHERE kind = 'answered' AND answer ->> 'words' IS NOT NULL AND at < @older_than;

-- name: MarkTaskReportedAt :execrows
-- marks a task told up to its event seq: never when a newer waiting_input,
-- done or failed event is still to be told
UPDATE tasks t SET reported_at = now()
WHERE t.short_id = @short_id AND t.reported_at IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM task_events e
    WHERE e.task_id = t.id AND e.seq > @seq AND e.kind IN ('waiting_input', 'done', 'failed')
  );

-- name: ListTasksToReport :many
-- each task with a device whose latest waiting_input, done or failed event
-- was neither told nor turned into a notice yet, with that event's seq
SELECT sqlc.embed(t), r.seq
FROM tasks t
CROSS JOIN LATERAL (
    SELECT max(e.seq)::int AS seq FROM task_events e
    WHERE e.task_id = t.id AND e.kind IN ('waiting_input', 'done', 'failed')
) r
WHERE t.status IN ('waiting_input', 'done', 'failed')
  AND t.reported_at IS NULL
  AND t.origin_device_id IS NOT NULL
  AND r.seq IS NOT NULL
  AND (sqlc.narg(short_id)::text IS NULL OR t.short_id = sqlc.narg(short_id))
  AND NOT EXISTS (SELECT 1 FROM task_notices n WHERE n.task_id = t.id AND n.event_seq >= r.seq)
ORDER BY t.updated_at, substr(t.short_id, 3)::bigint;

-- name: ListTaskEvents :many
SELECT seq, kind, at, answer FROM task_events WHERE task_id = @task_id ORDER BY seq;

-- name: LockTask :one
-- holds the task's row until the transaction ends: a statement after it sees
-- every event an Apply holding the row committed meanwhile
SELECT id FROM tasks WHERE short_id = @short_id FOR UPDATE;

-- name: LatestTaskEventSeq :one
SELECT coalesce(max(seq), 0)::int FROM task_events WHERE task_id = @task_id;
