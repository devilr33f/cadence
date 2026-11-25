-- +goose Up
-- a member's trim to the microsecond: an acoustic calibration measures each member's output latency finer than a
-- millisecond, and a stereo pair is held to 100 us. trim_ms stays, trim_us rounded, for what reads milliseconds
ALTER TABLE group_members
    ADD COLUMN trim_us int NOT NULL DEFAULT 0 CHECK (trim_us BETWEEN -500000 AND 500000);
UPDATE group_members SET trim_us = trim_ms * 1000;

-- +goose Down
ALTER TABLE group_members DROP COLUMN trim_us;
