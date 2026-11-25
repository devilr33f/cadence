-- name: RecordHAUsage :exec
-- the score decays to now with a 14-day half-life, then counts this use; a
-- clock stepped back never grows it
INSERT INTO ha_usage (device_id, entity_id, score, updated_at)
VALUES (@device_id, @entity_id, 1, now())
ON CONFLICT (device_id, entity_id) DO UPDATE
SET score = ha_usage.score * power(0.5::float8, greatest(0, extract(epoch FROM now() - ha_usage.updated_at))::float8 / (14 * 86400)) + 1,
    updated_at = now();

-- name: TopHAUsage :many
-- the device's entities by their score decayed to now
SELECT entity_id, (score * power(0.5::float8, greatest(0, extract(epoch FROM now() - updated_at))::float8 / (14 * 86400)))::float8 AS decayed
FROM ha_usage
WHERE device_id = @device_id
ORDER BY decayed DESC, entity_id
LIMIT @n;

-- name: HouseholdHAUsage :many
-- the entities by their scores decayed to now, summed over the devices
SELECT entity_id, sum(score * power(0.5::float8, greatest(0, extract(epoch FROM now() - updated_at))::float8 / (14 * 86400)))::float8 AS decayed
FROM ha_usage
GROUP BY entity_id
ORDER BY decayed DESC, entity_id
LIMIT @n;
