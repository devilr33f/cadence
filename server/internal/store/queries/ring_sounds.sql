-- name: PutRingSound :one
-- a sound made again from the same source keeps its id and takes the new audio
INSERT INTO ring_sounds (id, device_id, kind, source_key, sha256, audio, size, duration_ms, title, created_at, last_used_at)
VALUES (@id, @device_id, @kind, @source_key, @sha256, @audio, @size, @duration_ms, @title, @now, @now)
ON CONFLICT (device_id, kind, source_key) DO UPDATE
SET sha256 = excluded.sha256, audio = excluded.audio, size = excluded.size, duration_ms = excluded.duration_ms,
    title = excluded.title, last_used_at = excluded.last_used_at
RETURNING id;

-- name: RingSoundBySource :one
SELECT id, title FROM ring_sounds WHERE device_id = @device_id AND kind = @kind AND source_key = @source_key;

-- name: GetRingSound :one
-- only the device the sound was made for may fetch it
SELECT sha256, audio, size, created_at FROM ring_sounds WHERE id = @id AND device_id = @device_id;

-- name: RingSoundInfo :many
SELECT id, kind, title, size, duration_ms, created_at, last_used_at
FROM ring_sounds WHERE device_id = @device_id AND id = ANY(@ids::text[]);

-- name: ListRingSounds :many
SELECT id, device_id, kind, title, size, duration_ms, created_at, last_used_at
FROM ring_sounds ORDER BY last_used_at DESC, id;

-- name: TouchRingSounds :exec
UPDATE ring_sounds SET last_used_at = @now
WHERE device_id = @device_id AND id = ANY(@ids::text[]) AND last_used_at < @now;

-- name: DeleteStaleRingSounds :execrows
DELETE FROM ring_sounds WHERE last_used_at < @before;
