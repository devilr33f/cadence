-- +goose Up
-- a capability whose hardware is absent right now (a screen's hdmi sink unplugged) stays reported and configured; the
-- device says so on its state deltas (StateDelta.unavailable), and the server hides its state meanwhile
ALTER TABLE device_capabilities ADD COLUMN available boolean NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE device_capabilities DROP COLUMN available;
