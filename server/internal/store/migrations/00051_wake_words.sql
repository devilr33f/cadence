-- +goose Up
-- the custom wake words (wake.custom): each a model pkg/kws runs on a device, uploaded ready or trained here from the
-- owner's takes. the model holds no audio. a training's dataset (the takes, a room and a speech recording) lives in
-- the server's memory for the training alone, and is stored here only when the owner asked to keep it (for a retrain),
-- until they delete it; it is never logged (rule 8)
CREATE TABLE wake_words (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- the owner's word, a label: never matched against speech
    word           text        NOT NULL CHECK (char_length(word) BETWEEN 1 AND 64),
    source         text        NOT NULL CHECK (source IN ('upload', 'trained')),
    -- training (a job runs), ready (model set), failed (error_code says why)
    status         text        NOT NULL CHECK (status IN ('training', 'ready', 'failed')),
    error_code     text        NOT NULL DEFAULT '',
    model          bytea,
    sha256         text        CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    threshold      real,
    takes          integer,
    recall         double precision,
    false_per_hour double precision,
    eval_hours     double precision,
    dataset        bytea,
    created_at     timestamptz NOT NULL DEFAULT now(),
    trained_at     timestamptz,
    CHECK (status <> 'ready' OR (model IS NOT NULL AND sha256 IS NOT NULL))
);

CREATE INDEX wake_words_created ON wake_words (created_at DESC);

-- +goose Down
DROP TABLE wake_words;
