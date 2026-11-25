-- name: CountOpenClaims :one
-- the open claims, all and the source's
SELECT count(*) AS total, count(*) FILTER (WHERE source = @source) AS from_source
FROM device_claims WHERE state = 'pending' AND expires_at > @now;

-- name: CreateClaim :exec
INSERT INTO device_claims (id_hash, code, board, source, created_at, expires_at)
VALUES (@id_hash, @code, @board, @source, @created_at, @expires_at);

-- name: ClaimByID :one
SELECT code, board, state, device_id, expires_at FROM device_claims WHERE id_hash = $1;

-- name: ListOpenClaims :many
SELECT code, board, source, created_at, expires_at FROM device_claims
WHERE state = 'pending' AND expires_at > @now ORDER BY created_at;

-- name: OpenClaimByCode :one
-- locked, so an approve and a decline of one code take turns
SELECT id_hash, board FROM device_claims
WHERE code = @code AND state = 'pending' AND expires_at > @now
FOR UPDATE;

-- name: ApproveClaim :exec
UPDATE device_claims SET state = 'approved', device_id = @device_id, finished_at = @now WHERE id_hash = @id_hash;

-- name: DeclineClaim :exec
UPDATE device_claims SET state = 'declined', finished_at = @now WHERE id_hash = @id_hash;

-- name: TakeClaim :one
-- the screen's poll after the approval: the claim ends and its device is
-- enrolled with the token made for this poll, once
WITH taken AS (
    UPDATE device_claims SET state = 'done' WHERE id_hash = @id_hash AND state = 'approved' AND device_id IS NOT NULL
    RETURNING device_id
)
UPDATE devices SET token_hash = @token_hash, status = 'enrolled', enrolled_at = @now
FROM taken WHERE devices.id = taken.device_id AND devices.status = 'pending'
RETURNING devices.id, devices.name, devices.model;

-- name: PruneClaims :execrows
DELETE FROM device_claims WHERE (state <> 'pending' AND finished_at < @before) OR expires_at < @before;
