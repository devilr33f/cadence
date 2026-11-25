-- +goose Up
-- the sounds a device's rings stream at ring time (GET /v2/ring-sound/{id}): an alarm's music, transcoded once when
-- it was set, and a reminder's speech. ogg opus at 48 khz. title is a user setting kept with its alarm or reminder
-- (the music's title, the reminder's text): never a stream or signed url. source_key is a sha-256 of what made it, so
-- the same request reuses one. a sound no alarm or reminder of its device names is deleted 7 days after it last did
CREATE TABLE ring_sounds (
    id           text        PRIMARY KEY CHECK (id ~ '^[a-z0-9]{16}$'),
    device_id    uuid        NOT NULL REFERENCES devices ON DELETE CASCADE,
    kind         text        NOT NULL CHECK (kind IN ('music', 'speech')),
    source_key   text        NOT NULL CHECK (source_key ~ '^[0-9a-f]{64}$'),
    sha256       text        NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    audio        bytea       NOT NULL,
    size         integer     NOT NULL CHECK (size > 0),
    duration_ms  integer     NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    title        text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX ring_sounds_source ON ring_sounds (device_id, kind, source_key);
CREATE INDEX ring_sounds_last_used ON ring_sounds (last_used_at);

-- +goose Down
DROP TABLE ring_sounds;
