-- name: GetAdminPassword :one
SELECT password_hash, updated_at FROM admin_users WHERE id = 1;

-- name: SetAdminPassword :exec
INSERT INTO admin_users (id, password_hash, updated_at) VALUES (1, @password_hash, @now)
ON CONFLICT (id) DO UPDATE SET password_hash = EXCLUDED.password_hash, updated_at = EXCLUDED.updated_at;

-- a session opens only while the password checked is still the owner's: the
-- share lock holds a concurrent change until the session commits, and that
-- change then revokes it
-- name: CreateAdminSession :execrows
INSERT INTO admin_sessions (token_hash, created_at, last_seen_at, expires_at)
SELECT @token_hash, @now, @now, @expires_at FROM admin_users
WHERE id = 1 AND updated_at = @password_at
FOR SHARE;

-- name: GetAdminSession :one
SELECT id, last_seen_at, expires_at FROM admin_sessions
WHERE token_hash = @token_hash AND revoked_at IS NULL AND expires_at > @now AND last_seen_at > @idle_since;

-- name: TouchAdminSession :exec
UPDATE admin_sessions SET last_seen_at = @now WHERE id = @id AND revoked_at IS NULL;

-- name: RevokeAdminSession :exec
UPDATE admin_sessions SET revoked_at = @now WHERE token_hash = @token_hash AND revoked_at IS NULL;

-- name: RevokeAdminSessions :execrows
UPDATE admin_sessions SET revoked_at = @now WHERE revoked_at IS NULL;

-- name: PruneAdminSessions :execrows
DELETE FROM admin_sessions WHERE revoked_at IS NOT NULL OR expires_at <= @now OR last_seen_at <= @idle_since;

-- name: CreateAdminBrowser :exec
INSERT INTO admin_browsers (token_hash, created_at, expires_at) VALUES (@token_hash, @now, @expires_at);

-- name: GetAdminBrowser :one
SELECT id FROM admin_browsers WHERE token_hash = @token_hash AND revoked_at IS NULL AND expires_at > @now;

-- a failed login counts over the browser's life; the one that reaches the
-- limit revokes it
-- name: FailAdminBrowser :one
UPDATE admin_browsers SET failures = failures + 1,
    revoked_at = CASE WHEN failures + 1 >= sqlc.arg(max_failures)::integer THEN sqlc.arg(now)::timestamptz ELSE revoked_at END
WHERE id = @id AND revoked_at IS NULL
RETURNING failures;

-- name: RevokeAdminBrowserToken :exec
UPDATE admin_browsers SET revoked_at = @now WHERE token_hash = @token_hash AND revoked_at IS NULL;

-- name: RevokeAdminBrowsers :execrows
UPDATE admin_browsers SET revoked_at = @now WHERE revoked_at IS NULL;

-- name: PruneAdminBrowsers :execrows
DELETE FROM admin_browsers WHERE revoked_at IS NOT NULL OR expires_at <= @now;
