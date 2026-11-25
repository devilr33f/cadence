-- name: InsertDeviceLogs :many
-- only the lines of a device that still exists are stored, the server's own
-- (no device) always: its row, locked for key share, makes an insert that races
-- the device's delete wait, then store none of its lines (M7bs-D4); r is read once,
-- so it streams. answers the devices whose lines were stored
WITH r AS (
    SELECT unnest(@ts::timestamptz[]) AS ts, unnest(@observed_ts::timestamptz[]) AS observed_ts,
           unnest(@device_id::uuid[]) AS device_id, unnest(@source::text[]) AS source,
           unnest(@severity::smallint[]) AS severity, unnest(@body::text[]) AS body,
           unnest(@truncated::boolean[]) AS truncated, unnest(@continued::boolean[]) AS continued
), live AS (
    SELECT d.id FROM devices d WHERE d.id = ANY(@device_id::uuid[]) FOR KEY SHARE
), stored AS (
    INSERT INTO device_logs (ts, observed_ts, device_id, source, severity, body, truncated, continued)
    SELECT r.ts, r.observed_ts, r.device_id, r.source, r.severity, r.body, r.truncated, r.continued FROM r
    WHERE r.device_id IS NULL OR r.device_id IN (SELECT live.id FROM live)
)
SELECT live.id FROM live;

-- name: EnsureDeviceLogPartition :exec
SELECT device_logs_ensure_partition(@day::date);

-- name: DropDeviceLogPartitions :one
SELECT device_logs_drop_partitions(@before::date)::integer AS dropped;

-- name: InsertMetricSamples :many
-- only the points of a device that still exists are stored, as InsertDeviceLogs
-- does; answers the devices whose points were stored
WITH r AS (
    SELECT unnest(@device_id::uuid[]) AS device_id, unnest(@name::text[]) AS name, unnest(@kind::smallint[]) AS kind,
           unnest(@attrs::text[]) AS attrs, unnest(@ts::timestamptz[]) AS ts, unnest(@start_ts::timestamptz[]) AS start_ts,
           unnest(@value::double precision[]) AS value
), live AS (
    SELECT d.id FROM devices d WHERE d.id = ANY(@device_id::uuid[]) FOR KEY SHARE
), stored AS (
    INSERT INTO metric_samples (device_id, name, kind, attrs, ts, start_ts, value)
    SELECT r.device_id, r.name, r.kind, r.attrs::jsonb, r.ts, r.start_ts, r.value FROM r
    WHERE r.device_id IN (SELECT live.id FROM live)
)
SELECT live.id FROM live;

-- name: PruneMetricSamples :execrows
DELETE FROM metric_samples WHERE ts < @before;

-- name: ListDeviceLogs :many
SELECT l.ts, l.observed_ts, l.device_id, d.name AS device_name, d.model AS board, l.source, l.severity, l.body,
       l.truncated, l.continued
FROM device_logs l LEFT JOIN devices d ON d.id = l.device_id
WHERE l.ts >= @since AND l.ts < @until AND l.severity >= @min_severity
  AND (sqlc.narg(device_id)::uuid IS NULL OR l.device_id = sqlc.narg(device_id))
  AND (sqlc.narg(source)::text IS NULL OR l.source = sqlc.narg(source))
  AND (sqlc.narg(pattern)::text IS NULL OR l.body ILIKE sqlc.narg(pattern))
ORDER BY l.ts DESC
LIMIT @max_rows;

-- name: LatestMetricSeries :many
WITH latest AS (
    SELECT DISTINCT ON (s.device_id, s.name, s.attrs) s.device_id, s.name, s.kind, s.attrs, s.ts, s.value
    FROM metric_samples s WHERE s.ts >= @since
    ORDER BY s.device_id, s.name, s.attrs, s.ts DESC
), ranked AS (
    SELECT l.device_id, l.name, l.kind, l.attrs, l.ts, l.value,
           row_number() OVER (PARTITION BY l.device_id ORDER BY l.name, l.attrs) AS n
    FROM latest l
)
SELECT r.device_id, d.name AS device_name, d.model AS board, r.name, r.kind, r.attrs, r.ts, r.value
FROM ranked r JOIN devices d ON d.id = r.device_id
WHERE r.n <= @per_device::bigint
ORDER BY r.device_id, r.name, r.attrs;

-- name: DeviceMetricPoints :many
SELECT name, kind, attrs, ts, start_ts, value FROM metric_samples
WHERE device_id = @device_id AND name = ANY(@names::text[]) AND ts >= @since
ORDER BY name, attrs, ts
LIMIT @max_rows;
