-- name: PutBusySignal :one
-- a source's signal replaces the one it had
INSERT INTO busy_signals (source, reason, devices, expires_at, updated_at)
VALUES (@source, @reason, sqlc.narg(devices)::uuid[], @expires_at, @updated_at)
ON CONFLICT (source) DO UPDATE
SET reason = EXCLUDED.reason, devices = EXCLUDED.devices, expires_at = EXCLUDED.expires_at, updated_at = EXCLUDED.updated_at
RETURNING source, reason, devices, expires_at, updated_at;

-- name: DeleteBusySignal :execrows
DELETE FROM busy_signals WHERE source = @source;

-- name: DeleteExpiredBusySignals :execrows
DELETE FROM busy_signals WHERE expires_at <= @now;

-- name: ListBusySignals :many
-- the signals still in force at now
SELECT source, reason, devices, expires_at, updated_at FROM busy_signals
WHERE expires_at > @now ORDER BY source;

-- name: BusyDeviceIDs :many
-- every enrolled device, whose busy state an all-devices signal changes
SELECT id FROM devices WHERE status = 'enrolled' ORDER BY id;
