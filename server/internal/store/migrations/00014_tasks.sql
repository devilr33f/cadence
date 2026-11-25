-- +goose Up
CREATE SEQUENCE task_short_id_seq START 1000;

CREATE TABLE tasks (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    short_id         text        NOT NULL UNIQUE,
    kind             text        NOT NULL CHECK (char_length(kind) BETWEEN 1 AND 64),
    title            text        NOT NULL CHECK (char_length(title) BETWEEN 1 AND 120),
    origin_device_id uuid        REFERENCES devices ON DELETE SET NULL,
    status           text        NOT NULL CHECK (status IN ('queued', 'running', 'waiting_input', 'done', 'failed', 'cancelled', 'unknown')),
    progress         text        NOT NULL DEFAULT '' CHECK (char_length(progress) <= 200),
    question         jsonb       CHECK (question IS NULL OR (
        jsonb_typeof(question) = 'object'
        AND question->>'kind' IN ('clarify', 'approval')
        AND char_length(question->>'text') BETWEEN 1 AND 1000
    )),
    result           text        CHECK (result IS NULL OR octet_length(result) <= 65536),
    error_code       text        CHECK (error_code IS NULL OR error_code ~ '^[a-z0-9_]{1,64}$'),
    external_ref     text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    finished_at      timestamptz,
    reported_at      timestamptz
);

CREATE INDEX tasks_status ON tasks (status);
CREATE INDEX tasks_kind_ref ON tasks (kind, external_ref) WHERE external_ref IS NOT NULL;
CREATE INDEX tasks_finished_at ON tasks (finished_at) WHERE finished_at IS NOT NULL;

CREATE TABLE task_events (
    task_id uuid        NOT NULL REFERENCES tasks ON DELETE CASCADE,
    seq     int         NOT NULL,
    kind    text        NOT NULL CHECK (kind IN ('progress', 'waiting_input', 'done', 'failed', 'cancelled', 'unknown')),
    at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, seq)
);

-- +goose Down
DROP TABLE task_events;
DROP TABLE tasks;
DROP SEQUENCE task_short_id_seq;
