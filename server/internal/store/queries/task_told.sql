-- name: AddTaskTold :exec
-- the first telling of a question on a device stays
INSERT INTO task_told (task_id, token, device_id, turn_id)
VALUES (@task_id, @token, @device_id, @turn_id)
ON CONFLICT DO NOTHING;

-- name: TaskToldBefore :one
-- the question was told on the device outside the turn turn_id names
SELECT EXISTS (
    SELECT 1 FROM task_told k JOIN tasks t ON t.id = k.task_id
    WHERE t.short_id = @short_id AND k.token = @token AND k.device_id = @device_id AND k.turn_id <> @turn_id
);
