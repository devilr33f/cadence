-- name: CreateHermesRun :exec
INSERT INTO hermes_runs (session_id, task_id, run_id, generation, tried, phase)
VALUES (@session_id, @task_id, @run_id, @generation, @generation, 'active');

-- name: GetHermesRun :one
SELECT * FROM hermes_runs WHERE session_id = @session_id;

-- name: SaveHermesRun :execrows
UPDATE hermes_runs SET
    run_id = @run_id,
    generation = @generation,
    tried = @tried,
    phase = @phase,
    control = sqlc.narg(control),
    shown_at = sqlc.narg(shown_at),
    updated_at = now()
WHERE session_id = @session_id;
