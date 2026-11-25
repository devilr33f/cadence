-- name: GetAgentMode :one
SELECT name, model, reasoning, max_steps, max_seconds, interim, tools, aliases, max_tokens, first_token_ms
FROM agent_modes
WHERE name = $1;

-- name: ListAgentModes :many
SELECT name, model, reasoning, max_steps, max_seconds, interim, tools, aliases, max_tokens, first_token_ms
FROM agent_modes
ORDER BY name COLLATE "C";

-- name: PutAgentMode :exec
INSERT INTO agent_modes (name, model, reasoning, max_steps, max_seconds, max_tokens, first_token_ms, interim, tools, aliases)
VALUES (@name, @model, @reasoning, @max_steps, @max_seconds, @max_tokens, @first_token_ms, @interim, @tools, @aliases)
ON CONFLICT (name) DO UPDATE SET
    model = EXCLUDED.model, reasoning = EXCLUDED.reasoning, max_steps = EXCLUDED.max_steps,
    max_seconds = EXCLUDED.max_seconds, max_tokens = EXCLUDED.max_tokens,
    first_token_ms = EXCLUDED.first_token_ms, interim = EXCLUDED.interim,
    tools = EXCLUDED.tools, aliases = EXCLUDED.aliases;

-- name: DeleteAgentMode :execrows
DELETE FROM agent_modes WHERE name = $1;

-- name: LockAgentModes :exec
-- the settings lock: mode puts and deletes, provider deletes and every settings
-- write take turns, so a delete never misses a setting that names its row, and
-- a setting never names a row a delete has just removed
SELECT pg_advisory_xact_lock(hashtextextended('agent_modes', 0));

-- name: ExistingAgentModes :many
SELECT name FROM agent_modes WHERE name = ANY(@names::text[]);
