-- name: ListMCPTools :many
-- every tool row of an instance, gone ones included
SELECT * FROM mcp_tools WHERE instance = @instance ORDER BY remote_name;

-- name: ListMCPToolNames :many
-- every local name of every instance, gone tools included: each stays taken while its row lives
SELECT instance, remote_name, local_name FROM mcp_tools;

-- name: SeeMCPTools :many
-- a listing: a new tool gets a row with the defaults and its hash acknowledged
-- as first seen; a known one its latest hash and name, its overrides untouched
INSERT INTO mcp_tools (instance, remote_name, local_name, schema_hash, acked_hash)
SELECT @instance::text, t.remote_name, t.local_name, t.schema_hash, t.schema_hash
FROM (SELECT unnest(@remote_names::text[]) AS remote_name, unnest(@local_names::text[]) AS local_name,
             unnest(@schema_hashes::text[]) AS schema_hash) AS t
ON CONFLICT (instance, remote_name) DO UPDATE
SET local_name = EXCLUDED.local_name, schema_hash = EXCLUDED.schema_hash, last_seen = now(), gone_since = NULL
RETURNING *;

-- name: GetMCPTool :one
SELECT * FROM mcp_tools WHERE instance = @instance AND remote_name = @remote_name;

-- name: UpdateMCPTool :one
-- the owner's overrides; a non-empty ack acknowledges that hash, and only
-- while it is the latest one listed
UPDATE mcp_tools SET
    effects = coalesce(sqlc.narg(effects), effects),
    trust = coalesce(sqlc.narg(trust), trust),
    enabled_default = coalesce(sqlc.narg(enabled_default), enabled_default),
    any_turn = coalesce(sqlc.narg(any_turn), any_turn),
    acked_hash = CASE WHEN @ack::text = '' THEN acked_hash ELSE schema_hash END
WHERE instance = @instance AND remote_name = @remote_name AND (@ack::text = '' OR @ack::text = schema_hash)
RETURNING *;

-- name: MarkGoneMCPTools :exec
-- gone_since: set when a row's instance is first found gone, cleared once a
-- provider instance of its name exists again
UPDATE mcp_tools t SET gone_since = CASE WHEN t.gone_since IS NULL THEN now() END
WHERE (t.gone_since IS NULL) = NOT EXISTS (SELECT 1 FROM provider_instances p WHERE p.name = t.instance);

-- name: PruneMCPTools :execrows
-- the rows of an instance gone keep: an instance that exists keeps every row
-- (Ruling M5d-R11)
DELETE FROM mcp_tools t
WHERE t.gone_since < now() - make_interval(secs => @keep_seconds::float8)
  AND NOT EXISTS (SELECT 1 FROM provider_instances p WHERE p.name = t.instance);

-- name: AckMCPTools :many
-- acknowledges each tool named at its hash, all or none: nothing changes
-- unless every hash given is the latest one listed
WITH want AS (
    SELECT unnest(@remote_names::text[]) AS remote_name, unnest(@hashes::text[]) AS hash
)
UPDATE mcp_tools t SET acked_hash = t.schema_hash
FROM want w
WHERE t.instance = @instance AND t.remote_name = w.remote_name AND t.schema_hash = w.hash
  AND NOT EXISTS (
      SELECT 1 FROM want x
      WHERE NOT EXISTS (
          SELECT 1 FROM mcp_tools m
          WHERE m.instance = @instance AND m.remote_name = x.remote_name AND m.schema_hash = x.hash))
RETURNING t.*;
