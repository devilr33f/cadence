-- +goose Up
CREATE TABLE memories (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    scope       text        NOT NULL CHECK (scope IN ('global', 'device')),
    device_id   uuid        REFERENCES devices ON DELETE CASCADE,
    kind        text        NOT NULL CHECK (kind IN ('preference', 'fact', 'alias', 'lesson')),
    text        text        NOT NULL CHECK (char_length(text) BETWEEN 1 AND 500),
    embedding   vector,
    embed_model text,
    pinned      boolean     NOT NULL DEFAULT false,
    hits        int         NOT NULL DEFAULT 0,
    last_hit_at timestamptz,
    confidence  real        NOT NULL CHECK (confidence BETWEEN 0 AND 1),
    evidence    bigint[]    NOT NULL DEFAULT '{}',
    source      text        NOT NULL CHECK (source IN ('learned', 'manual')),
    status      text        NOT NULL CHECK (status IN ('active', 'pending')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CHECK ((scope = 'global') = (device_id IS NULL)),
    CHECK ((embedding IS NULL) = (embed_model IS NULL))
);

CREATE INDEX memories_scope_device_status ON memories (scope, device_id, status);

CREATE TABLE conversation_compactions (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id       uuid        NOT NULL REFERENCES devices ON DELETE CASCADE,
    upto_message_id bigint      NOT NULL,
    summary         text        NOT NULL,
    open_threads    jsonb       NOT NULL DEFAULT '[]',
    task_refs       jsonb       NOT NULL DEFAULT '[]',
    model           text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX conversation_compactions_device_created ON conversation_compactions (device_id, created_at DESC);

CREATE TABLE memory_learning_runs (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    compaction_id uuid        NOT NULL REFERENCES conversation_compactions ON DELETE CASCADE,
    device_id     uuid        NOT NULL,
    applied       int         NOT NULL,
    dropped       int         NOT NULL,
    pending       int         NOT NULL,
    memory_ids    uuid[]      NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX memory_learning_runs_device_created ON memory_learning_runs (device_id, created_at DESC);

-- +goose Down
DROP TABLE memory_learning_runs;
DROP TABLE conversation_compactions;
DROP TABLE memories;
