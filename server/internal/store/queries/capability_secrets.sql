-- name: UpsertCapabilitySecret :exec
INSERT INTO device_capability_secrets (device_id, capability, name, sealed)
VALUES (@device_id, @capability, @name, @sealed)
ON CONFLICT (device_id, capability, name)
DO UPDATE SET sealed = EXCLUDED.sealed, updated_at = now();

-- name: DeleteCapabilitySecret :exec
DELETE FROM device_capability_secrets WHERE device_id = $1 AND capability = $2 AND name = $3;

-- name: ListCapabilitySecrets :many
SELECT name, sealed FROM device_capability_secrets WHERE device_id = $1 AND capability = $2 ORDER BY name;

-- name: ListDeviceSecretNames :many
SELECT capability, name FROM device_capability_secrets WHERE device_id = $1 ORDER BY capability, name;

-- name: BumpDesiredGen :one
UPDATE device_capabilities SET desired_gen = desired_gen + 1, updated_at = now()
WHERE device_id = @device_id AND capability = @capability
RETURNING desired, desired_gen;

-- name: ListDeviceSecrets :many
SELECT capability, name, sealed FROM device_capability_secrets WHERE device_id = $1 ORDER BY capability, name;
