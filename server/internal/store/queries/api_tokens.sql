-- name: CreateAPIToken :execrows
INSERT INTO api_tokens (name, hash, scopes, mcp_tools, created_at) VALUES (@name, @hash, @scopes, @mcp_tools, @now)
ON CONFLICT (name) DO NOTHING;

-- name: GetAPIToken :one
SELECT id, scopes, mcp_tools, last_used_at FROM api_tokens WHERE hash = @hash;

-- a token's use is written at most once a minute
-- name: TouchAPIToken :exec
UPDATE api_tokens SET last_used_at = @now
WHERE id = @id AND (last_used_at IS NULL OR last_used_at <= @stale_before);

-- name: ListAPITokens :many
SELECT name, scopes, mcp_tools, created_at, last_used_at FROM api_tokens ORDER BY name;

-- a token's tool list changes without touching the token itself
-- name: SetAPITokenTools :execrows
UPDATE api_tokens SET mcp_tools = @mcp_tools WHERE name = @name;

-- name: DeleteAPIToken :execrows
DELETE FROM api_tokens WHERE name = @name;
