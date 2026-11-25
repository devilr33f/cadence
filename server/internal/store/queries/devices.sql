-- name: CreateDevice :one
INSERT INTO devices (name, model, status) VALUES ($1, $2, 'pending')
RETURNING id, name, model, status, created_at;

-- name: CreateEnrollCode :exec
INSERT INTO enroll_codes (code_hash, device_id, model, expires_at) VALUES ($1, $2, $3, $4);

-- name: ClaimEnrollCode :one
WITH claimed AS (
    UPDATE enroll_codes SET used_at = @now
    WHERE code_hash = @code_hash AND used_at IS NULL AND expires_at > @now AND enroll_codes.model = @model
    RETURNING device_id
)
UPDATE devices SET token_hash = @token_hash, status = 'enrolled', enrolled_at = @now
FROM claimed WHERE devices.id = claimed.device_id
RETURNING devices.id, devices.name, devices.model;

-- name: DeviceByTokenHash :one
SELECT id FROM devices WHERE token_hash = $1 AND status = 'enrolled';

-- name: ListDevices :many
SELECT id, name, model, status, last_seen_at, room FROM devices ORDER BY created_at, id;

-- name: UpdateLastSeen :one
-- its name and model are what the link's presence events carry
UPDATE devices SET last_seen_at = @seen_at WHERE id = @id
RETURNING name, model;

-- name: RenameDevice :one
-- another device's name, in any case, fails on devices_name_lower
UPDATE devices SET name = @name WHERE id = @id
RETURNING model;

-- name: DeleteDevice :execrows
-- its token, enrol codes, capabilities, secrets, memories, conversation, compactions, task notices and home
-- assistant usage go with it (ON DELETE CASCADE); its log lines and metric points stay until their retention
DELETE FROM devices WHERE id = $1;

-- name: DeletePendingDevice :execrows
-- DeleteDevice for a device that is still pending: an enrolment that commits
-- while this waits for the row's lock leaves it, as the row is read again
DELETE FROM devices WHERE id = $1 AND status = 'pending';

-- name: SetDeviceRoom :execrows
UPDATE devices SET room = @room WHERE id = @id;

-- name: DeviceRoom :one
SELECT room FROM devices WHERE id = $1;
