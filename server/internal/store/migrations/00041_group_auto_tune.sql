-- +goose Up
-- a group's leader tunes its stream itself (multiroom auto-tune): latency_ms is where its latency starts, bitrate_kbps
-- and wait_ms the ceilings its bitrate and start wait move under. on by default, for the groups made before too
ALTER TABLE groups
    ADD COLUMN auto_latency boolean NOT NULL DEFAULT true,
    ADD COLUMN auto_bitrate boolean NOT NULL DEFAULT true,
    ADD COLUMN auto_wait    boolean NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE groups DROP COLUMN auto_latency, DROP COLUMN auto_bitrate, DROP COLUMN auto_wait;
