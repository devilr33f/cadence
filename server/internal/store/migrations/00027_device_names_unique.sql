-- +goose Up
-- a device's name is no other device's, in any case (M7bs-D4): the index holds
-- for creates and renames alike, and makes a racing rename wait and fail.
-- names that already clash in case are renamed first (ruling M7bs-R7): the
-- oldest of each keeps its name, by created_at then id, and each later one
-- takes " 2", " 3"… cut to fit 64 characters, the first such name no device
-- has in any case
-- +goose StatementBegin
DO $$
DECLARE
    twin record;
    k int;
    candidate text;
BEGIN
    FOR twin IN
        SELECT id, name FROM (
            SELECT id, name, row_number() OVER (PARTITION BY lower(name) ORDER BY created_at, id) AS n
            FROM devices
        ) d
        WHERE n > 1
        ORDER BY lower(name), n
    LOOP
        k := 2;
        LOOP
            candidate := left(twin.name, 64 - length(' ' || k)) || ' ' || k;
            EXIT WHEN NOT EXISTS (SELECT 1 FROM devices WHERE lower(name) = lower(candidate));
            k := k + 1;
        END LOOP;
        UPDATE devices SET name = candidate WHERE id = twin.id;
    END LOOP;
END $$;
-- +goose StatementEnd

CREATE UNIQUE INDEX devices_name_lower ON devices (lower(name));

-- +goose Down
-- the renamed names stay as they are
DROP INDEX devices_name_lower;
