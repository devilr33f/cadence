-- +goose Up
-- the hermes kind's own state per task: the session is the task's external_ref,
-- the run moves on when a paused task resumes in a new run
CREATE TABLE hermes_runs (
    session_id text        PRIMARY KEY CHECK (char_length(session_id) BETWEEN 1 AND 256),
    task_id    uuid        NOT NULL UNIQUE REFERENCES tasks ON DELETE CASCADE,
    run_id     text        NOT NULL CHECK (char_length(run_id) BETWEEN 1 AND 256),
    generation int         NOT NULL CHECK (generation >= 1),
    -- the highest generation submitted: a failed resume's key is never reused
    tried      int         NOT NULL CHECK (tried >= generation),
    phase      text        NOT NULL CHECK (phase IN ('active', 'paused', 'closed')),
    control    jsonb       CHECK (control IS NULL OR jsonb_typeof(control) = 'object'),
    shown_at   timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE hermes_runs;
