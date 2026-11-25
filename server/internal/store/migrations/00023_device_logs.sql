-- +goose Up
-- every device's log lines and the server's own (device_id null), M6b-D9: one
-- partition per UTC day of ts, made ahead by the maintenance loop and on demand
-- by an insert, dropped past the logRetentionDays setting. ts is the record's
-- time, or the receive time when that is more than a day off it.
CREATE TABLE device_logs (
    ts          timestamptz NOT NULL,
    observed_ts timestamptz NOT NULL,
    device_id   uuid REFERENCES devices ON DELETE CASCADE,
    source      text        NOT NULL,
    severity    smallint    NOT NULL,
    body        text        NOT NULL,
    truncated   boolean     NOT NULL DEFAULT false,
    continued   boolean     NOT NULL DEFAULT false
) PARTITION BY RANGE (ts);

CREATE INDEX device_logs_device_ts ON device_logs (device_id, ts DESC);
CREATE INDEX device_logs_source_ts ON device_logs (source, ts DESC);

-- one creator or dropper at a time: two creates of one name collide on its row
-- type. a lock not had in 2 s fails the call (its device sends the lines again)
-- rather than queue every insert behind a stuck one; the timeout is set for the
-- call alone, where a SET LOCAL would last to the end of the caller's transaction
-- +goose StatementBegin
CREATE FUNCTION device_logs_ensure_partition(day date) RETURNS void
SET lock_timeout = '2s'
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext('device_logs_partitions'));
    EXECUTE format('CREATE TABLE IF NOT EXISTS %I PARTITION OF device_logs FOR VALUES FROM (%L) TO (%L)',
        'device_logs_' || to_char(day, 'YYYYMMDD'),
        to_char(day, 'YYYY-MM-DD') || ' 00:00:00+00',
        to_char(day + 1, 'YYYY-MM-DD') || ' 00:00:00+00');
END
$$;
-- +goose StatementEnd

-- drops every day partition before the given day, returning how many
-- +goose StatementBegin
CREATE FUNCTION device_logs_drop_partitions(before date) RETURNS integer
SET lock_timeout = '2s'
LANGUAGE plpgsql AS $$
DECLARE
    part    text;
    dropped integer := 0;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext('device_logs_partitions'));
    FOR part IN
        SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'device_logs'::regclass AND c.relname ~ '^device_logs_[0-9]{8}$'
          AND to_date(right(c.relname, 8), 'YYYYMMDD') < before
        ORDER BY c.relname
    LOOP
        EXECUTE format('DROP TABLE %I', part);
        dropped := dropped + 1;
    END LOOP;
    RETURN dropped;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION device_logs_drop_partitions(date);
DROP FUNCTION device_logs_ensure_partition(date);
DROP TABLE device_logs;
