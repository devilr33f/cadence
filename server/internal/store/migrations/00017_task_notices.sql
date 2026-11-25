-- +goose Up
-- what an event turn said about one event of a task, waiting until its device
-- is online, idle and out of quiet hours; at most one undelivered per task
CREATE TABLE task_notices (
    task_id      uuid        NOT NULL REFERENCES tasks ON DELETE CASCADE,
    event_seq    int         NOT NULL,
    device_id    uuid        NOT NULL REFERENCES devices ON DELETE CASCADE,
    text         text        NOT NULL CHECK (char_length(text) BETWEEN 1 AND 1000),
    created_at   timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    PRIMARY KEY (task_id, event_seq)
);

CREATE UNIQUE INDEX task_notices_undelivered ON task_notices (task_id) WHERE delivered_at IS NULL;
CREATE INDEX task_notices_device ON task_notices (device_id, created_at) WHERE delivered_at IS NULL;

-- +goose Down
DROP TABLE task_notices;
