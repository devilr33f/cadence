-- +goose Up
-- the owner's recently-played history, kept for the same 14 days as transcripts.
-- artwork_url is a thumbnail's public url only: stream and signed urls have no
-- column here. airplay keeps its uploaded artwork's hash instead.
CREATE TABLE play_history (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    device_id    uuid        NOT NULL REFERENCES devices ON DELETE CASCADE,
    source       text        NOT NULL,
    provider     text        NOT NULL DEFAULT '',
    kind         text        NOT NULL CHECK (kind IN ('track', 'video', 'episode', 'stream')),
    title        text        NOT NULL DEFAULT '',
    artist       text        NOT NULL DEFAULT '',
    album        text        NOT NULL DEFAULT '',
    artwork_url  text        NOT NULL DEFAULT '',
    artwork_hash text        NOT NULL DEFAULT '' CHECK (artwork_hash = '' OR artwork_hash ~ '^[0-9a-f]{64}$'),
    catalog_id   text        NOT NULL DEFAULT '',
    turn_id      text        NOT NULL DEFAULT '',
    dedupe_key   text        NOT NULL,
    started_at   timestamptz NOT NULL DEFAULT now(),
    ended_at     timestamptz,
    played_ms    bigint      NOT NULL DEFAULT 0 CHECK (played_ms >= 0)
);

CREATE INDEX play_history_started ON play_history (started_at DESC, id DESC);
CREATE INDEX play_history_device_started ON play_history (device_id, started_at DESC, id DESC);
CREATE INDEX play_history_dedupe ON play_history (device_id, dedupe_key, started_at DESC, id DESC);

-- +goose Down
DROP TABLE play_history;
