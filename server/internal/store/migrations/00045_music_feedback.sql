-- +goose Up
-- durable household song feedback lives apart from the 14-day play rows.
ALTER TABLE play_history
    ADD COLUMN provider_song_id  text NOT NULL DEFAULT '',
    ADD COLUMN web_url           text NOT NULL DEFAULT '',
    ADD COLUMN provider_instance text NOT NULL DEFAULT '';

CREATE TABLE music_song_state (
    provider         text        NOT NULL CHECK (provider <> ''),
    provider_song_id text        NOT NULL CHECK (provider_song_id <> ''),
    instance         text        NOT NULL DEFAULT '',
    video_id         text        NOT NULL DEFAULT '',
    title            text        NOT NULL DEFAULT '',
    artist           text        NOT NULL DEFAULT '',
    album            text        NOT NULL DEFAULT '',
    duration_s       integer     NOT NULL DEFAULT 0 CHECK (duration_s >= 0),
    artwork_url      text        NOT NULL DEFAULT '',
    web_url          text        NOT NULL DEFAULT '',
    feedback         text        NOT NULL CHECK (feedback IN ('like', 'dislike')),
    favorited_at     timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, provider_song_id),
    CHECK ((feedback = 'like' AND favorited_at IS NOT NULL) OR (feedback = 'dislike' AND favorited_at IS NULL))
);

CREATE INDEX music_song_state_favorites
    ON music_song_state (favorited_at DESC, provider, provider_song_id)
    WHERE favorited_at IS NOT NULL;

CREATE INDEX play_history_song
    ON play_history (provider, provider_song_id, started_at DESC, id DESC)
    WHERE provider_song_id <> '';

-- +goose Down
DROP TABLE music_song_state;
ALTER TABLE play_history
    DROP COLUMN provider_song_id,
    DROP COLUMN web_url,
    DROP COLUMN provider_instance;
