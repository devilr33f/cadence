-- +goose Up
-- every device's metric points, M6b-D9: kind 1 is a gauge, stored as sent
-- (a cumulative sum that is not monotonic is one too); kind 2 a monotonic
-- cumulative sum, stored as its running value from start_ts, so a reset shows
-- as a decrease. start_ts may lie past ts: a device's clock can be set after it
-- started. Kept 24 h by an hourly prune.
CREATE TABLE metric_samples (
    device_id uuid             NOT NULL REFERENCES devices ON DELETE CASCADE,
    name      text             NOT NULL,
    kind      smallint         NOT NULL CHECK (kind IN (1, 2)),
    attrs     jsonb            NOT NULL DEFAULT '{}',
    ts        timestamptz      NOT NULL,
    start_ts  timestamptz,
    value     double precision NOT NULL
);

CREATE INDEX metric_samples_series ON metric_samples (device_id, name, ts DESC);
-- the prune's range: rows arrive in ts order, so a block range index stays small
CREATE INDEX metric_samples_ts ON metric_samples USING brin (ts);

-- +goose Down
DROP TABLE metric_samples;
