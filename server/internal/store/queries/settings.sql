-- name: GetSettings :one
SELECT data FROM settings WHERE scope = $1;

-- name: PutSettings :exec
INSERT INTO settings (scope, data) VALUES ($1, $2)
ON CONFLICT (scope) DO UPDATE SET data = EXCLUDED.data;

-- name: PatchSettings :one
-- a device's scope is its id. its row, locked for key share, makes a write
-- that races the device's delete wait, then find no device and store nothing
WITH d AS (SELECT id FROM devices WHERE id = @device_id FOR KEY SHARE)
INSERT INTO settings (scope, data) SELECT d.id::text, @patch::jsonb FROM d
ON CONFLICT (scope) DO UPDATE SET data = (settings.data || EXCLUDED.data) - @drop::text[]
RETURNING data;

-- name: DeleteSettings :exec
DELETE FROM settings WHERE scope = $1;

-- name: ListSettings :many
SELECT scope, data FROM settings;
