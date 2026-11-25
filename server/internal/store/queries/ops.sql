-- name: PutDeviceArtifact :one
INSERT INTO device_artifacts (id, board, kind, version, label, sha256, size, data, files, created_at)
VALUES (@id, @board, @kind, @version, @label, @sha256, @size, @data, @files, @now)
RETURNING id;

-- name: ListDeviceArtifacts :many
SELECT id, board, kind, version, label, sha256, size, files, created_at
FROM device_artifacts WHERE board = @board ORDER BY created_at DESC, id;

-- name: GetDeviceArtifact :one
SELECT id, board, kind, version, label, sha256, size, data, files, created_at
FROM device_artifacts WHERE id = @id;

-- name: DeleteDeviceArtifact :execrows
DELETE FROM device_artifacts WHERE id = @id;

-- name: PutOpsRun :one
INSERT INTO ops_runs (id, device_id, artifact_id, board, target_version, transport, plan, started_at)
VALUES (@id, @device_id, @artifact_id, @board, @target_version, @transport, @plan, @now)
RETURNING id, started_at;

-- name: GetOpsRun :one
SELECT id, device_id, artifact_id, board, target_version, transport, plan, status, error_code, steps, started_at, finished_at
FROM ops_runs WHERE id = @id;

-- name: ListOpsRuns :many
SELECT id, artifact_id, board, target_version, transport, status, error_code, steps, started_at, finished_at
FROM ops_runs WHERE device_id = @device_id ORDER BY started_at DESC LIMIT 25;

-- name: ActiveOpsRun :many
SELECT id, device_id, status FROM ops_runs WHERE status IN ('running', 'awaiting_restart');

-- name: SetOpsRunStatus :exec
-- a run still in flight (running, awaiting its restart's relink) keeps its finished_at
UPDATE ops_runs SET status = @status, error_code = @error_code WHERE id = @id;

-- name: FinishOpsRun :exec
UPDATE ops_runs SET status = @status, error_code = @error_code, finished_at = @now WHERE id = @id;

-- name: SetOpsRunSteps :exec
UPDATE ops_runs SET steps = @steps WHERE id = @id;

-- name: InterruptStaleOpsRuns :execrows
-- a server restart must not leave a server-driven run looking live: its executor is gone
UPDATE ops_runs SET status = 'failed', error_code = 'interrupted', finished_at = @now
WHERE status = 'running' AND transport IN ('adb', 'ssh') AND started_at < @before;

-- name: SetDeviceDaemonVersion :exec
UPDATE devices SET daemon_version = @version WHERE id = @id AND daemon_version <> @version;

-- name: PutDeviceAccess :exec
INSERT INTO device_access (device_id, kind, addr, ssh_key, ssh_host_key)
VALUES (@device_id, @kind, @addr, @ssh_key, @ssh_host_key)
ON CONFLICT (device_id, kind) DO UPDATE
SET addr = excluded.addr, ssh_key = excluded.ssh_key, ssh_host_key = excluded.ssh_host_key,
    last_probe = NULL, last_ok = NULL;

-- name: TouchDeviceAccess :exec
UPDATE device_access SET last_probe = @now, last_ok = @ok WHERE device_id = @device_id AND kind = @kind;

-- name: DeleteDeviceAccess :execrows
DELETE FROM device_access WHERE device_id = @device_id AND kind = @kind;

-- name: ListDeviceAccess :many
SELECT device_id, kind, addr, ssh_host_key, last_probe, last_ok
FROM device_access WHERE device_id = @device_id;

-- name: GetDeviceAccessKey :one
SELECT ssh_key FROM device_access WHERE device_id = @device_id AND kind = 'ssh';

-- name: SetDeviceAccessHostKey :exec
-- the host key an ssh access pins on its first connection (trust on first use)
UPDATE device_access SET ssh_host_key = @host_key WHERE device_id = @device_id AND kind = 'ssh';
