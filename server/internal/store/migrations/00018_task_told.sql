-- +goose Up
-- which question, by its token, a device's user was told (Ruling M5b-R9): its
-- notice was spoken there, or a turn there (not an event turn) read it, in
-- turn_id; a voice approval answers only a question told before its own turn.
-- the first telling stays
ALTER TABLE task_notices ADD COLUMN token text NOT NULL DEFAULT '' CHECK (token ~ '^[A-Za-z0-9_-]{0,64}$');

CREATE TABLE task_told (
    task_id   uuid        NOT NULL REFERENCES tasks ON DELETE CASCADE,
    token     text        NOT NULL CHECK (token ~ '^[A-Za-z0-9_-]{1,64}$'),
    device_id uuid        NOT NULL REFERENCES devices ON DELETE CASCADE,
    turn_id   text        NOT NULL DEFAULT '' CHECK (char_length(turn_id) <= 64),
    at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, token, device_id)
);

-- +goose Down
DROP TABLE task_told;
ALTER TABLE task_notices DROP COLUMN token;
