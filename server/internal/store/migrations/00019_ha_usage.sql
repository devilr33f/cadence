-- +goose Up
-- how much each device uses each home assistant entity (M5c-D8): a score that
-- halves every 14 days and grows by one on each successful control; entity
-- ids only, never names or states
CREATE TABLE ha_usage (
    device_id  uuid             NOT NULL REFERENCES devices ON DELETE CASCADE,
    entity_id  text             NOT NULL CHECK (char_length(entity_id) <= 128 AND entity_id ~ '^[a-z][a-z0-9_]*\.[a-z0-9_]+$'),
    score      double precision NOT NULL,
    updated_at timestamptz      NOT NULL,
    PRIMARY KEY (device_id, entity_id)
);

-- +goose Down
DROP TABLE ha_usage;
