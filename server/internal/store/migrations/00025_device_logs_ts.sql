-- +goose Up
-- the admin API reads the newest lines first, filtered or not (M6b-D10): on
-- every day partition, so a read walks the newest partition's index first
CREATE INDEX device_logs_ts ON device_logs (ts DESC);

-- +goose Down
DROP INDEX device_logs_ts;
