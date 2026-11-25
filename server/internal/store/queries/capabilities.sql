-- name: DeviceByID :one
SELECT id, name, model, status, last_seen_at, room, daemon_version FROM devices WHERE id = $1;

-- name: GetCapabilitiesHash :one
SELECT capabilities_hash FROM devices WHERE id = $1;

-- name: SetCapabilitiesHash :exec
UPDATE devices SET capabilities_hash = $2 WHERE id = $1;

-- name: UpsertCapability :exec
INSERT INTO device_capabilities (device_id, capability, version, descriptor)
VALUES ($1, $2, $3, $4)
ON CONFLICT (device_id, capability)
DO UPDATE SET version = EXCLUDED.version, descriptor = EXCLUDED.descriptor, updated_at = now();

-- name: DeleteCapabilitiesNotIn :exec
DELETE FROM device_capabilities WHERE device_id = @device_id AND capability <> ALL(@keep::text[]);

-- name: ListCapabilities :many
SELECT capability, version, descriptor, desired, desired_gen, applied_gen, applied_error, state, available
FROM device_capabilities WHERE device_id = $1 ORDER BY capability;

-- name: GetCapability :one
SELECT capability, version, descriptor, desired, desired_gen, applied_gen, applied_error, state, available
FROM device_capabilities WHERE device_id = $1 AND capability = $2;

-- name: SetDesiredConfig :one
UPDATE device_capabilities SET desired = @desired, desired_gen = desired_gen + 1, updated_at = now()
WHERE device_id = @device_id AND capability = @capability
RETURNING desired_gen;

-- name: PendingConfigs :many
SELECT capability, desired, desired_gen FROM device_capabilities
WHERE device_id = $1 AND desired_gen > applied_gen ORDER BY capability;

-- name: SetApplied :execrows
UPDATE device_capabilities SET applied_gen = @generation, applied_error = @applied_error, updated_at = now()
WHERE device_id = @device_id AND capability = @capability
  AND @generation <= desired_gen AND @generation > applied_gen;

-- name: SetCapabilityState :execrows
UPDATE device_capabilities SET state = @state, available = @available, updated_at = now()
WHERE device_id = @device_id AND capability = @capability;

-- name: SetCapabilityAvailable :execrows
UPDATE device_capabilities SET available = @available, updated_at = now()
WHERE device_id = @device_id AND capability = @capability;
